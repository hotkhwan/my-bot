package api

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"bottrade/internal/campaign"
	"bottrade/internal/decimal"
	"bottrade/internal/signals"

	"github.com/gofiber/fiber/v3"
)

// Goal/paper endpoints turn the /goal preview into a data-driven paper run over
// REAL Binance candles. No exchange orders are ever placed — this is simulation,
// safe by construction — but the price action and win/loss outcomes are real, so
// the stats show whether a profit goal actually holds up. Results are persisted
// per user so the dashboard can show accumulating real stats.

// goalRequest is the structured form the dashboard submits.
type goalRequest struct {
	Profit   json.Number `json:"profit"`
	Capital  json.Number `json:"capital"`
	Risk     int         `json:"risk"` // max drawdown, percent of capital
	Symbol   string      `json:"symbol"`
	Strategy string      `json:"strategy"` // "ema" | "rsi"
	Interval string      `json:"interval"` // kline interval
	Bars     int         `json:"bars"`     // history length
	UseAI    bool        `json:"use_ai"`
}

// GoalRun is a persisted summary of one paper run. It is keyed by the JWT
// subject (UserKey) so it works for any authenticated user — Telegram or
// password — not just Telegram accounts.
type GoalRun struct {
	UserKey      string    `json:"-" bson:"user_key"`
	Symbol       string    `json:"symbol" bson:"symbol"`
	Strategy     string    `json:"strategy" bson:"strategy"`
	Interval     string    `json:"interval" bson:"interval"`
	Bias         string    `json:"bias" bson:"bias"`
	UsedAI       bool      `json:"used_ai" bson:"used_ai"`
	ProfitTarget string    `json:"profit_target" bson:"profit_target"`
	Capital      string    `json:"capital" bson:"capital"`
	RiskPct      int       `json:"risk_pct" bson:"risk_pct"`
	Trades       int       `json:"trades" bson:"trades"`
	Wins         int       `json:"wins" bson:"wins"`
	Losses       int       `json:"losses" bson:"losses"`
	WinRatePct   float64   `json:"win_rate_pct" bson:"win_rate_pct"`
	RealizedPnL  string    `json:"realized_pnl" bson:"realized_pnl"`
	Verdict      string    `json:"verdict" bson:"verdict"`
	CreatedAt    time.Time `json:"created_at" bson:"created_at"`
}

// GoalRunStore persists and lists paper goal runs for a user, keyed by JWT
// subject. Community returns recent runs across ALL users (aggregate only — used
// for the leaderboard and coin suggestions; never exposes who ran what).
type GoalRunStore interface {
	Save(ctx context.Context, run GoalRun) error
	List(ctx context.Context, userKey string, limit int) ([]GoalRun, error)
	Community(ctx context.Context, limit int) ([]GoalRun, error)
}

var allowedIntervals = map[string]bool{
	"1m": true, "3m": true, "5m": true, "15m": true, "30m": true,
	"1h": true, "2h": true, "4h": true, "6h": true, "12h": true, "1d": true,
}

const (
	goalMaxBars     = 1000
	goalMinBars     = 60
	goalDefaultBars = 500
	goalHistoryMax  = 50
)

