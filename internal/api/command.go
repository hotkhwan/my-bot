package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"bottrade/internal/backtest"
	"bottrade/internal/campaign"
	"bottrade/internal/journal"
	"bottrade/internal/marketdata"
	"bottrade/internal/orders"
	"bottrade/internal/parser"

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
		output, curve := commandGoal(text)
		return c.JSON(fiber.Map{"output": output, "curve": curve})
	case "backtest":
		return c.JSON(fiber.Map{"output": s.commandBacktest(c, rest)})
	case "report":
		return c.JSON(fiber.Map{"output": s.commandReport(c)})
	default:
		return s.commandTrade(c, text)
	}
}

// commandTrade parses a trade command and prepares it, returning a confirm_id
// the dashboard turns into a Confirm button. Placement happens in handleConfirm.
// Same order service, same testnet/dry-run gates as the bot.
func (s *Server) commandTrade(c fiber.Ctx, text string) error {
	if s.orders == nil {
		return c.JSON(fiber.Map{"output": "Trading from the web is not enabled — use the Telegram bot."})
	}
	intent, err := s.parser.Parse(text)
	if err != nil {
		// Surface the parser's specific guidance (e.g. "size must use
		// <amount>usdt") instead of a generic message, so a slightly-off trade
		// is correctable from the web just like in Telegram. The catch-all stays
		// for words the parser doesn't recognize at all.
		var ve parser.ValidationError
		if errors.As(err, &ve) && !strings.Contains(ve.Message, "not recognized") {
			return c.JSON(fiber.Map{"output": "⚠️ " + ve.Message})
		}
		return c.JSON(fiber.Map{"output": "Unknown command. Type help.\n\n" + webCommandHelp})
	}
	if !intent.IsExchangeChanging() {
		return c.JSON(fiber.Map{"output": "That command isn't available on the web yet — try it in Telegram."})
	}
	userID, ok := webUserID(c)
	if !ok {
		return c.JSON(fiber.Map{"output": "Trading needs a Telegram login (the key is tied to your Telegram account)."})
	}
	confirmation, err := s.orders.Prepare(c.Context(), userID, intent)
	if err != nil {
		return c.JSON(fiber.Map{"output": "⚠️ " + err.Error()})
	}
	return c.JSON(fiber.Map{
		"output":     "Review this action:\n\n" + orders.Summary(intent),
		"confirm_id": confirmation.ID,
	})
}

// handleConfirm executes (or cancels) a prepared confirmation from the web.
func (s *Server) handleConfirm(c fiber.Ctx) error {
	if s.orders == nil {
		return c.Status(fiber.StatusNotImplemented).JSON(fiber.Map{"error": "trading is not enabled"})
	}
	var body struct {
		ID     string `json:"id"`
		Cancel bool   `json:"cancel"`
	}
	if err := json.Unmarshal(c.Body(), &body); err != nil || strings.TrimSpace(body.ID) == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "missing confirmation id"})
	}
	userID, ok := webUserID(c)
	if !ok {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "telegram login required"})
	}

	if body.Cancel {
		s.cancelAwaitingScheduledClose(c.Context(), body.ID, "mission cancelled")
		if err := s.orders.Cancel(c.Context(), userID, body.ID); err != nil {
			return c.JSON(fiber.Map{"output": "⚠️ " + err.Error()})
		}
		return c.JSON(fiber.Map{"output": "Cancelled."})
	}

	var result orders.ExecutionResult
	var err error
	if s.armedMissionRuntimeAllowed() {
		if !s.hasActiveKeyForSubject(c.Context(), claimsOf(c).Subject) {
			return c.JSON(fiber.Map{
				"output":   "🔑 No active testnet Binance key is available for this confirmation. Open Settings, make a testnet key active, then stage the Mission again.",
				"need_key": true,
			})
		}
		result, err = s.orders.ConfirmWithRequiredUserExecutor(c.Context(), userID, body.ID)
	} else {
		result, err = s.orders.Confirm(c.Context(), userID, body.ID)
	}
	if err != nil {
		// Do NOT cancel the awaiting close here: a duplicate/concurrent confirm can
		// hit "already executing" while the first call is still placing the entry.
		// Cancelling now would drop the close for an entry that then succeeds. The
		// reconciler resolves the awaiting close from the confirmation's durable
		// status (executed → activate, failed/cancelled/expired → cancel).
		return c.JSON(fiber.Map{"output": "⚠️ " + err.Error()})
	}
	// Entry executed → arm the durable plan-end close (safe no-op if none awaits).
	s.activateScheduledClose(c.Context(), body.ID)
	out := result.Message
	if strings.TrimSpace(out) == "" {
		out = "✅ " + result.Mode + " " + result.ClientOrderID
	}
	return c.JSON(fiber.Map{"output": out})
}

