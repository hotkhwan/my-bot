package telegram

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"bottrade/internal/backtest"
	"bottrade/internal/campaign"
	"bottrade/internal/campaignexec"
	"bottrade/internal/decimal"
	"bottrade/internal/domain"
	"bottrade/internal/marketdata"
	"bottrade/internal/orders"
	"bottrade/internal/parser"
	"bottrade/internal/plans"

	tgbot "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

type Sender interface {
	SendMessage(ctx context.Context, params *tgbot.SendMessageParams) (*models.Message, error)
	AnswerCallbackQuery(ctx context.Context, params *tgbot.AnswerCallbackQueryParams) (bool, error)
	EditMessageText(ctx context.Context, params *tgbot.EditMessageTextParams) (*models.Message, error)
}

type Handler struct {
	adminUserID   int64
	allowedUsers  map[int64]struct{}
	parser        parser.Parser
	orderService  *orders.Service
	statusService *orders.StatusService
	planService   *plans.Service
	marketData    marketdata.Provider
	marketPeriod  string
	klines        klineSource
	campaigns     *campaignexec.Manager
	webAppURL     string
	crew          CrewAdmin
	logger        *slog.Logger
}

// CrewMember is a pending access request, for the admin /pending list.
type CrewMember struct {
	Subject     string
	Name        string
	RequestedAt time.Time
}

// CrewAdmin lets the admin list and approve crew-access requests from Telegram.
// It is backed by the same store the web "Crew approvals" panel uses.
type CrewAdmin interface {
	Pending(ctx context.Context) ([]CrewMember, error)
	Approve(ctx context.Context, subject string) error
	Revoke(ctx context.Context, subject string) error
	SetTier(ctx context.Context, subject, tier string) error
	SetRole(ctx context.Context, subject, role string) error
}

// WithCrew enables the admin-only /pending and /approve commands.
func (h *Handler) WithCrew(crew CrewAdmin) *Handler {
	h.crew = crew
	return h
}

// WithWebAppURL enables the "Open ANNY" Telegram Mini App button on /start and
// /app. The URL must be the dashboard's public https address. When unset, /app
// explains how to open the dashboard instead.
func (h *Handler) WithWebAppURL(url string) *Handler {
	h.webAppURL = strings.TrimSpace(url)
	return h
}

// WithCampaigns enables the /campaign command (autonomous testnet campaigns).
// When unset, /campaign reports that it is unavailable.
func (h *Handler) WithCampaigns(manager *campaignexec.Manager) *Handler {
	h.campaigns = manager
	return h
}

// botNotifier sends campaign progress to a chat in the background. The Sender
// (the bot) outlives any single update, so a captured reference stays valid.
type botNotifier struct {
	sender Sender
	chatID int64
}

func (n botNotifier) Notify(text string) {
	_, _ = n.sender.SendMessage(context.Background(), &tgbot.SendMessageParams{ChatID: n.chatID, Text: text})
}

// klineSource provides historical closes for the /backtest command.
type klineSource interface {
	Closes(ctx context.Context, symbol, interval string, limit int) ([]float64, error)
}

// WithMarketData attaches a market-data provider so the /market command can
// report live Binance order-flow (funding, OI, long/short, taker). If the
// provider also supplies klines, it enables /backtest too. Returns the handler
// for chaining. When unset, /market and /backtest reply that they are unavailable.
func (h *Handler) WithMarketData(provider marketdata.Provider, period string) *Handler {
	if strings.TrimSpace(period) == "" {
		period = "5m"
	}
	h.marketData = provider
	h.marketPeriod = period
	if ks, ok := provider.(klineSource); ok {
		h.klines = ks
	}
	return h
}

func NewHandler(adminUserID int64, allowedUserIDs []int64, logger *slog.Logger) *Handler {
	return NewHandlerWithServices(
		adminUserID,
		allowedUserIDs,
		20,
		orders.NewService(true, 5*time.Minute, logger),
		orders.NewStatusService(nil),
		logger,
	)
}

func NewHandlerWithServices(adminUserID int64, allowedUserIDs []int64, maxLeverage int, orderService *orders.Service, statusService *orders.StatusService, logger *slog.Logger) *Handler {
	return NewHandlerWithServicesAndPlans(adminUserID, allowedUserIDs, maxLeverage, orderService, statusService, plans.NewService(nil), logger)
}

