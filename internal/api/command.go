package api

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"bottrade/internal/backtest"
	"bottrade/internal/campaign"
	"bottrade/internal/journal"
	"bottrade/internal/marketdata"

	"github.com/gofiber/fiber/v3"
)

// handleCommand runs a read/analysis command from the web console and returns
// its text output. It mirrors the bot's safe commands (help, market, goal,
// backtest, report) so the dashboard is a single entry point; order placement
// and campaigns stay on their own audited paths. Authenticated via requireAuth.
func (s *Server) handleCommand(c fiber.Ctx) error {
	var body struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal(c.Body(), &body); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid JSON body"})
	}

	text := strings.TrimSpace(body.Command)
	text = strings.TrimPrefix(text, "/")
	word, rest, _ := strings.Cut(text, " ")
	rest = strings.TrimSpace(rest)

	switch strings.ToLower(word) {
	case "", "help":
		return c.JSON(fiber.Map{"output": webCommandHelp})
	case "market":
		return c.JSON(fiber.Map{"output": s.commandMarket(c, rest)})
	case "goal":
		return c.JSON(fiber.Map{"output": commandGoal(text)})
	case "backtest":
		return c.JSON(fiber.Map{"output": s.commandBacktest(c, rest)})
	case "report":
		return c.JSON(fiber.Map{"output": s.commandReport(c)})
	default:
		return c.JSON(fiber.Map{"output": "Unknown command. Type help.\n\n" + webCommandHelp})
	}
}

const webCommandHelp = `Web console commands:
  market BTC            live order-flow the AI reads (funding, OI, long/short, taker)
  goal profit 10 risk 50    preview a profit plan (simulation, no real orders)
  backtest BTC          backtest EMA-cross / RSI on history
  report                your win-rate / PnL
  help                  this list

Trading and autonomous campaigns run from the Telegram bot for now.`

func (s *Server) commandMarket(c fiber.Ctx, arg string) string {
	symbol := normalizeSymbol(arg)
	snap, err := marketdata.Collect(c.Context(), s.market, symbol, s.cfg.AI.MarketDataPeriod, time.Now().UTC())
	if err != nil && snap.Funding.MarkPrice.IsZero() {
		return "Could not fetch market data for " + symbol + "."
	}
	var b strings.Builder
	fmt.Fprintf(&b, "📊 %s market data (%s)", snap.Symbol, snap.Period)
	if snap.Funding.MarkPrice.IsPositive() {
		fmt.Fprintf(&b, "\nMark price: %s", snap.Funding.MarkPrice.String())
		fmt.Fprintf(&b, "\nFunding rate: %s (per 8h)", snap.Funding.LastFundingRate.String())
	}
	if snap.OpenInterest.OpenInterest.IsPositive() {
		fmt.Fprintf(&b, "\nOpen interest: %s", snap.OpenInterest.OpenInterest.String())
	}
	if snap.LongShort.Ratio.IsPositive() {
		fmt.Fprintf(&b, "\nLong/Short accounts: %s (long %s / short %s)",
			snap.LongShort.Ratio.String(), snap.LongShort.LongAccount.String(), snap.LongShort.ShortAccount.String())
	}
	if snap.Taker.BuySellRatio.IsPositive() {
		fmt.Fprintf(&b, "\nTaker buy/sell: %s", snap.Taker.BuySellRatio.String())
	}
	return b.String()
}