// handleHistory returns the authenticated user's performance report, including
// the per-coin breakdown the History page renders as cards.
func (s *Server) handleHistory(c fiber.Ctx) error {
	if s.report == nil {
		return c.Status(fiber.StatusNotImplemented).JSON(fiber.Map{"error": "reporting is not enabled"})
	}
	filter := journal.Filter{}
	if userID, ok := webUserID(c); ok {
		filter.UserID = userID
	}
	report, err := s.report.Report(c.Context(), filter)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "could not load history"})
	}
	return c.JSON(report)
}

// handleSymbols searches the tradable Binance Futures symbols for the coin
// picker. Authenticated; the list itself is public data.
func (s *Server) handleSymbols(c fiber.Ctx) error {
	symbols, err := s.market.SearchSymbols(c.Context(), c.Query("q"), 25)
	if err != nil {
		return c.Status(fiber.StatusBadGateway).JSON(fiber.Map{"error": "could not load symbols"})
	}
	return c.JSON(fiber.Map{"symbols": symbols})
}

// handlePrices returns last price and 24h change percent for the comma-separated
// symbols in ?symbols=, so the dashboard can show live quotes beside favourites.
// Authenticated; the quotes themselves are public data.
func (s *Server) handlePrices(c fiber.Ctx) error {
	var symbols []string
	for _, part := range strings.Split(c.Query("symbols"), ",") {
		if v := strings.ToUpper(strings.TrimSpace(part)); v != "" {
			symbols = append(symbols, v)
		}
	}
	if len(symbols) > 50 {
		symbols = symbols[:50]
	}
	prices, err := s.market.Tickers(c.Context(), symbols)
	if err != nil {
		return c.Status(fiber.StatusBadGateway).JSON(fiber.Map{"error": "could not load prices"})
	}
	return c.JSON(fiber.Map{"prices": prices})
}

// webUserID extracts the numeric Telegram id from a "tg:<id>" JWT subject.
func webUserID(c fiber.Ctx) (int64, bool) {
	subject := claimsOf(c).Subject
	id := strings.TrimPrefix(subject, "tg:")
	if id == subject {
		return 0, false
	}
	n, err := strconv.ParseInt(id, 10, 64)
	if err != nil {
		return 0, false
	}
	return n, true
}

const webCommandHelp = `Web console commands:
  market BTC            live order-flow the AI reads (funding, OI, long/short, taker)
  goal profit 10 risk 50    preview a profit plan (simulation, no real orders)
  backtest BTC          backtest EMA-cross / RSI on history
  report                your win-rate / PnL

  Trade (needs a Telegram login + stored key; you'll Confirm before it places):
  long BTC 3x entry 67500 sl 65000 tp 72000 size 100usdt
  close BTC
  close all

Autonomous campaigns run from the Telegram bot for now.`

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

func commandGoal(text string) (string, []string) {
	goal, err := campaign.ParseGoal(text)
	if err != nil {
		return err.Error() + "\n\nExample: goal profit 10 risk 50", nil
	}
	estimate, err := campaign.EstimateTrades(goal)
	if err != nil {
		return "⚠️ " + err.Error(), nil
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

	// Running-PnL curve, one point per trade — the dashboard draws it as a
	// sparkline like a bot's PnL chart.
	curve := make([]string, 0, len(result.Outcomes))
	for _, o := range result.Outcomes {
		curve = append(curve, o.RunningPnL.String())
	}
	return b.String(), curve
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