func NewHandlerWithServicesAndPlans(adminUserID int64, allowedUserIDs []int64, maxLeverage int, orderService *orders.Service, statusService *orders.StatusService, planService *plans.Service, logger *slog.Logger) *Handler {
	if logger == nil {
		logger = slog.Default()
	}
	if orderService == nil {
		orderService = orders.NewService(true, 5*time.Minute, logger)
	}
	if statusService == nil {
		statusService = orders.NewStatusService(nil)
	}
	if planService == nil {
		planService = plans.NewService(nil)
	}

	allowedUsers := make(map[int64]struct{}, len(allowedUserIDs))
	for _, id := range allowedUserIDs {
		allowedUsers[id] = struct{}{}
	}

	return &Handler{
		adminUserID:   adminUserID,
		allowedUsers:  allowedUsers,
		parser:        parser.New(parser.Options{MaxLeverage: maxLeverage}),
		orderService:  orderService,
		statusService: statusService,
		planService:   planService,
		logger:        logger,
	}
}

func (h *Handler) Handle(ctx context.Context, sender Sender, update *models.Update) error {
	if update != nil && update.CallbackQuery != nil {
		return h.handleCallback(ctx, sender, update.CallbackQuery)
	}

	if update == nil || update.Message == nil {
		return nil
	}

	message := update.Message
	if message.From == nil {
		h.logger.Warn("telegram message without sender", "chat_id", message.Chat.ID)
		return nil
	}

	userID := message.From.ID
	text := strings.TrimSpace(message.Text)
	if text == "" {
		return nil
	}
	command := commandName(text)

	// New / non-allowlisted users can still onboard: /start, /app and /help send
	// them to the web app, where they register and request crew access for the
	// admin to approve. Every other command stays gated.
	if !h.isAllowed(userID) {
		if command != "/start" && command != "/app" && command != "/help" {
			h.logger.Info("telegram non-member directed to onboarding", "user_id", userID, "chat_id", message.Chat.ID)
			if h.webAppURL != "" {
				return h.sendWithKeyboard(ctx, sender, message.Chat.ID, OnboardText, webAppKeyboard(h.webAppURL))
			}
			return h.sendText(ctx, sender, message.Chat.ID, OnboardText)
		}
	}

	switch command {
	case "/start", "/app":
		if h.webAppURL != "" {
			return h.sendWithKeyboard(ctx, sender, message.Chat.ID, StartText, webAppKeyboard(h.webAppURL))
		}
		return h.sendText(ctx, sender, message.Chat.ID, StartText)
	case "/help":
		return h.sendText(ctx, sender, message.Chat.ID, HelpText)
	case "/status":
		return h.sendStatus(ctx, sender, message.Chat.ID)
	case "/market":
		return h.sendMarket(ctx, sender, message.Chat.ID, commandArg(text))
	case "/backtest":
		return h.sendBacktest(ctx, sender, message.Chat.ID, commandArg(text))
	case "/goal":
		return h.sendGoal(ctx, sender, message.Chat.ID, text)
	case "/campaign":
		return h.handleCampaign(sender, message.Chat.ID, userID, text)
	case "/pending":
		return h.handlePending(ctx, sender, message.Chat.ID, userID)
	case "/approve":
		return h.handleApprove(ctx, sender, message.Chat.ID, userID, commandRest(text))
	case "/unapprove", "/revoke", "/reject":
		return h.handleRevoke(ctx, sender, message.Chat.ID, userID, commandArg(text))
	case "/tier":
		return h.handleTier(ctx, sender, message.Chat.ID, userID, commandRest(text))
	case "/makeadmin":
		return h.handleMakeAdmin(ctx, sender, message.Chat.ID, userID, commandArg(text), true)
	case "/removeadmin":
		return h.handleMakeAdmin(ctx, sender, message.Chat.ID, userID, commandArg(text), false)
	}

	intent, err := h.parser.Parse(text)
	if err != nil {
		return h.sendText(ctx, sender, message.Chat.ID, err.Error())
	}

	switch intent.Type {
	case "status":
		return h.sendStatus(ctx, sender, message.Chat.ID)
	case "plan_status":
		return h.sendText(ctx, sender, message.Chat.ID, h.planService.Text(ctx, userID, intent.PlanStatus.PlanID))
	}

	confirmation, err := h.orderService.Prepare(ctx, userID, intent)
	if err != nil {
		return err
	}

	reply := "Review this action:\n\n" + orders.Summary(intent) + "\n\nPress Confirm within " + h.orderService.TTL().String() + "."
	return h.sendWithKeyboard(ctx, sender, message.Chat.ID, reply, confirmationKeyboard(confirmation.ID))
}

