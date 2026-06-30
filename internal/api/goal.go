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
	"bottrade/internal/marketdata"
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
	Profit         json.Number `json:"profit"`
	Capital        json.Number `json:"capital"`
	Risk           int         `json:"risk"` // legacy alias for CapitalRiskPct
	CapitalRiskPct int         `json:"capital_risk_pct"`
	LeverageUsePct int         `json:"leverage_use_pct"`
	Duration       string      `json:"duration"`
	Interval       string      `json:"interval"` // legacy client fallback
	Symbol         string      `json:"symbol"`
	Strategy       string      `json:"strategy"`
	UseAI          bool        `json:"use_ai"`
}

// GoalRun is a persisted summary of one paper run. It is keyed by the JWT
// subject (UserKey) so it works for any authenticated user — Telegram or
// password — not just Telegram accounts.
type GoalRun struct {
	UserKey          string    `json:"-" bson:"user_key"`
	Symbol           string    `json:"symbol" bson:"symbol"`
	Strategy         string    `json:"strategy" bson:"strategy"`
	Interval         string    `json:"interval" bson:"interval"`
	Duration         string    `json:"duration" bson:"duration"`
	Bias             string    `json:"bias" bson:"bias"`
	UsedAI           bool      `json:"used_ai" bson:"used_ai"`
	ProfitTarget     string    `json:"profit_target" bson:"profit_target"`
	Capital          string    `json:"capital" bson:"capital"`
	RiskPct          int       `json:"risk_pct" bson:"risk_pct"`
	LeverageUsePct   int       `json:"leverage_use_pct" bson:"leverage_use_pct"`
	Trades           int       `json:"trades" bson:"trades"`
	Wins             int       `json:"wins" bson:"wins"`
	Losses           int       `json:"losses" bson:"losses"`
	WinRatePct       float64   `json:"win_rate_pct" bson:"win_rate_pct"`
	RealizedPnL      string    `json:"realized_pnl" bson:"realized_pnl"`
	Verdict          string    `json:"verdict" bson:"verdict"`
	Validation       string    `json:"validation_window" bson:"validation_window"`
	EstimatedEntries int       `json:"estimated_entries" bson:"estimated_entries"`
	SignalSetups     int       `json:"signal_setups" bson:"signal_setups"`
	ExpectancyUSDT   string    `json:"expectancy_per_trade" bson:"expectancy_per_trade"`
	RewardRisk       string    `json:"reward_risk" bson:"reward_risk"`
	Launchable       bool      `json:"launchable" bson:"launchable"`
	Actionable       bool      `json:"actionable" bson:"actionable"`
	NeedsPlanEdit    bool      `json:"needs_plan_edit" bson:"needs_plan_edit"`
	BlockedReason    string    `json:"blocked_reason,omitempty" bson:"blocked_reason,omitempty"`
	TopBlocker       string    `json:"top_blocker,omitempty" bson:"top_blocker,omitempty"`
	PlanHint         string    `json:"plan_hint,omitempty" bson:"plan_hint,omitempty"`
	CreatedAt        time.Time `json:"created_at" bson:"created_at"`
}

// GoalRunStore persists and lists paper goal runs for a user, keyed by JWT
// subject. Community returns recent runs across ALL users (aggregate only — used
// for the leaderboard and coin suggestions; never exposes who ran what).
type GoalRunStore interface {
	Save(ctx context.Context, run GoalRun) error
	List(ctx context.Context, userKey string, limit int) ([]GoalRun, error)
	Community(ctx context.Context, limit int) ([]GoalRun, error)
}

type durationSpec struct {
	ExecutionInterval string
	PlanBars          int
}

var allowedDurations = map[string]durationSpec{
	"15m": {ExecutionInterval: "1m", PlanBars: 15},
	"1h":  {ExecutionInterval: "1m", PlanBars: 60},
	"2h":  {ExecutionInterval: "1m", PlanBars: 120},
	"4h":  {ExecutionInterval: "5m", PlanBars: 48},
	"8h":  {ExecutionInterval: "5m", PlanBars: 96},
	"12h": {ExecutionInterval: "5m", PlanBars: 144},
	"24h": {ExecutionInterval: "15m", PlanBars: 96},
	"48h": {ExecutionInterval: "15m", PlanBars: 192},
	"1w":  {ExecutionInterval: "1h", PlanBars: 168},
}

