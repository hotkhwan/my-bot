package telegram

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"bottrade/internal/decimal"
	"bottrade/internal/domain"
	"bottrade/internal/marketdata"
	"bottrade/internal/orders"
	"bottrade/internal/plans"

	tgbot "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

func TestHandlerRejectsUnauthorizedUser(t *testing.T) {
	handler := NewHandler(12345, nil, testLogger())
	sender := &fakeSender{}

	err := handler.Handle(context.Background(), sender, textUpdate(999, 111, "/start"))
	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}

	if len(sender.messages) != 0 {
		t.Fatalf("sent messages = %d, want 0", len(sender.messages))
	}
}

func TestHandlerMarketCommand(t *testing.T) {
	handler := NewHandler(12345, nil, testLogger()).WithMarketData(marketdata.MockProvider{
		FundingValue:   marketdata.Funding{MarkPrice: decimal.MustParse("68000"), LastFundingRate: decimal.MustParse("0.0001")},
		OIValue:        marketdata.OpenInterest{OpenInterest: decimal.MustParse("104625")},
		LongShortValue: marketdata.LongShortRatio{Ratio: decimal.MustParse("1.98"), LongAccount: decimal.MustParse("0.66"), ShortAccount: decimal.MustParse("0.34")},
		TakerValue:     marketdata.TakerFlow{BuySellRatio: decimal.MustParse("1.97")},
	}, "5m")
	sender := &fakeSender{}

	// "/market eth" should normalise to ETHUSDT and report the metrics.
	if err := handler.Handle(context.Background(), sender, textUpdate(12345, 111, "/market eth")); err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	got := sender.singleMessage(t)
	for _, want := range []string{"ETHUSDT", "Funding rate: 0.0001", "Open interest: 104625", "Long/Short accounts: 1.98", "Taker buy/sell: 1.97"} {
		if !strings.Contains(got.Text, want) {
			t.Fatalf("market message missing %q: %q", want, got.Text)
		}
	}
}

func TestHandlerMarketCommandWithoutProvider(t *testing.T) {
	handler := NewHandler(12345, nil, testLogger())
	sender := &fakeSender{}
	if err := handler.Handle(context.Background(), sender, textUpdate(12345, 111, "/market")); err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if got := sender.singleMessage(t); !strings.Contains(got.Text, "not configured") {
		t.Fatalf("message = %q, want not-configured notice", got.Text)
	}
}

type fakeMarketWithKlines struct {
	marketdata.MockProvider
	closes []float64
}

func (f fakeMarketWithKlines) Closes(context.Context, string, string, int) ([]float64, error) {
	return f.closes, nil
}

func TestHandlerBacktestCommand(t *testing.T) {
	closes := make([]float64, 0, 80)
	for i := 0; i < 80; i++ {
		closes = append(closes, 100+float64(i))
	}
	handler := NewHandler(12345, nil, testLogger()).WithMarketData(fakeMarketWithKlines{closes: closes}, "1h")
	sender := &fakeSender{}

	if err := handler.Handle(context.Background(), sender, textUpdate(12345, 111, "/backtest BTC")); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	got := sender.singleMessage(t)
	for _, want := range []string{"Backtest BTCUSDT", "ema_cross", "rsi_reversion", "Win rate", "Return"} {
		if !strings.Contains(got.Text, want) {
			t.Fatalf("backtest message missing %q: %q", want, got.Text)
		}
	}
}

func TestHandlerBacktestWithoutKlines(t *testing.T) {
	handler := NewHandler(12345, nil, testLogger())
	sender := &fakeSender{}
	if err := handler.Handle(context.Background(), sender, textUpdate(12345, 111, "/backtest BTC")); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if got := sender.singleMessage(t); !strings.Contains(got.Text, "not configured") {
		t.Fatalf("message = %q, want not-configured notice", got.Text)
	}
}

func TestHandlerStartCommand(t *testing.T) {
	handler := NewHandler(12345, nil, testLogger())
	sender := &fakeSender{}

	err := handler.Handle(context.Background(), sender, textUpdate(12345, 111, "/start"))
	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}

	got := sender.singleMessage(t)
	if !strings.Contains(got.Text, "Trade bot online") {
		t.Fatalf("message = %q, want start text", got.Text)
	}
}