func (h *Handler) BotHandler(ctx context.Context, b *tgbot.Bot, update *models.Update) {
	if err := h.Handle(ctx, b, update); err != nil {
		h.logger.Error("telegram handler failed", "error", err)
	}
}

func (h *Handler) isAllowed(userID int64) bool {
	if h.adminUserID != 0 && userID == h.adminUserID {
		return true
	}

	_, ok := h.allowedUsers[userID]
	return ok
}

func (h *Handler) sendText(ctx context.Context, sender Sender, chatID int64, text string) error {
	return h.sendWithKeyboard(ctx, sender, chatID, text, nil)
}

func (h *Handler) sendStatus(ctx context.Context, sender Sender, chatID int64) error {
	snapshot := h.statusService.Snapshot(ctx)
	var keyboard models.ReplyMarkup
	if snapshot.Err == nil && len(snapshot.Positions) > 0 {
		keyboard = positionActionKeyboard(snapshot.Positions)
	}
	return h.sendWithKeyboard(ctx, sender, chatID, snapshot.Text, keyboard)
}

func (h *Handler) sendWithKeyboard(ctx context.Context, sender Sender, chatID int64, text string, keyboard models.ReplyMarkup) error {
	if sender == nil {
		return fmt.Errorf("telegram sender is required")
	}

	_, err := sender.SendMessage(ctx, &tgbot.SendMessageParams{
		ChatID:      chatID,
		Text:        text,
		ReplyMarkup: keyboard,
	})
	if err != nil {
		return fmt.Errorf("send telegram message: %w", err)
	}

	return nil
}

func (h *Handler) handleCallback(ctx context.Context, sender Sender, callback *models.CallbackQuery) error {
	userID := callback.From.ID
	if !h.isAllowed(userID) {
		h.logger.Warn("telegram callback rejected by allowlist", "user_id", userID)
		return h.answerCallback(ctx, sender, callback.ID, "Unauthorized.")
	}

	action, confirmationID, ok := parseConfirmationCallback(callback.Data)
	if !ok {
		positionAction, symbol, ok := parsePositionActionCallback(callback.Data)
		if !ok {
			return h.answerCallback(ctx, sender, callback.ID, "Unknown action.")
		}
		return h.handlePositionAction(ctx, sender, callback, positionAction, symbol)
	}

	switch action {
	case "confirm":
		result, err := h.orderService.Confirm(ctx, userID, confirmationID)
		if err != nil {
			return h.finishCallback(ctx, sender, callback, "Cannot confirm: "+callbackErrorText(err), "")
		}
		return h.finishCallback(ctx, sender, callback, "Confirmed.", result.Message+"\n\nClient order ID: "+result.ClientOrderID)
	case "cancel":
		if err := h.orderService.Cancel(ctx, userID, confirmationID); err != nil {
			return h.finishCallback(ctx, sender, callback, "Cannot cancel: "+callbackErrorText(err), "")
		}
		return h.finishCallback(ctx, sender, callback, "Cancelled.", "Cancelled.\n\nNo order was sent.")
	default:
		return h.answerCallback(ctx, sender, callback.ID, "Unknown action.")
	}
}

func (h *Handler) handlePositionAction(ctx context.Context, sender Sender, callback *models.CallbackQuery, action string, symbol string) error {
	intent, err := positionActionIntent(action, symbol)
	if err != nil {
		return h.answerCallback(ctx, sender, callback.ID, err.Error())
	}

	confirmation, err := h.orderService.Prepare(ctx, callback.From.ID, intent)
	if err != nil {
		return h.finishCallback(ctx, sender, callback, "Cannot prepare: "+callbackErrorText(err), "")
	}

	text := "Review this action:\n\n" + orders.Summary(intent) + "\n\nPress Confirm within " + h.orderService.TTL().String() + "."
	return h.finishCallback(ctx, sender, callback, "Review action.", text, confirmationKeyboard(confirmation.ID))
}

func commandName(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}

	first, _, _ := strings.Cut(text, " ")
	first = strings.ToLower(strings.TrimSpace(first))

	if strings.HasPrefix(first, "/") {
		command, _, _ := strings.Cut(first, "@")
		return command
	}

	return first
}