const (
	goalHistoryMax              = 500
	annyBasicPaperExecutionBars = 10080 // 7 days of 1m candles; ANNY Basic setups are intentionally sparse.
	annyBasicPaperMainBars      = 1000
	goalAIDirectionalConfidence = 50
	// minLaunchSample is the absolute floor of trades for any launchable plan, so a
	// single lucky fill can never pass the gate — even one that "reached" a trivial
	// target.
	minLaunchSample = 2
	// minLaunchTrades is the sample required to call a plan launchable when it did
	// NOT reach its profit target: without a target hit, a short window must instead
	// prove a positive edge over at least this many trades.
	minLaunchTrades = 5
	// unlimitedDuration runs the paper engine over a long recent window with no
	// fixed plan timebox, resolving when the goal hits its profit target or its
	// drawdown stop — whichever comes first — instead of stopping at an arbitrary
	// duration. History is finite, so the window is capped at unlimitedPaperBars;
	// if neither target nor stop is reached the verdict is "continue".
	unlimitedDuration      = "unlimited"
	unlimitedPaperBars     = 3000 // ~10 days at 5m; the provider paginates beyond 1000
	unlimitedPaperInterval = "5m"
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
	duration := strings.ToLower(strings.TrimSpace(req.Duration))
	unlimited := duration == unlimitedDuration || duration == "∞"
	spec, ok := allowedDurations[duration]
	if !ok && !unlimited {
		duration = "1h"
		spec = allowedDurations[duration]
	}
	if req.LeverageUsePct <= 0 {
		req.LeverageUsePct = 25
	}
	if req.LeverageUsePct > 100 {
		req.LeverageUsePct = 100
	}
	// The leverage-use slider sizes the per-trade bracket so an aggressive plan
	// reaches its $ target in fewer trades; RR is preserved (reward = 2× risk).
	goal = applyLeverageSizing(goal, req.LeverageUsePct)
	interval := spec.ExecutionInterval
	paperPlanBars := spec.PlanBars
	validation := duration
	strategy := "ema"
	switch req.Strategy {
	case "rsi", "macd", "sma", "breakout", "auto", "anny_basic":
		strategy = req.Strategy
	}
	bars := spec.PlanBars + 40 // warmup + a small volatility lookback
	if unlimited {
		// Run open-ended: entries allowed on every bar after warmup, the max-trades
		// cap removed, so the run stops only on target reached or drawdown — whichever
		// comes first — across a long recent window.
		duration = unlimitedDuration
		interval = unlimitedPaperInterval
		bars = unlimitedPaperBars
		paperPlanBars = 0
		validation = "until target or stop"
		goal.MaxTrades = 0
	}
	if strategy == "anny_basic" {
		// ANNY Basic uses sparse 15m CDC color changes plus QQE crosses. A 15m
		// live plan may legitimately contain no setup, so paper assessment uses a
		// longer recent validation sample to calculate trade count / W-L / win rate.
		interval = "1m"
		bars = annyBasicPaperExecutionBars
		paperPlanBars = 0
		validation = fmt.Sprintf("%d x 1m", annyBasicPaperExecutionBars)
	}

	candles, err := s.market.Candles(c.Context(), symbol, interval, bars)
	if err != nil {
		return c.Status(fiber.StatusBadGateway).JSON(fiber.Map{"error": "could not load market data for " + symbol})
	}
	if strategy == "anny_basic" {
		validation = fmt.Sprintf("%d x %s", len(candles), interval)
	}
	var mainCandles []marketdata.Candle
	if strategy == "anny_basic" {
		mainCandles, err = s.market.Candles(c.Context(), symbol, "15m", annyBasicPaperMainBars)
		if err != nil {
			return c.Status(fiber.StatusBadGateway).JSON(fiber.Map{"error": "could not load 15m model data for " + symbol})
		}
	}

	bias := campaign.BiasBoth
	aiNote := ""
	if req.UseAI {
		bias, aiNote = s.aiBias(c.Context(), claimsOf(c).Subject, symbol, candles[len(candles)-1].Close)
	}

	result, err := campaign.RunPaper(campaign.PaperConfig{
		Goal: goal, Symbol: symbol, Strategy: strategy, Bias: bias,
		PlanBars: paperPlanBars, MainCandles: mainCandles,
	}, candles)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
	}

	userKey := claimsOf(c).Subject
	stats := summarize(userKey, req, duration, interval, validation, result)
	// Persist a summary best-effort; a storage failure must not fail the run.
	if userKey != "" && actionableGoalRun(stats) {
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
		"output": goalSummaryText(goal, result, stats, aiNote),
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
	runs = filterActionableGoalRuns(runs)
	if runs == nil {
		runs = []GoalRun{}
	}
	return c.JSON(fiber.Map{"runs": runs})
}