func TestHandlerHelpCommandShowsPhaseOneGrammar(t *testing.T) {
	handler := NewHandler(12345, nil, testLogger())
	sender := &fakeSender{}

	err := handler.Handle(context.Background(), sender, textUpdate(12345, 111, "/help"))
	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}

	got := sender.singleMessage(t)
	for _, want := range []string{
		"long BTC 3x entry 67500 sl 65000 tp 72000 size 100usdt",
		"short ETH 2x entry 3300 sl 3450 tp 3000 qty 0.05",
		"close BTC 50%",
		"/status",
		"[Confirm] [Cancel]",
	} {
		if !strings.Contains(got.Text, want) {
			t.Fatalf("help text missing %q: %q", want, got.Text)
		}
	}
}

func TestHandlerStatusCommandIsReadOnly(t *testing.T) {
	tests := []string{"/status", "status", "/status@TradeBot"}

	for _, text := range tests {
		t.Run(text, func(t *testing.T) {
			handler := NewHandler(12345, nil, testLogger())
			sender := &fakeSender{}

			err := handler.Handle(context.Background(), sender, textUpdate(12345, 111, text))
			if err != nil {
				t.Fatalf("Handle returned error: %v", err)
			}

			got := sender.singleMessage(t)
			if got.Text != "No open positions." {
				t.Fatalf("message = %q, want no positions", got.Text)
			}
		})
	}
}

func TestHandlerStatusCommandShowsPositionActions(t *testing.T) {
	statusService := orders.NewStatusService(fakePositionProvider{
		positions: []domain.Position{
			{
				Symbol:           "BTCUSDT",
				Side:             domain.PositionSideLong,
				Amount:           decimal.MustParse("0.01"),
				EntryPrice:       decimal.MustParse("67500"),
				MarkPrice:        decimal.MustParse("68000"),
				UnrealizedProfit: decimal.MustParse("5"),
				Leverage:         3,
				MarginType:       "isolated",
			},
		},
	})
	handler := NewHandlerWithServices(12345, nil, 20, orders.NewService(true, 5*time.Minute, testLogger()), statusService, testLogger())
	sender := &fakeSender{}

	err := handler.Handle(context.Background(), sender, textUpdate(12345, 111, "/status"))
	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}

	got := sender.singleMessage(t)
	keyboard, ok := got.ReplyMarkup.(*models.InlineKeyboardMarkup)
	if !ok {
		t.Fatalf("ReplyMarkup = %T, want InlineKeyboardMarkup", got.ReplyMarkup)
	}
	if len(keyboard.InlineKeyboard) != 2 {
		t.Fatalf("rows = %d, want 2", len(keyboard.InlineKeyboard))
	}
	if !strings.HasPrefix(keyboard.InlineKeyboard[0][0].CallbackData, "tbact:be:BTCUSDT") {
		t.Fatalf("callback = %q, want be action", keyboard.InlineKeyboard[0][0].CallbackData)
	}
}

func TestHandlerPlanStatusUsesPlanService(t *testing.T) {
	planService := plans.NewService(fakePlanProvider{
		status: plans.Status{
			PlanID:      "2",
			IntentCount: 1,
			Symbols:     []string{"BTCUSDT"},
		},
	})
	handler := NewHandlerWithServicesAndPlans(12345, nil, 20, orders.NewService(true, 5*time.Minute, testLogger()), orders.NewStatusService(nil), planService, testLogger())
	sender := &fakeSender{}

	err := handler.Handle(context.Background(), sender, textUpdate(12345, 111, "plan 2 status"))
	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}

	got := sender.singleMessage(t)
	if !strings.Contains(got.Text, "Plan 2") || !strings.Contains(got.Text, "Recorded intents: 1") {
		t.Fatalf("message = %q, want plan status", got.Text)
	}
}

func TestHandlerPositionActionCallbackCreatesConfirmation(t *testing.T) {
	handler := NewHandler(12345, nil, testLogger())
	sender := &fakeSender{}

	err := handler.Handle(context.Background(), sender, callbackUpdate(12345, 111, 777, "cb-action", positionActionCallbackData("close50", "BTCUSDT")))
	if err != nil {
		t.Fatalf("Handle callback returned error: %v", err)
	}

	if len(sender.answers) != 1 || sender.answers[0].Text != "Review action." {
		t.Fatalf("answers = %#v, want Review action", sender.answers)
	}
	if len(sender.edits) != 1 {
		t.Fatalf("edits = %d, want 1", len(sender.edits))
	}
	if !strings.Contains(sender.edits[0].Text, "CLOSE BTCUSDT") {
		t.Fatalf("edit text = %q, want close summary", sender.edits[0].Text)
	}
	keyboard, ok := sender.edits[0].ReplyMarkup.(*models.InlineKeyboardMarkup)
	if !ok || len(keyboard.InlineKeyboard) != 1 {
		t.Fatalf("edit keyboard = %#v, want confirmation keyboard", sender.edits[0].ReplyMarkup)
	}
}