func commandGoal(text string) string {
	goal, err := campaign.ParseGoal(text)
	if err != nil {
		return err.Error() + "\n\nExample: goal profit 10 risk 50"
	}
	estimate, err := campaign.EstimateTrades(goal)
	if err != nil {
		return "⚠️ " + err.Error()
	}
	result := campaign.Simulate(goal)
	wins := 0
	for _, o := range result.Outcomes {
		if o.Win {
			wins++
		}
	}
	verdict := map[campaign.Verdict]string{
		campaign.StopTargetReached: "🎯 target reached",
		campaign.StopMaxDrawdown:   "🛑 stopped: max drawdown",
		campaign.StopMaxTrades:     "⏹ stopped: max trades",
	}[result.Verdict]
	if verdict == "" {
		verdict = string(result.Verdict)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "🎯 Goal: make %s USDT from %s USDT capital\n", goal.TargetProfitUSDT.String(), goal.CapitalUSDT.String())
	fmt.Fprintf(&b, "Risk: up to %d%% of capital (stop at -%s USDT), assumed win-rate %d%%\n",
		goal.RiskPercent(), goal.MaxDrawdownUSDT.String(), goal.AssumedWinRate)
	fmt.Fprintf(&b, "Expectancy: %s USDT/trade → ~%d trades needed\n\n", goal.ExpectedPerTrade().String(), estimate)
	fmt.Fprintf(&b, "📊 Simulation (no real orders): %s\n", verdict)
	fmt.Fprintf(&b, "Trades: %d (%d win / %d loss) · Final PnL: %s USDT", result.State.TradesClosed, wins, result.State.TradesClosed-wins, result.State.RealizedPnL.String())
	return b.String()
}

func (s *Server) commandBacktest(c fiber.Ctx, arg string) string {
	symbol := normalizeSymbol(arg)
	closes, err := s.market.Closes(c.Context(), symbol, "1h", 500)
	if err != nil {
		return "Could not fetch history for " + symbol + "."
	}
	strategies := []backtest.Strategy{
		backtest.EMACrossStrategy{Fast: 12, Slow: 26},
		backtest.RSIReversionStrategy{Period: 14, Low: 30, High: 70},
	}
	var b strings.Builder
	fmt.Fprintf(&b, "🧪 Backtest %s (1h, %d bars, fee 0.04%%/side)", symbol, len(closes))
	for _, strat := range strategies {
		r, err := backtest.Run(closes, strat, backtest.Config{FeeRate: 0.0004})
		if err != nil {
			fmt.Fprintf(&b, "\n\n%s: %v", strat.Name(), err)
			continue
		}
		fmt.Fprintf(&b, "\n\n%s\nTrades: %d | Win rate: %.0f%%\nReturn: %.2f%% | Max DD: %.2f%%",
			r.Strategy, r.Trades, r.WinRatePct, r.ReturnPct, r.MaxDrawdownPct)
	}
	b.WriteString("\n\nPast performance is not indicative of future results.")
	return b.String()
}

func (s *Server) commandReport(c fiber.Ctx) string {
	if s.report == nil {
		return "Reporting is not enabled."
	}
	filter := journal.Filter{ClosedOnly: true}
	// JWT subject is "tg:<id>"; the journal keys trades by the numeric id.
	if id := strings.TrimPrefix(claimsOf(c).Subject, "tg:"); id != claimsOf(c).Subject {
		if n, err := strconv.ParseInt(id, 10, 64); err == nil {
			filter.UserID = n
		}
	}
	report, err := s.report.Report(c.Context(), filter)
	if err != nil {
		return "Could not load your report."
	}
	return fmt.Sprintf("📈 Your performance\nTrades: %d (%d win / %d loss)\nWin rate: %s%%\nTotal PnL: %s USDT\nExpectancy: %s",
		report.Trades, report.Wins, report.Losses, report.WinRate, report.TotalPnL, report.Expectancy)
}

// normalizeSymbol upper-cases a symbol arg and appends USDT, defaulting to
// BTCUSDT.
func normalizeSymbol(arg string) string {
	value := strings.ToUpper(strings.TrimSpace(arg))
	value = strings.ReplaceAll(value, "/", "")
	if first, _, ok := strings.Cut(value, " "); ok {
		value = first
	}
	if value == "" {
		return "BTCUSDT"
	}
	if !strings.HasSuffix(value, "USDT") {
		value += "USDT"
	}
	return value
}