// aiBias asks the advisor for a directional lean. It never leaks raw upstream
// errors to the client (logged instead) and degrades to a both-sides rule-based
// run whenever the advisor is absent, errors, or is neutral.
func (s *Server) aiBias(ctx context.Context, subject, symbol string, price float64) (campaign.PaperBias, string) {
	advisor := s.userAdvisor(ctx, subject) // a user's own key takes priority
	byo := advisor != nil
	if advisor == nil {
		advisor = s.advisor
	}
	if advisor == nil {
		return campaign.BiasBoth, "AI is not configured — add your own AI key in Settings, or it falls back to the rule-based strategy."
	}
	// A user's own key isn't metered (it's their quota). The shared server AI is
	// metered too — except it's free for the admin and approved crew until the
	// public free tier opens (PRIVATE_BETA=false).
	meter := !byo && !s.aiFreeForSubject(ctx, subject)
	if meter {
		if allowed, msg := s.allow(ctx, subject, "ai"); !allowed {
			return campaign.BiasBoth, "🔒 " + msg + " (used the rule-based strategy)."
		}
	}
	sig := signals.MarketSignal{
		Source:     "goal",
		Symbol:     symbol,
		Price:      strconv.FormatFloat(price, 'f', -1, 64),
		ReceivedAt: time.Now().UTC(),
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	decision, err := advisor.Decide(ctx, sig)
	if err != nil {
		s.logger.Warn("goal AI bias failed", "symbol", symbol, "byo", byo, "error", err)
		// Show the real upstream reason to the admin (so misconfig — bad key, wrong
		// model — is diagnosable), but never to ordinary users.
		if s.isAdminSubject(subject) {
			return campaign.BiasBoth, "AI error (admin): " + err.Error() + " — used the rule-based strategy."
		}
		return campaign.BiasBoth, "AI was unavailable — used the rule-based strategy (both sides)."
	}
	if meter {
		s.usage.Incr(subject, "ai") // count a metered shared-server AI run toward the daily limit
	}
	who := "AI"
	if byo {
		who = "Your AI"
	}
	if decision.ConfidencePercent > 0 && decision.ConfidencePercent < goalAIDirectionalConfidence {
		return campaign.BiasBoth, who + " confidence " + strconv.Itoa(decision.ConfidencePercent) + "% is low - used both sides."
	}
	switch strings.ToLower(decision.Side) {
	case "long":
		return campaign.BiasLong, who + " leans long (confidence " + strconv.Itoa(decision.ConfidencePercent) + "%)."
	case "short":
		return campaign.BiasShort, who + " leans short (confidence " + strconv.Itoa(decision.ConfidencePercent) + "%)."
	default:
		return campaign.BiasBoth, who + " was neutral — used both sides."
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
	risk := req.CapitalRiskPct
	if risk == 0 {
		risk = req.Risk
	}
	if risk <= 0 {
		risk = 30
	}
	if risk > 100 {
		risk = 100
	}
	text := fmt.Sprintf("goal profit %s capital %s risk %d", profit.String(), capital.String(), risk)
	return campaign.ParseGoal(text)
}

// applyLeverageSizing scales the goal's per-trade reward/risk by the leverage-use
// slider so an aggressive plan reaches its $ target in fewer trades while a
// conservative one keeps the original small bracket. The reward:risk multiple is
// preserved (reward = 2× risk), so this changes position size, not the strategy's
// edge — win rate and TP/SL geometry are untouched in the paper run. Per-trade
// risk is clamped to 20% of capital and to the drawdown budget so no single trade
// can reach the whole target or blow the stop in one shot.
//
// The slider maps linearly at 4 basis points of capital per percent: the default
// 25% reproduces the legacy 1%-risk / 2%-reward bracket, 50% doubles it, 100%
// quadruples it.
func applyLeverageSizing(goal campaign.Goal, leverageUsePct int) campaign.Goal {
	if leverageUsePct <= 0 || !goal.CapitalUSDT.IsPositive() {
		return goal
	}
	riskBp := int64(leverageUsePct) * 4 // 25%→100bp(1%), 50%→200bp(2%), 100%→400bp(4%)
	if riskBp > 2000 {                  // cap per-trade risk at 20% of capital
		riskBp = 2000
	}
	risk, err := goal.CapitalUSDT.Mul(decimal.NewFromInt(riskBp)).QuoFloor(decimal.NewFromInt(10000), 8)
	if err != nil || !risk.IsPositive() {
		return goal
	}
	// One losing trade must never exceed the run's whole drawdown budget.
	if goal.MaxDrawdownUSDT.IsPositive() && risk.Cmp(goal.MaxDrawdownUSDT) > 0 {
		risk = goal.MaxDrawdownUSDT
	}
	goal.RiskPerTradeUSDT = risk
	goal.RewardPerTradeUSDT = risk.Mul(decimal.NewFromInt(2)) // preserve RR 2:1
	return goal
}

func summarize(userKey string, req goalRequest, duration, interval, validation string, r campaign.PaperResult) GoalRun {
	estimate, _ := campaign.EstimateTrades(r.Goal)
	stats := GoalRun{
		UserKey:          userKey,
		Symbol:           r.Symbol,
		Strategy:         r.Strategy,
		Interval:         interval,
		Duration:         duration,
		Bias:             string(r.Bias),
		UsedAI:           req.UseAI,
		ProfitTarget:     r.Goal.TargetProfitUSDT.String(),
		Capital:          r.Goal.CapitalUSDT.String(),
		RiskPct:          r.Goal.RiskPercent(),
		LeverageUsePct:   req.LeverageUsePct,
		Trades:           r.State.TradesClosed,
		Wins:             r.Wins,
		Losses:           r.Losses,
		WinRatePct:       r.WinRatePct,
		RealizedPnL:      r.State.RealizedPnL.String(),
		Verdict:          string(r.Verdict),
		Validation:       validation,
		EstimatedEntries: estimate,
		SignalSetups:     r.Diagnostics.SetupsFound,
		ExpectancyUSDT:   expectancyPerTrade(r),
		RewardRisk:       rewardRiskRatio(r.Goal),
		Actionable:       r.State.TradesClosed > 0,
		CreatedAt:        time.Now().UTC(),
	}
	// Launchable means the plan showed a genuine, RR-adjusted edge — not a 1-trade
	// fluke. Two ways to qualify, both needing positive realized PnL (fees netted)
	// and at least minLaunchSample trades so a single lucky fill can't pass:
	//   - it actually REACHED its profit target (target_reached) — hitting the goal
	//     IS the success signal, even when sparse strategies (e.g. ANNY Basic) need
	//     only a couple of entries; or
	//   - it didn't reach target but proved a positive edge over a fuller sample
	//     (>= minLaunchTrades), so a short non-target window still needs more proof.
	// This is why a high-RR plan can launch below 50% win rate.
	positiveEdge := r.State.RealizedPnL.IsPositive()
	hitTarget := r.Verdict == campaign.StopTargetReached
	stats.Launchable = positiveEdge && stats.Trades >= minLaunchSample &&
		(hitTarget || stats.Trades >= minLaunchTrades)
	if r.State.TradesClosed == 0 {
		stats.Actionable = false
		stats.NeedsPlanEdit = true
		stats.BlockedReason = noSetupReason(r)
		stats.TopBlocker = r.Diagnostics.TopBlocker
		stats.PlanHint = planEditHint(r)
	}
	return stats
}

// expectancyPerTrade is the realized average PnL per closed trade (fees included),
// the RR-and-win-rate-adjusted edge the launch gate is built on. Empty when no
// trade closed.
func expectancyPerTrade(r campaign.PaperResult) string {
	if r.State.TradesClosed <= 0 {
		return ""
	}
	exp, err := r.State.RealizedPnL.QuoFloor(decimal.NewFromInt(int64(r.State.TradesClosed)), 4)
	if err != nil {
		return ""
	}
	return exp.String()
}

// rewardRiskRatio reports the goal's structural reward:risk (e.g. "2") for display,
// so the user can see the system never trades a 1:1 bracket. Empty when undefined.
func rewardRiskRatio(goal campaign.Goal) string {
	if !goal.RiskPerTradeUSDT.IsPositive() {
		return ""
	}
	rr, err := goal.RewardPerTradeUSDT.QuoFloor(goal.RiskPerTradeUSDT, 1)
	if err != nil {
		return ""
	}
	return rr.String()
}

func noSetupReason(r campaign.PaperResult) string {
	if strings.HasPrefix(r.Strategy, "anny_basic") {
		return annyBasicBlockedReason(r)
	}
	return "No trade setup in this validation window"
}

func annyBasicBlockedReason(r campaign.PaperResult) string {
	if r.Diagnostics.SetupsFound > 0 && r.Diagnostics.BiasRejected > 0 {
		return "AI side filter rejected all launchable ANNY Basic setups"
	}
	switch r.Diagnostics.TopBlocker {
	case "no-trade market condition", "abnormal volatility", "sideways market",
		"entry extended from trend", "execution not aligned":
		return "ANNY Basic market-condition filter blocked this validation window"
	case "CDC and QQE are not aligned":
		return "CDC/QQE did not align in this validation window"
	case "indicator warmup":
		return "ANNY Basic indicator warmup left no launchable setup"
	case "AI side filter":
		return "AI side filter rejected all launchable ANNY Basic setups"
	default:
		return "No launchable ANNY Basic setup in this validation window"
	}
}

func planEditHint(r campaign.PaperResult) string {
	if !strings.HasPrefix(r.Strategy, "anny_basic") {
		return "Try another duration, another symbol, or another strategy, then assess again."
	}
	if r.Diagnostics.SetupsFound > 0 && r.Diagnostics.BiasRejected > 0 {
		return "The model found a setup, but the AI side filter rejected it. Try disabling AI side pick or reassess when AI confidence is higher."
	}
	switch r.Diagnostics.TopBlocker {
	case "CDC and QQE are not aligned":
		return "Wait for CDC/QQE alignment, try another symbol, or use Auto/RSI for paper assessment while ANNY Basic waits."
	case "no-trade market condition", "abnormal volatility", "sideways market",
		"entry extended from trend", "execution not aligned":
		return "Try another symbol, use Auto/RSI for paper assessment, or wait for a cleaner, less extended move."
	case "indicator warmup":
		return "Market data loaded but the model needed more aligned 15m/1m warmup. Reassess in a moment or try a longer duration."
	case "":
		return "Try another symbol, longer duration, or a non-ANNY Basic paper strategy before launching."
	default:
		return "Top blocker: " + r.Diagnostics.TopBlocker + ". Edit the symbol, duration, or side filter, then assess again."
	}
}

func actionableGoalRun(r GoalRun) bool {
	return r.Trades > 0 && !r.NeedsPlanEdit
}

func filterActionableGoalRuns(runs []GoalRun) []GoalRun {
	if len(runs) == 0 {
		return runs
	}
	out := runs[:0]
	for _, run := range runs {
		if actionableGoalRun(run) {
			out = append(out, run)
		}
	}
	return out
}

func goalSummaryText(goal campaign.Goal, r campaign.PaperResult, stats GoalRun, aiNote string) string {
	verdict := map[campaign.Verdict]string{
		campaign.StopTargetReached: "🎯 target reached",
		campaign.StopMaxDrawdown:   "🛑 stopped: max drawdown",
		campaign.StopMaxTrades:     "⏹ stopped: max trades",
		campaign.StopStrategyRule:  "stopped: strategy risk rule",
		campaign.Continue:          "target not reached in this window",
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
	if stats.NeedsPlanEdit {
		fmt.Fprintf(&b, "\n📊 Plan assessment on real %s candles: edit plan\n", r.Symbol)
		fmt.Fprintf(&b, "%s. Market data loaded, but no paper result is launchable from this window. Entries needed by goal math: %d. Launchable setups found: %d.",
			stats.BlockedReason, stats.EstimatedEntries, stats.SignalSetups)
		if stats.TopBlocker != "" {
			fmt.Fprintf(&b, " Top blocker: %s.", stats.TopBlocker)
		}
		if stats.PlanHint != "" {
			fmt.Fprintf(&b, " Next edit: %s", stats.PlanHint)
		}
		return b.String()
	}
	fmt.Fprintf(&b, "\n📊 Paper run on real %s candles (no real orders): %s\n", r.Symbol, verdict)
	if stats.EstimatedEntries > 0 {
		fmt.Fprintf(&b, "Entries needed by goal math: %d\n", stats.EstimatedEntries)
	}
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