func TestHandlerUnknownCommand(t *testing.T) {
	handler := NewHandler(12345, nil, testLogger())
	sender := &fakeSender{}

	err := handler.Handle(context.Background(), sender, textUpdate(12345, 111, "hello"))
	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}

	got := sender.singleMessage(t)
	if got.Text != UnknownText {
		t.Fatalf("message = %q, want unknown text", got.Text)
	}
}

func TestHandlerOpenIntentCreatesConfirmation(t *testing.T) {
	handler := NewHandler(12345, nil, testLogger())
	sender := &fakeSender{}

	err := handler.Handle(context.Background(), sender, textUpdate(12345, 111, "long BTC 3x entry 67500 sl 65000 tp 72000 size 100usdt"))
	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}

	got := sender.singleMessage(t)
	if !strings.Contains(got.Text, "Review this action") {
		t.Fatalf("message = %q, want review text", got.Text)
	}
	if !strings.Contains(got.Text, "LONG BTCUSDT 3x") {
		t.Fatalf("message = %q, want parsed order summary", got.Text)
	}

	keyboard, ok := got.ReplyMarkup.(*models.InlineKeyboardMarkup)
	if !ok {
		t.Fatalf("ReplyMarkup = %T, want InlineKeyboardMarkup", got.ReplyMarkup)
	}
	if len(keyboard.InlineKeyboard) != 1 || len(keyboard.InlineKeyboard[0]) != 2 {
		t.Fatalf("keyboard = %#v, want confirm/cancel row", keyboard.InlineKeyboard)
	}
	if keyboard.InlineKeyboard[0][0].Text != "Confirm" || !strings.HasPrefix(keyboard.InlineKeyboard[0][0].CallbackData, "tb:confirm:") {
		t.Fatalf("confirm button = %#v, want confirm callback", keyboard.InlineKeyboard[0][0])
	}
	if keyboard.InlineKeyboard[0][1].Text != "Cancel" || !strings.HasPrefix(keyboard.InlineKeyboard[0][1].CallbackData, "tb:cancel:") {
		t.Fatalf("cancel button = %#v, want cancel callback", keyboard.InlineKeyboard[0][1])
	}
}

func TestHandlerConfirmCallbackExecutesDryRun(t *testing.T) {
	handler := NewHandler(12345, nil, testLogger())
	sender := &fakeSender{}

	err := handler.Handle(context.Background(), sender, textUpdate(12345, 111, "long BTC 3x entry 67500 sl 65000 tp 72000 size 100usdt"))
	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	message := sender.singleMessage(t)
	keyboard := message.ReplyMarkup.(*models.InlineKeyboardMarkup)
	confirmData := keyboard.InlineKeyboard[0][0].CallbackData

	err = handler.Handle(context.Background(), sender, callbackUpdate(12345, 111, 777, "cb1", confirmData))
	if err != nil {
		t.Fatalf("Handle callback returned error: %v", err)
	}

	if len(sender.answers) != 1 {
		t.Fatalf("callback answers = %d, want 1", len(sender.answers))
	}
	if sender.answers[0].Text != "Confirmed." {
		t.Fatalf("answer text = %q, want Confirmed.", sender.answers[0].Text)
	}
	if len(sender.edits) != 1 {
		t.Fatalf("edits = %d, want 1", len(sender.edits))
	}
	if !strings.Contains(sender.edits[0].Text, "DRY-RUN accepted") {
		t.Fatalf("edit text = %q, want dry-run execution", sender.edits[0].Text)
	}
	if !strings.Contains(sender.edits[0].Text, "Client order ID: tb_") {
		t.Fatalf("edit text = %q, want client order id", sender.edits[0].Text)
	}
}