// commandArg returns the first argument after the command word, or "".
func commandArg(text string) string {
	_, rest, found := strings.Cut(strings.TrimSpace(text), " ")
	if !found {
		return ""
	}
	first, _, _ := strings.Cut(strings.TrimSpace(rest), " ")
	return strings.TrimSpace(first)
}

// commandRest returns everything after the command word, or "".
func commandRest(text string) string {
	_, rest, found := strings.Cut(strings.TrimSpace(text), " ")
	if !found {
		return ""
	}
	return strings.TrimSpace(rest)
}

// marketSymbol normalises a /market argument into a Binance symbol, defaulting
// to BTCUSDT and appending USDT when only the base asset is given.
func marketSymbol(arg string) string {
	value := strings.ToUpper(strings.TrimSpace(arg))
	value = strings.ReplaceAll(value, "/", "")
	if value == "" {
		return "BTCUSDT"
	}
	if !strings.HasSuffix(value, "USDT") {
		value += "USDT"
	}
	return value
}

func (h *Handler) sendMarket(ctx context.Context, sender Sender, chatID int64, arg string) error {
	if h.marketData == nil {
		return h.sendText(ctx, sender, chatID, "Market data is not configured.")
	}
	symbol := marketSymbol(arg)
	snapshot, err := marketdata.Collect(ctx, h.marketData, symbol, h.marketPeriod, time.Now().UTC())
	if err != nil && snapshot.Funding.MarkPrice.IsZero() && snapshot.OpenInterest.OpenInterest.IsZero() &&
		snapshot.LongShort.Ratio.IsZero() && snapshot.Taker.BuySellRatio.IsZero() {
		h.logger.Warn("market data fetch failed", "symbol", symbol, "error", err)
		return h.sendText(ctx, sender, chatID, "Could not fetch market data for "+symbol+".")
	}
	return h.sendText(ctx, sender, chatID, formatMarketSnapshot(snapshot))
}

const goalUsage = "Set a profit goal and preview the plan (simulation — no real orders):\n" +
	"/goal profit 10 capital 100\n" +
	"Optional: winrate 55 reward 2 risk 1 maxtrades 50 drawdown 30"

const campaignUsage = "Autonomous campaign (testnet):\n" +
	"/campaign start profit 10 capital 100 symbol BTC\n" +
	"/campaign stop"

// handleCampaign starts or stops an autonomous campaign. Starting is heavily
// gated by the manager (testnet only, real trading off, explicit opt-in); this
// handler just parses the request and reports refusals.
func (h *Handler) handleCampaign(sender Sender, chatID, userID int64, text string) error {
	bg := context.Background()
	if h.campaigns == nil {
		return h.sendText(bg, sender, chatID, "Autonomous campaigns are not enabled on this deployment.")
	}

	switch strings.ToLower(commandArg(text)) {
	case "stop":
		if h.campaigns.Stop(userID) {
			return h.sendText(bg, sender, chatID, "⏹ Stopping your campaign after the current trade resolves…")
		}
		return h.sendText(bg, sender, chatID, "No campaign is running.")
	case "start":
		goal, err := campaign.ParseGoal(text)
		if err != nil {
			return h.sendText(bg, sender, chatID, err.Error()+"\n\n"+campaignUsage)
		}
		if _, err := campaign.EstimateTrades(goal); err != nil {
			return h.sendText(bg, sender, chatID, "⚠️ "+err.Error())
		}
		symbol := marketSymbol(symbolArg(text))
		if err := h.campaigns.Start(userID, symbol, goal, botNotifier{sender: sender, chatID: chatID}); err != nil {
			return h.sendText(bg, sender, chatID, "⚠️ "+err.Error())
		}
		return nil // the manager sends the "started" message
	default:
		return h.sendText(bg, sender, chatID, campaignUsage)
	}
}

// symbolArg extracts the value after a "symbol" token, or "".
func symbolArg(text string) string {
	tokens := strings.Fields(text)
	for i, tok := range tokens {
		if strings.EqualFold(tok, "symbol") && i+1 < len(tokens) {
			return tokens[i+1]
		}
	}
	return ""
}

func (h *Handler) sendGoal(ctx context.Context, sender Sender, chatID int64, text string) error {
	goal, err := campaign.ParseGoal(text)
	if err != nil {
		return h.sendText(ctx, sender, chatID, err.Error()+"\n\n"+goalUsage)
	}

	// Feasibility: a goal with no positive expectancy can never reach its target.
	estimate, err := campaign.EstimateTrades(goal)
	if err != nil {
		return h.sendText(ctx, sender, chatID, "⚠️ "+err.Error())
	}

	result := campaign.Simulate(goal)
	return h.sendText(ctx, sender, chatID, formatGoal(goal, estimate, result))
}

