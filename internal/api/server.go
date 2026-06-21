package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"bottrade/internal/config"
	"bottrade/internal/dashboard"
	"bottrade/internal/signals"

	"github.com/gofiber/fiber/v3"
)

type Server struct {
	cfg       config.Config
	processor *signals.Processor
	logger    *slog.Logger
	app       *fiber.App
}

func NewServer(cfg config.Config, processor *signals.Processor, logger *slog.Logger) *Server {
	if logger == nil {
		logger = slog.Default()
	}

	server := &Server{
		cfg:       cfg,
		processor: processor,
		logger:    logger,
		app: fiber.New(fiber.Config{
			AppName: "tradebot",
		}),
	}
	server.routes()
	return server
}

func (s *Server) App() *fiber.App {
	return s.app
}

func (s *Server) Run(ctx context.Context) error {
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := s.app.ShutdownWithContext(shutdownCtx); err != nil {
			s.logger.Error("http shutdown failed", "error", err)
		}
	}()

	s.logger.Info("http server started", "addr", s.cfg.HTTP.Addr)
	if err := s.app.Listen(s.cfg.HTTP.Addr); err != nil && !errors.Is(err, context.Canceled) {
		return fmt.Errorf("listen http: %w", err)
	}
	return nil
}

func (s *Server) routes() {
	s.app.Get("/healthz", func(c fiber.Ctx) error {
		return c.JSON(fiber.Map{"status": "ok"})
	})
	s.app.Get("/readyz", func(c fiber.Ctx) error {
		return c.JSON(fiber.Map{
			"status":      "ok",
			"tradingview": s.cfg.TradingView.Enabled,
			"ai":          s.cfg.AI.Enabled,
			"autotrade":   s.cfg.AI.AutoTradeEnabled,
			"dry_run":     s.cfg.App.DryRun,
		})
	})
	s.app.Post("/tradingview/webhook", s.handleTradingViewWebhook)

	// Mount the embedded dashboard last: its "/*" catch-all must not shadow the
	// API routes above. A mount failure must not take down the API, so log and
	// continue with health checks and the webhook still served.
	if err := dashboard.Register(s.app); err != nil {
		s.logger.Error("dashboard mount failed", "error", err)
	}
}

func (s *Server) handleTradingViewWebhook(c fiber.Ctx) error {
	if !s.cfg.TradingView.Enabled {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "tradingview webhook is disabled"})
	}
	if s.processor == nil {
		return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{"error": "signal processor is not connected"})
	}

	var payload tradingViewPayload
	if err := json.Unmarshal(c.Body(), &payload); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "invalid JSON webhook payload",
			"hint":  `TradingView alert body should be valid JSON such as {"secret":"...","symbol":"BTCUSDT","price":"67500"}`,
		})
	}

	secret := strings.TrimSpace(c.Get("X-TradeBot-Webhook-Secret"))
	if secret == "" {
		secret = strings.TrimSpace(c.Get("X-TradingView-Secret"))
	}
	if secret == "" {
		secret = strings.TrimSpace(payload.Secret)
	}
	if secret != s.cfg.TradingView.WebhookSecret {
		s.logger.Warn("tradingview webhook rejected")
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "unauthorized webhook"})
	}

	signal := payload.Signal()
	result, err := s.processor.Process(c.Context(), signal)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
	}

	return c.JSON(result)
}

type tradingViewPayload struct {
	Secret     string         `json:"secret"`
	Source     string         `json:"source"`
	Symbol     string         `json:"symbol"`
	Ticker     string         `json:"ticker"`
	Interval   string         `json:"interval"`
	Timeframe  string         `json:"timeframe"`
	Price      string         `json:"price"`
	Close      string         `json:"close"`
	Strategy   string         `json:"strategy"`
	ActionHint string         `json:"action_hint"`
	Action     string         `json:"action"`
	SideHint   string         `json:"side_hint"`
	Message    string         `json:"message"`
	Indicators map[string]any `json:"indicators"`
}

func (p tradingViewPayload) Signal() signals.MarketSignal {
	symbol := firstNonEmpty(p.Symbol, p.Ticker)
	interval := firstNonEmpty(p.Interval, p.Timeframe)
	price := firstNonEmpty(p.Price, p.Close)
	actionHint := firstNonEmpty(p.ActionHint, p.Action)

	return signals.MarketSignal{
		Source:     p.Source,
		Symbol:     symbol,
		Interval:   interval,
		Price:      price,
		Strategy:   p.Strategy,
		ActionHint: actionHint,
		SideHint:   p.SideHint,
		Message:    p.Message,
		Indicators: stringifyMap(p.Indicators),
		ReceivedAt: time.Now(),
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func stringifyMap(values map[string]any) map[string]string {
	if len(values) == 0 {
		return map[string]string{}
	}

	result := make(map[string]string, len(values))
	for key, value := range values {
		result[key] = fmt.Sprint(value)
	}
	return result
}
