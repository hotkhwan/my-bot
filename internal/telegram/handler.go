package telegram

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"bottrade/internal/backtest"
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
	logger        *slog.Logger
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
	if !h.isAllowed(userID) {
		h.logger.Warn("telegram message rejected by allowlist", "user_id", userID, "chat_id", message.Chat.ID)
		return nil
	}

	text := strings.TrimSpace(message.Text)
	if text == "" {
		return nil
	}

	command := commandName(text)
	switch command {
	case "/start":
		return h.sendText(ctx, sender, message.Chat.ID, StartText)
	case "/help":
		return h.sendText(ctx, sender, message.Chat.ID, HelpText)
	case "/status":
		return h.sendStatus(ctx, sender, message.Chat.ID)
	case "/market":
		return h.sendMarket(ctx, sender, message.Chat.ID, commandArg(text))
	case "/backtest":
		return h.sendBacktest(ctx, sender, message.Chat.ID, commandArg(text))
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