// parseGoal reads a goal from "/goal profit 10 capital 100 ..." with sensible
// defaults derived from capital. Target profit is required.
func formatGoal(goal campaign.Goal, estimate int, result campaign.SimulationResult) string {
	wins := 0
	for _, o := range result.Outcomes {
		if o.Win {
			wins++
		}
	}
	verdictText := map[campaign.Verdict]string{
		campaign.StopTargetReached: "🎯 target reached",
		campaign.StopMaxDrawdown:   "🛑 stopped: max drawdown",
		campaign.StopMaxTrades:     "⏹ stopped: max trades",
	}[result.Verdict]
	if verdictText == "" {
		verdictText = string(result.Verdict)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "🎯 Goal: make %s USDT from %s USDT capital\n", goal.TargetProfitUSDT.String(), goal.CapitalUSDT.String())
	fmt.Fprintf(&b, "Risk: up to %d%% of capital (stop at -%s USDT), assumed win-rate %d%%\n",
		goal.RiskPercent(), goal.MaxDrawdownUSDT.String(), goal.AssumedWinRate)
	fmt.Fprintf(&b, "Expectancy: %s USDT/trade → ~%d trades needed\n\n", goal.ExpectedPerTrade().String(), estimate)
	fmt.Fprintf(&b, "📊 Simulation (no real orders): %s\n", verdictText)
	fmt.Fprintf(&b, "Trades: %d (%d win / %d loss) · Final PnL: %s USDT\n",
		result.State.TradesClosed, wins, result.State.TradesClosed-wins, result.State.RealizedPnL.String())
	b.WriteString("\n⚠️ This is a planning preview. Autonomous live execution is gated until testnet-validated; for now place trades yourself with [Confirm].")
	return b.String()
}

func (h *Handler) sendBacktest(ctx context.Context, sender Sender, chatID int64, arg string) error {
	if h.klines == nil {
		return h.sendText(ctx, sender, chatID, "Backtesting is not configured.")
	}
	symbol := marketSymbol(arg)
	closes, err := h.klines.Closes(ctx, symbol, "1h", 500)
	if err != nil {
		h.logger.Warn("backtest klines fetch failed", "symbol", symbol, "error", err)
		return h.sendText(ctx, sender, chatID, "Could not fetch history for "+symbol+".")
	}

	strategies := []backtest.Strategy{
		backtest.EMACrossStrategy{Fast: 12, Slow: 26},
		backtest.RSIReversionStrategy{Period: 14, Low: 30, High: 70},
	}
	cfg := backtest.Config{FeeRate: 0.0004}

	var b strings.Builder
	fmt.Fprintf(&b, "🧪 Backtest %s (1h, %d bars, fee 0.04%%/side)", symbol, len(closes))
	for _, strategy := range strategies {
		result, err := backtest.Run(closes, strategy, cfg)
		if err != nil {
			fmt.Fprintf(&b, "\n\n%s: %v", strategy.Name(), err)
			continue
		}
		fmt.Fprintf(&b, "\n\n%s\nTrades: %d | Win rate: %.0f%%\nReturn: %.2f%% | Max DD: %.2f%%",
			result.Strategy, result.Trades, result.WinRatePct, result.ReturnPct, result.MaxDrawdownPct)
	}
	b.WriteString("\n\nPast performance is not indicative of future results.")
	return h.sendText(ctx, sender, chatID, b.String())
}

func formatMarketSnapshot(s marketdata.Snapshot) string {
	var b strings.Builder
	fmt.Fprintf(&b, "📊 %s market data (%s)", s.Symbol, s.Period)
	if s.Funding.MarkPrice.IsPositive() {
		fmt.Fprintf(&b, "\nMark price: %s", s.Funding.MarkPrice.String())
		fmt.Fprintf(&b, "\nFunding rate: %s (per 8h)", s.Funding.LastFundingRate.String())
	}
	if s.OpenInterest.OpenInterest.IsPositive() {
		fmt.Fprintf(&b, "\nOpen interest: %s", s.OpenInterest.OpenInterest.String())
	}
	if s.LongShort.Ratio.IsPositive() {
		fmt.Fprintf(&b, "\nLong/Short accounts: %s (long %s / short %s)",
			s.LongShort.Ratio.String(), s.LongShort.LongAccount.String(), s.LongShort.ShortAccount.String())
	}
	if s.Taker.BuySellRatio.IsPositive() {
		fmt.Fprintf(&b, "\nTaker buy/sell: %s", s.Taker.BuySellRatio.String())
	}
	return b.String()
}