// handleGoalRun runs a paper goal over real candles and returns rich stats.
func (s *Server) handleGoalRun(c fiber.Ctx) error {
	var req goalRequest
	if err := json.Unmarshal(c.Body(), &req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid JSON body"})
	}

	goal, err := buildGoal(req)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
	}
	symbol := normalizeSymbol(req.Symbol)
	interval := req.Interval
	if !allowedIntervals[interval] {
		interval = "1h"
	}
	strategy := "ema"
	if req.Strategy == "rsi" {
		strategy = "rsi"
	}
	bars := req.Bars
	switch {
	case bars <= 0:
		bars = goalDefaultBars
	case bars < goalMinBars:
		bars = goalMinBars
	case bars > goalMaxBars:
		bars = goalMaxBars
	}

	candles, err := s.market.Candles(c.Context(), symbol, interval, bars)
	if err != nil {
		return c.Status(fiber.StatusBadGateway).JSON(fiber.Map{"error": "could not load market data for " + symbol})
	}

	bias := campaign.BiasBoth
	aiNote := ""
	if req.UseAI {
		bias, aiNote = s.aiBias(c.Context(), symbol, candles[len(candles)-1].Close)
	}

	result, err := campaign.RunPaper(campaign.PaperConfig{
		Goal: goal, Symbol: symbol, Strategy: strategy, Bias: bias,
	}, candles)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
	}

	userKey := claimsOf(c).Subject
	stats := summarize(userKey, req, interval, result)
	// Persist a summary best-effort; a storage failure must not fail the run.
	if userKey != "" {
		if err := s.goalRuns.Save(c.Context(), stats); err != nil {
			s.logger.Warn("goal run persist failed", "error", err)
		}
	}

	curve := make([]string, 0, len(result.Trades))
	trades := make([]goalTradeView, 0, len(result.Trades))
	for _, t := range result.Trades {
		curve = append(curve, t.RunningPnL.String())
		trades = append(trades, goalTradeView{
			Side:    t.Side,
			Outcome: t.Outcome,
			Entry:   t.EntryPrice,
			Exit:    t.ExitPrice,
			PnL:     t.PnLUSDT.String(),
			Win:     t.Win,
		})
	}
	return c.JSON(fiber.Map{
		"output": goalSummaryText(goal, result, aiNote),
		"ai":     aiNote,
		"curve":  curve,
		"stats":  stats,
		"trades": trades,
	})
}

// goalTradeView is the slim per-trade shape the dashboard renders.
type goalTradeView struct {
	Side    string  `json:"side"`
	Outcome string  `json:"outcome"`
	Entry   float64 `json:"entry"`
	Exit    float64 `json:"exit"`
	PnL     string  `json:"pnl"`
	Win     bool    `json:"win"`
}

// handleGoalHistory returns the user's recent paper runs (most recent first).
func (s *Server) handleGoalHistory(c fiber.Ctx) error {
	userKey := claimsOf(c).Subject
	if userKey == "" {
		return c.JSON(fiber.Map{"runs": []GoalRun{}})
	}
	runs, err := s.goalRuns.List(c.Context(), userKey, goalHistoryMax)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "could not load goal history"})
	}
	if runs == nil {
		runs = []GoalRun{}
	}
	return c.JSON(fiber.Map{"runs": runs})
}