func TestHandlerCancelCallbackIsIdempotent(t *testing.T) {
	handler := NewHandler(12345, nil, testLogger())
	sender := &fakeSender{}

	err := handler.Handle(context.Background(), sender, textUpdate(12345, 111, "close BTC 50%"))
	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	message := sender.singleMessage(t)
	keyboard := message.ReplyMarkup.(*models.InlineKeyboardMarkup)
	cancelData := keyboard.InlineKeyboard[0][1].CallbackData

	for i := 0; i < 2; i++ {
		err = handler.Handle(context.Background(), sender, callbackUpdate(12345, 111, 777, "cb-cancel", cancelData))
		if err != nil {
			t.Fatalf("Handle cancel callback returned error: %v", err)
		}
	}

	if len(sender.answers) != 2 {
		t.Fatalf("callback answers = %d, want 2", len(sender.answers))
	}
	if len(sender.edits) != 2 {
		t.Fatalf("edits = %d, want 2", len(sender.edits))
	}
	if !strings.Contains(sender.edits[1].Text, "No order was sent") {
		t.Fatalf("edit text = %q, want cancelled text", sender.edits[1].Text)
	}
}

func TestHandlerReturnsSendError(t *testing.T) {
	handler := NewHandler(12345, nil, testLogger())
	sender := &fakeSender{err: errors.New("send failed")}

	err := handler.Handle(context.Background(), sender, textUpdate(12345, 111, "/start"))
	if err == nil {
		t.Fatal("Handle returned nil error, want send error")
	}
	if !strings.Contains(err.Error(), "send telegram message") {
		t.Fatalf("error = %q, want wrapped send error", err.Error())
	}
}

func TestHandlerAllowsExtraAllowlistedUser(t *testing.T) {
	handler := NewHandler(12345, []int64{67890}, testLogger())
	sender := &fakeSender{}

	err := handler.Handle(context.Background(), sender, textUpdate(67890, 111, "/start"))
	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}

	got := sender.singleMessage(t)
	if !strings.Contains(got.Text, "Trade bot online") {
		t.Fatalf("message = %q, want start text", got.Text)
	}
}

type fakeSender struct {
	messages []*tgbot.SendMessageParams
	answers  []*tgbot.AnswerCallbackQueryParams
	edits    []*tgbot.EditMessageTextParams
	err      error
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func (f *fakeSender) SendMessage(ctx context.Context, params *tgbot.SendMessageParams) (*models.Message, error) {
	if f.err != nil {
		return nil, f.err
	}

	f.messages = append(f.messages, params)
	return &models.Message{}, nil
}

func (f *fakeSender) AnswerCallbackQuery(ctx context.Context, params *tgbot.AnswerCallbackQueryParams) (bool, error) {
	if f.err != nil {
		return false, f.err
	}

	f.answers = append(f.answers, params)
	return true, nil
}

func (f *fakeSender) EditMessageText(ctx context.Context, params *tgbot.EditMessageTextParams) (*models.Message, error) {
	if f.err != nil {
		return nil, f.err
	}

	f.edits = append(f.edits, params)
	return &models.Message{}, nil
}

func (f *fakeSender) singleMessage(t *testing.T) *tgbot.SendMessageParams {
	t.Helper()
	if len(f.messages) != 1 {
		t.Fatalf("sent messages = %d, want 1", len(f.messages))
	}
	return f.messages[0]
}

func textUpdate(userID int64, chatID int64, text string) *models.Update {
	return &models.Update{
		Message: &models.Message{
			From: &models.User{
				ID: userID,
			},
			Chat: models.Chat{
				ID: chatID,
			},
			Text: text,
		},
	}
}

func callbackUpdate(userID int64, chatID int64, messageID int, callbackID string, data string) *models.Update {
	return &models.Update{
		CallbackQuery: &models.CallbackQuery{
			ID: callbackID,
			From: models.User{
				ID: userID,
			},
			Message: models.MaybeInaccessibleMessage{
				Type: models.MaybeInaccessibleMessageTypeMessage,
				Message: &models.Message{
					ID: messageID,
					Chat: models.Chat{
						ID: chatID,
					},
				},
			},
			Data: data,
		},
	}
}

type fakePositionProvider struct {
	positions []domain.Position
}

func (f fakePositionProvider) Positions(ctx context.Context) ([]domain.Position, error) {
	return f.positions, nil
}

type fakePlanProvider struct {
	status plans.Status
}

func (f fakePlanProvider) PlanStatus(ctx context.Context, userID int64, planID string) (plans.Status, error) {
	return f.status, nil
}