// webAppKeyboard builds an inline button that launches the dashboard as a
// Telegram Mini App. Tapping it opens the web app inside Telegram, where the
// initData auto-login signs the user into their own account.
func webAppKeyboard(url string) models.ReplyMarkup {
	return &models.InlineKeyboardMarkup{
		InlineKeyboard: [][]models.InlineKeyboardButton{{
			{Text: "🤖 Open ANNY", WebApp: &models.WebAppInfo{URL: url}},
		}},
	}
}

// handlePending lists pending crew-access requests (admin only).
func (h *Handler) handlePending(ctx context.Context, sender Sender, chatID, userID int64) error {
	if h.adminUserID == 0 || userID != h.adminUserID {
		return h.sendText(ctx, sender, chatID, "Admin only.")
	}
	if h.crew == nil {
		return h.sendText(ctx, sender, chatID, "Crew approvals are not enabled.")
	}
	members, err := h.crew.Pending(ctx)
	if err != nil {
		return h.sendText(ctx, sender, chatID, "Could not load requests.")
	}
	if len(members) == 0 {
		return h.sendText(ctx, sender, chatID, "No pending crew requests. 🎉")
	}
	var b strings.Builder
	b.WriteString("🛡 Pending crew requests:\n")
	for _, m := range members {
		id := strings.TrimPrefix(m.Subject, "tg:")
		when := ""
		if !m.RequestedAt.IsZero() {
			when = " · " + humanizeSince(time.Since(m.RequestedAt))
		}
		fmt.Fprintf(&b, "\n• %s (tg:%s)%s\n   /approve %s · /approve %s captain · /reject %s", m.Name, id, when, id, id, id)
	}
	return h.sendText(ctx, sender, chatID, b.String())
}

// humanizeSince renders a duration as "just now / 5m ago / 2h ago / 3d ago".
func humanizeSince(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return strconv.Itoa(int(d.Minutes())) + "m ago"
	case d < 24*time.Hour:
		return strconv.Itoa(int(d.Hours())) + "h ago"
	default:
		return strconv.Itoa(int(d.Hours()/24)) + "d ago"
	}
}

// handleApprove approves a user by Telegram id (admin only): /approve 12345.
func (h *Handler) handleApprove(ctx context.Context, sender Sender, chatID, userID int64, arg string) error {
	if h.adminUserID == 0 || userID != h.adminUserID {
		return h.sendText(ctx, sender, chatID, "Admin only.")
	}
	if h.crew == nil {
		return h.sendText(ctx, sender, chatID, "Crew approvals are not enabled.")
	}
	fields := strings.Fields(arg)
	if len(fields) == 0 {
		return h.sendText(ctx, sender, chatID, "Usage: /approve <telegram id> [free|captain|commander]  (see /pending)")
	}
	id := strings.TrimSpace(strings.TrimPrefix(fields[0], "tg:"))
	if id == "" {
		return h.sendText(ctx, sender, chatID, "Usage: /approve <telegram id> [free|captain|commander]")
	}
	if err := h.crew.Approve(ctx, "tg:"+id); err != nil {
		return h.sendText(ctx, sender, chatID, "Could not approve.")
	}
	// Optional plan: /approve 102 captain
	if len(fields) > 1 {
		plan := strings.ToLower(fields[1])
		switch plan {
		case "free", "captain", "commander":
			if err := h.crew.SetTier(ctx, "tg:"+id, plan); err != nil {
				return h.sendText(ctx, sender, chatID, "Approved, but could not set the plan.")
			}
			return h.sendText(ctx, sender, chatID, "✅ Approved tg:"+id+" on the "+plan+" plan.")
		default:
			return h.sendText(ctx, sender, chatID, "✅ Approved tg:"+id+" (plan must be free|captain|commander; ignored).")
		}
	}
	return h.sendText(ctx, sender, chatID, "✅ Approved tg:"+id+" — they now have full access.")
}