// aiBias asks the advisor for a directional lean. It never leaks raw upstream
// errors to the client (logged instead) and degrades to a both-sides rule-based
// run whenever the advisor is absent, errors, or is neutral.
func (s *Server) aiBias(ctx context.Context, symbol string, price float64) (campaign.PaperBias, string) {
	if s.advisor == nil {
		return campaign.BiasBoth, "AI is not configured on this server — used the rule-based strategy (both sides)."
	}
	sig := signals.MarketSignal{
		Source:     "goal",
		Symbol:     symbol,
		Price:      strconv.FormatFloat(price, 'f', -1, 64),
		ReceivedAt: time.Now().UTC(),
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	decision, err := s.advisor.Decide(ctx, sig)
	if err != nil {
		s.logger.Warn("goal AI bias failed", "symbol", symbol, "error", err)
		return campaign.BiasBoth, "AI was unavailable — used the rule-based strategy (both sides)."
	}
	switch strings.ToLower(decision.Side) {
	case "long":
		return campaign.BiasLong, "AI leans long (confidence " + strconv.Itoa(decision.ConfidencePercent) + "%)."
	case "short":
		return campaign.BiasShort, "AI leans short (confidence " + strconv.Itoa(decision.ConfidencePercent) + "%)."
	default:
		return campaign.BiasBoth, "AI was neutral — used both sides."
	}
}

// buildGoal validates the structured request and reuses campaign.ParseGoal so
// the web and bot share one goal grammar and defaults.
func buildGoal(req goalRequest) (campaign.Goal, error) {
	maxUSDT := decimal.NewFromInt(1_000_000_000) // 1e9 cap keeps sizing math sane
	profit, err := decimal.Parse(strings.TrimSpace(req.Profit.String()))
	if err != nil || !profit.IsPositive() {
		return campaign.Goal{}, fmt.Errorf("profit must be a positive number of USDT")
	}
	if profit.Cmp(maxUSDT) > 0 {
		return campaign.Goal{}, fmt.Errorf("profit is too large (max 1,000,000,000 USDT)")
	}
	capitalStr := strings.TrimSpace(req.Capital.String())
	if capitalStr == "" {
		capitalStr = "100"
	}
	capital, err := decimal.Parse(capitalStr)
	if err != nil || !capital.IsPositive() {
		return campaign.Goal{}, fmt.Errorf("capital must be a positive number of USDT")
	}
	if capital.Cmp(maxUSDT) > 0 {
		return campaign.Goal{}, fmt.Errorf("capital is too large (max 1,000,000,000 USDT)")
	}
	risk := req.Risk
	if risk <= 0 {
		risk = 30
	}
	if risk > 100 {
		risk = 100
	}
	text := fmt.Sprintf("goal profit %s capital %s risk %d", profit.String(), capital.String(), risk)
	return campaign.ParseGoal(text)
}

func summarize(userKey string, req goalRequest, interval string, r campaign.PaperResult) GoalRun {
	return GoalRun{
		UserKey:      userKey,
		Symbol:       r.Symbol,
		Strategy:     r.Strategy,
		Interval:     interval,
		Bias:         string(r.Bias),
		UsedAI:       req.UseAI,
		ProfitTarget: r.Goal.TargetProfitUSDT.String(),
		Capital:      r.Goal.CapitalUSDT.String(),
		RiskPct:      r.Goal.RiskPercent(),
		Trades:       r.State.TradesClosed,
		Wins:         r.Wins,
		Losses:       r.Losses,
		WinRatePct:   r.WinRatePct,
		RealizedPnL:  r.State.RealizedPnL.String(),
		Verdict:      string(r.Verdict),
		CreatedAt:    time.Now().UTC(),
	}
}

func goalSummaryText(goal campaign.Goal, r campaign.PaperResult, aiNote string) string {
	verdict := map[campaign.Verdict]string{
		campaign.StopTargetReached: "🎯 target reached",
		campaign.StopMaxDrawdown:   "🛑 stopped: max drawdown",
		campaign.StopMaxTrades:     "⏹ stopped: max trades",
		campaign.Continue:          "… ran out of history before a stop rule",
	}[r.Verdict]
	if verdict == "" {
		verdict = string(r.Verdict)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "🎯 Goal: make %s USDT from %s USDT capital\n", goal.TargetProfitUSDT.String(), goal.CapitalUSDT.String())
	fmt.Fprintf(&b, "Strategy: %s · %s · risk %d%%\n", r.Strategy, r.Symbol, goal.RiskPercent())
	if aiNote != "" {
		fmt.Fprintf(&b, "%s\n", aiNote)
	}
	fmt.Fprintf(&b, "\n📊 Paper run on real %s candles (no real orders): %s\n", r.Symbol, verdict)
	fmt.Fprintf(&b, "Trades: %d (%d win / %d loss) · Win rate: %.0f%% · Final PnL: %s USDT",
		r.State.TradesClosed, r.Wins, r.Losses, r.WinRatePct, r.State.RealizedPnL.String())
	return b.String()
}

// memGoalRuns is the default in-process store: a capped per-user history. It is
// fine for single-instance/testing; production wires a Mongo-backed store so
// stats survive restarts and are shared across instances.
type memGoalRuns struct {
	mu   sync.Mutex
	cap  int
	runs map[string][]GoalRun
}

func newMemGoalRuns(capacity int) *memGoalRuns {
	if capacity <= 0 {
		capacity = 100
	}
	return &memGoalRuns{cap: capacity, runs: make(map[string][]GoalRun)}
}

func (m *memGoalRuns) Save(_ context.Context, run GoalRun) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	list := append(m.runs[run.UserKey], run)
	if len(list) > m.cap {
		list = list[len(list)-m.cap:]
	}
	m.runs[run.UserKey] = list
	return nil
}

func (m *memGoalRuns) List(_ context.Context, userKey string, limit int) ([]GoalRun, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	src := m.runs[userKey]
	out := make([]GoalRun, len(src))
	copy(out, src)
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (m *memGoalRuns) Community(_ context.Context, limit int) ([]GoalRun, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []GoalRun
	for _, runs := range m.runs {
		out = append(out, runs...)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}
