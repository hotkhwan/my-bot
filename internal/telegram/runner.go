package telegram

import (
	"context"
	"fmt"
	"log/slog"

	"bottrade/internal/campaignexec"
	"bottrade/internal/config"
	"bottrade/internal/decimal"
	"bottrade/internal/marketdata"
	"bottrade/internal/orders"
	"bottrade/internal/plans"
	"bottrade/internal/realtime"

	tgbot "github.com/go-telegram/bot"
)

type PollingRunner struct {
	bot     *tgbot.Bot
	handler *Handler
	logger  *slog.Logger
}

// SetCampaignManager enables the /campaign command on the runner's handler.
// SetCrew enables the admin /pending and /approve commands.
func (r *PollingRunner) SetCrew(crew CrewAdmin) {
	if r.handler != nil {
		r.handler.WithCrew(crew)
	}
}

func (r *PollingRunner) SetCampaignManager(manager *campaignexec.Manager) {
	if r.handler != nil {
		r.handler.WithCampaigns(manager)
	}
}

func NewPollingRunner(cfg config.Config, orderService *orders.Service, statusService *orders.StatusService, planService *plans.Service, logger *slog.Logger) (*PollingRunner, error) {
	if logger == nil {
		logger = slog.Default()
	}
	if orderService == nil {
		orderService = orders.NewService(cfg.App.DryRun, cfg.App.ConfirmationTTL, logger)
	}
	if statusService == nil {
		statusService = orders.NewStatusService(nil)
	}
	if planService == nil {
		planService = plans.NewService(nil)
	}
	handler := NewHandlerWithServicesAndPlans(cfg.Telegram.AdminUserID, cfg.Telegram.AllowedUserIDs, cfg.App.MaxLeverage, orderService, statusService, planService, logger)
	handler.WithWebAppURL(cfg.Telegram.WebAppURL)
	if cfg.AI.MarketDataEnabled {
		handler.WithMarketData(marketdata.NewBinanceProvider(cfg.AI.MarketDataBaseURL, nil), cfg.AI.MarketDataPeriod)
	}
	b, err := tgbot.New(
		cfg.Telegram.BotToken,
		tgbot.WithDefaultHandler(handler.BotHandler),
		tgbot.WithAllowedUpdates(tgbot.AllowedUpdates{"message", "callback_query"}),
		tgbot.WithErrorsHandler(func(err error) {
			logger.Error("telegram polling error", "error", err)
		}),
		tgbot.WithSkipGetMe(),
	)
	if err != nil {
		return nil, fmt.Errorf("create telegram bot: %w", err)
	}

	return &PollingRunner{
		bot:     b,
		handler: handler,
		logger:  logger,
	}, nil
}

func (r *PollingRunner) Run(ctx context.Context) error {
	r.logger.Info("telegram polling started")
	r.bot.Start(ctx)
	r.logger.Info("telegram polling stopped")
	return nil
}

// RealtimeStream is the slice of the realtime broadcaster the push subscriber
// needs. *realtime.Broadcaster satisfies it.
type RealtimeStream interface {
	Subscribe() (<-chan realtime.Event, func())
}

// isNilStream reports whether stream is nil — including a typed-nil
// *realtime.Broadcaster, which is a non-nil interface wrapping a nil pointer and
// would otherwise pass a plain `== nil` check and panic on first use.
func isNilStream(stream RealtimeStream) bool {
	if stream == nil {
		return true
	}
	if b, ok := stream.(*realtime.Broadcaster); ok {
		return b == nil
	}
	return false
}

// StartRealtime pushes realtime trade-closed alerts to the admin chat in the
// background. Only closes are pushed (not every price tick) so the chat is not
// flooded; the web SSE stream carries the high-frequency updates. A nil stream
// (including a typed-nil *Broadcaster) or zero chat id is a no-op.
func (r *PollingRunner) StartRealtime(ctx context.Context, stream RealtimeStream, chatID int64) {
	if chatID == 0 || isNilStream(stream) {
		return
	}
	events, cancel := stream.Subscribe()
	go func() {
		defer cancel()
		for {
			select {
			case <-ctx.Done():
				return
			case event, ok := <-events:
				if !ok {
					return
				}
				text, push := formatRealtimeAlert(event)
				if !push {
					continue
				}
				if _, err := r.bot.SendMessage(ctx, &tgbot.SendMessageParams{ChatID: chatID, Text: text}); err != nil {
					r.logger.Warn("realtime telegram push failed", "error", err)
				}
			}
		}
	}()
}

// formatRealtimeAlert renders the Telegram message for an event, and reports
// whether it should be pushed at all. Only trade closes are pushed.
func formatRealtimeAlert(event realtime.Event) (string, bool) {
	if event.Type != realtime.EventTradeClosed {
		return "", false
	}
	emoji := "⚪"
	switch {
	case event.RealizedPnL.IsPositive():
		emoji = "🟢"
	case event.RealizedPnL.Cmp(decimal.Zero()) < 0:
		emoji = "🔴"
	}
	return fmt.Sprintf("%s %s position closed — realized PnL %s USDT", emoji, event.Symbol, event.RealizedPnL.String()), true
}