// handleRevoke revokes a user's access (admin only): /unapprove 12345.
func (h *Handler) handleRevoke(ctx context.Context, sender Sender, chatID, userID int64, arg string) error {
	if h.adminUserID == 0 || userID != h.adminUserID {
		return h.sendText(ctx, sender, chatID, "Admin only.")
	}
	if h.crew == nil {
		return h.sendText(ctx, sender, chatID, "Crew approvals are not enabled.")
	}
	id := strings.TrimSpace(strings.TrimPrefix(arg, "tg:"))
	if id == "" {
		return h.sendText(ctx, sender, chatID, "Usage: /unapprove <telegram id>")
	}
	if h.adminUserID != 0 && id == strconv.FormatInt(h.adminUserID, 10) {
		return h.sendText(ctx, sender, chatID, "You can't revoke yourself.")
	}
	if err := h.crew.Revoke(ctx, "tg:"+id); err != nil {
		return h.sendText(ctx, sender, chatID, "Could not revoke.")
	}
	return h.sendText(ctx, sender, chatID, "🚫 Revoked tg:"+id+" — access removed until re-approved.")
}

// handleTier sets a user's plan (admin only): /tier 12345 captain.
func (h *Handler) handleTier(ctx context.Context, sender Sender, chatID, userID int64, arg string) error {
	if h.adminUserID == 0 || userID != h.adminUserID {
		return h.sendText(ctx, sender, chatID, "Admin only.")
	}
	if h.crew == nil {
		return h.sendText(ctx, sender, chatID, "Crew approvals are not enabled.")
	}
	fields := strings.Fields(arg)
	if len(fields) != 2 {
		return h.sendText(ctx, sender, chatID, "Usage: /tier <telegram id> <free|captain|commander>")
	}
	id := strings.TrimSpace(strings.TrimPrefix(fields[0], "tg:"))
	tier := strings.ToLower(strings.TrimSpace(fields[1]))
	switch tier {
	case "free", "captain", "commander":
	default:
		return h.sendText(ctx, sender, chatID, "Tier must be free, captain, or commander.")
	}
	if id == "" {
		return h.sendText(ctx, sender, chatID, "Usage: /tier <telegram id> <free|captain|commander>")
	}
	if err := h.crew.SetTier(ctx, "tg:"+id, tier); err != nil {
		return h.sendText(ctx, sender, chatID, "Could not set tier.")
	}
	return h.sendText(ctx, sender, chatID, "✅ tg:"+id+" is now on the "+tier+" plan.")
}

// handleMakeAdmin promotes (promote=true) or demotes a member to/from admin.
func (h *Handler) handleMakeAdmin(ctx context.Context, sender Sender, chatID, userID int64, arg string, promote bool) error {
	if h.adminUserID == 0 || userID != h.adminUserID {
		return h.sendText(ctx, sender, chatID, "Admin only.")
	}
	if h.crew == nil {
		return h.sendText(ctx, sender, chatID, "Crew approvals are not enabled.")
	}
	id := strings.TrimSpace(strings.TrimPrefix(arg, "tg:"))
	if id == "" {
		return h.sendText(ctx, sender, chatID, "Usage: /makeadmin <telegram id>")
	}
	role := "admin"
	if !promote {
		role = ""
	}
	if err := h.crew.SetRole(ctx, "tg:"+id, role); err != nil {
		return h.sendText(ctx, sender, chatID, "Could not change the role.")
	}
	if promote {
		return h.sendText(ctx, sender, chatID, "👑 tg:"+id+" is now an admin — they can approve crew and manage tiers.")
	}
	return h.sendText(ctx, sender, chatID, "tg:"+id+" is no longer an admin.")
}

func confirmationKeyboard(id string) models.ReplyMarkup {
	return &models.InlineKeyboardMarkup{
		InlineKeyboard: [][]models.InlineKeyboardButton{
			{
				{Text: "Confirm", CallbackData: confirmationCallbackData("confirm", id)},
				{Text: "Cancel", CallbackData: confirmationCallbackData("cancel", id)},
			},
		},
	}
}

func positionActionKeyboard(positions []domain.Position) models.ReplyMarkup {
	rows := make([][]models.InlineKeyboardButton, 0, len(positions)*2)
	for _, position := range positions {
		symbol := position.Symbol
		rows = append(rows, []models.InlineKeyboardButton{
			{Text: "BE " + symbol, CallbackData: positionActionCallbackData("be", symbol)},
			{Text: "Trail 0.5%", CallbackData: positionActionCallbackData("trail05", symbol)},
		})
		rows = append(rows, []models.InlineKeyboardButton{
			{Text: "Close 50%", CallbackData: positionActionCallbackData("close50", symbol)},
			{Text: "Close", CallbackData: positionActionCallbackData("close100", symbol)},
		})
	}
	return &models.InlineKeyboardMarkup{InlineKeyboard: rows}
}

func positionActionCallbackData(action string, symbol string) string {
	return "tbact:" + action + ":" + symbol
}

func confirmationCallbackData(action string, id string) string {
	return "tb:" + action + ":" + id
}

func parseConfirmationCallback(data string) (string, string, bool) {
	value, ok := strings.CutPrefix(data, "tb:")
	if !ok {
		return "", "", false
	}

	action, id, ok := strings.Cut(value, ":")
	if !ok || id == "" {
		return "", "", false
	}
	if action != "confirm" && action != "cancel" {
		return "", "", false
	}

	return action, id, true
}

func parsePositionActionCallback(data string) (string, string, bool) {
	value, ok := strings.CutPrefix(data, "tbact:")
	if !ok {
		return "", "", false
	}

	action, symbol, ok := strings.Cut(value, ":")
	if !ok || symbol == "" {
		return "", "", false
	}
	switch action {
	case "be", "trail05", "close50", "close100":
		return action, symbol, true
	default:
		return "", "", false
	}
}

func positionActionIntent(action string, symbol string) (domain.Intent, error) {
	switch action {
	case "be":
		return domain.Intent{
			Type: domain.IntentBreakeven,
			Breakeven: &domain.BreakevenIntent{
				Symbol: symbol,
			},
		}, nil
	case "trail05":
		return domain.Intent{
			Type: domain.IntentTrail,
			Trail: &domain.TrailIntent{
				Symbol:       symbol,
				CallbackRate: decimal.MustParse("0.5"),
			},
		}, nil
	case "close50":
		return domain.Intent{
			Type: domain.IntentClose,
			Close: &domain.CloseIntent{
				Symbol:          symbol,
				Percent:         decimal.MustParse("50"),
				HasPercent:      true,
				ResolvedPercent: decimal.MustParse("50"),
			},
		}, nil
	case "close100":
		return domain.Intent{
			Type: domain.IntentClose,
			Close: &domain.CloseIntent{
				Symbol:          symbol,
				ResolvedPercent: decimal.NewFromInt(100),
			},
		}, nil
	default:
		return domain.Intent{}, fmt.Errorf("unknown position action")
	}
}

func (h *Handler) finishCallback(ctx context.Context, sender Sender, callback *models.CallbackQuery, answer string, editText string, keyboard ...models.ReplyMarkup) error {
	if err := h.answerCallback(ctx, sender, callback.ID, answer); err != nil {
		return err
	}
	if editText == "" || callback.Message.Message == nil {
		return nil
	}

	var replyMarkup models.ReplyMarkup
	if len(keyboard) > 0 {
		replyMarkup = keyboard[0]
	}

	_, err := sender.EditMessageText(ctx, &tgbot.EditMessageTextParams{
		ChatID:      callback.Message.Message.Chat.ID,
		MessageID:   callback.Message.Message.ID,
		Text:        editText,
		ReplyMarkup: replyMarkup,
	})
	if err != nil {
		return fmt.Errorf("edit telegram message: %w", err)
	}

	return nil
}

func (h *Handler) answerCallback(ctx context.Context, sender Sender, callbackID string, text string) error {
	if sender == nil {
		return fmt.Errorf("telegram sender is required")
	}

	_, err := sender.AnswerCallbackQuery(ctx, &tgbot.AnswerCallbackQueryParams{
		CallbackQueryID: callbackID,
		Text:            text,
	})
	if err != nil {
		return fmt.Errorf("answer telegram callback: %w", err)
	}

	return nil
}

func callbackErrorText(err error) string {
	switch err {
	case orders.ErrConfirmationNotFound:
		return "confirmation not found."
	case orders.ErrConfirmationForbidden:
		return "this confirmation belongs to another user."
	case orders.ErrConfirmationExpired:
		return "confirmation expired. Send the command again."
	case orders.ErrConfirmationCancelled:
		return "already cancelled."
	case orders.ErrConfirmationExecuted:
		return "already executed."
	case orders.ErrConfirmationExecuting:
		return "already executing."
	case orders.ErrConfirmationFailed:
		return "previous execution failed. Send the command again."
	default:
		return err.Error()
	}
}
