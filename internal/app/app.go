package app

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"time"

	"bottrade/internal/ai"
	"bottrade/internal/api"
	"bottrade/internal/config"
	binanceexec "bottrade/internal/exchange/binance"
	"bottrade/internal/journal"
	"bottrade/internal/logging"
	"bottrade/internal/orders"
	"bottrade/internal/plans"
	"bottrade/internal/signals"
	mongostore "bottrade/internal/storage/mongo"
	"bottrade/internal/telegram"
	"bottrade/internal/users"
)

type App struct {
	cfg    config.Config
	logger *slog.Logger
}

func New(cfg config.Config, logger *slog.Logger) *App {
	if logger == nil {
		logger = slog.Default()
	}

	return &App{
		cfg:    cfg,
		logger: logger,
	}
}

// Bootstrap loads configuration from the environment, builds the configured
// logger, and returns a ready App. Each process entrypoint (worker, api, the
// combined tradebot) calls this so configuration and logging are wired
// identically regardless of which runtime is started.
func Bootstrap() (*App, *slog.Logger, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, nil, err
	}

	logger, err := logging.New(cfg.App.LogLevel, os.Stdout)
	if err != nil {
		return nil, nil, err
	}

	return New(cfg, logger), logger, nil
}

// IsShutdown reports whether err is the result of ctx being cancelled (e.g. a
// SIGTERM caught by signal.NotifyContext), so entrypoints can exit cleanly
// instead of logging it as a fatal error. It uses errors.Is so a cancellation
// wrapped anywhere up the stack is still recognised as graceful. When ctx is
// not cancelled it short-circuits to false, so genuine runtime errors are never
// suppressed.
func IsShutdown(ctx context.Context, err error) bool {
	return ctx.Err() != nil && errors.Is(err, ctx.Err())
}

func (a *App) logBootstrap(role string) {
	a.logger.Info(
		"application bootstrap complete",
		"role", role,
		"env", a.cfg.App.Env,
		"dry_run", a.cfg.App.DryRun,
		"real_trading_enabled", a.cfg.App.RealTradingEnabled,
		"telegram_mode", a.cfg.Telegram.Mode,
		"binance_testnet", a.cfg.Binance.Testnet,
		"mongodb_database", a.cfg.MongoDB.Database,
	)
}

// Run starts the combined all-in-one runtime: the Telegram poller and (when
// HTTP is enabled) the API server in a single process. It is used by the local
// cmd/tradebot entrypoint for development. Production deploys split these into
// the worker and api processes via RunWorker and RunAPI.
func (a *App) Run(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	a.logBootstrap("all")

	orderService, statusService, planService, signalStore, cleanup, err := a.newTradingServices(ctx)
	if err != nil {
		return err
	}
	defer cleanup()

	signalProcessor := a.newSignalProcessor(orderService, signalStore)

	errCh := make(chan error, 2)
	if a.cfg.HTTP.Enabled {
		server := api.NewServer(a.cfg, signalProcessor, a.logger, a.userOptions(signalStore)...)
		go func() {
			if err := server.Run(ctx); err != nil {
				errCh <- err
			}
		}()
	}

	if a.cfg.Telegram.Mode != config.TelegramModePolling {
		a.logger.Info("telegram mode is not polling; telegram polling runtime not started", "telegram_mode", a.cfg.Telegram.Mode)
		if a.cfg.HTTP.Enabled {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case err := <-errCh:
				return err
			}
		}
		return nil
	}

	runner, err := telegram.NewPollingRunner(a.cfg, orderService, statusService, planService, a.logger)
	if err != nil {
		return err
	}

	if a.cfg.HTTP.Enabled {
		go func() {
			if err := runner.Run(ctx); err != nil {
				errCh <- err
			}
		}()

		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-errCh:
			return err
		}
	}

	return runner.Run(ctx)
}

// RunWorker starts the worker process: the long-running Telegram poller plus
// the trading services it drives. It never opens an inbound HTTP port, so it is
// safe to run as a single always-on machine. Telegram getUpdates allows only
// one poller, so this process must not be scaled beyond one instance.
func (a *App) RunWorker(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	a.logBootstrap("worker")

	orderService, statusService, planService, _, cleanup, err := a.newTradingServices(ctx)
	if err != nil {
		return err
	}
	defer cleanup()

	if a.cfg.Telegram.Mode != config.TelegramModePolling {
		a.logger.Info("telegram mode is not polling; worker idle until shutdown", "telegram_mode", a.cfg.Telegram.Mode)
		<-ctx.Done()
		return ctx.Err()
	}

	runner, err := telegram.NewPollingRunner(a.cfg, orderService, statusService, planService, a.logger)
	if err != nil {
		return err
	}

	return runner.Run(ctx)
}

// RunAPI starts the api process: the Fiber HTTP server (health checks, the
// TradingView webhook, and the future dashboard). It shares the same MongoDB as
// the worker, so confirmations created here are completed by the worker's
// Telegram poller. This process is free to scale horizontally.
func (a *App) RunAPI(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	a.logBootstrap("api")

	orderService, _, _, signalStore, cleanup, err := a.newTradingServices(ctx)
	if err != nil {
		return err
	}
	defer cleanup()

	processor := a.newSignalProcessor(orderService, signalStore)

	server := api.NewServer(a.cfg, processor, a.logger, a.userOptions(signalStore)...)
	return server.Run(ctx)
}

// userOptions wires the registration/login service. It persists to MongoDB when
// the trading store is MongoDB-backed (accounts survive restarts), and falls
// back to an in-memory store otherwise.
func (a *App) userOptions(signalStore signals.SignalStore) []api.Option {
	var repo users.Repository = users.NewMemoryRepository()
	if store, ok := signalStore.(*mongostore.Store); ok {
		repo = store.Users()
	}
	service, err := users.NewService(repo)
	if err != nil {
		a.logger.Warn("user service unavailable; registration disabled", "error", err)
		return nil
	}
	return []api.Option{api.WithUsers(service)}
}

func (a *App) newTradingServices(ctx context.Context) (*orders.Service, *orders.StatusService, *plans.Service, signals.SignalStore, func(), error) {
	connectCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	store, err := mongostore.Connect(connectCtx, mongostore.Config{
		URI:      a.cfg.MongoDB.URI,
		Database: a.cfg.MongoDB.Database,
	})
	if err != nil {
		return nil, nil, nil, nil, nil, err
	}
	cleanup := func() {
		disconnectCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := store.Disconnect(disconnectCtx); err != nil {
			a.logger.Warn("mongodb disconnect failed", "error", err)
		}
	}
	a.logger.Info("mongodb connected", "database", a.cfg.MongoDB.Database)

	executor := orders.Executor(orders.DryRunExecutor{DryRun: true})
	positionProvider := orders.PositionProvider(orders.EmptyPositionProvider{})
	if !a.cfg.App.DryRun {
		binanceExecutor := binanceexec.NewExecutor(binanceexec.ExecutorConfig{
			APIKey:               a.cfg.Binance.APIKey,
			APISecret:            a.cfg.Binance.APISecret,
			BaseURL:              a.cfg.Binance.FuturesBaseURL,
			Testnet:              a.cfg.Binance.Testnet,
			RealTradingEnabled:   a.cfg.App.RealTradingEnabled,
			RequestTimeout:       a.cfg.Binance.RequestTimeout,
			ExchangeInfoCacheTTL: a.cfg.Binance.ExchangeInfoCacheTTL,
		}, a.logger)
		executor = binanceExecutor
		positionProvider = binanceExecutor
	}

	var tradeJournal orders.TradeJournal
	if journalService, err := journal.NewService(store.Journal()); err == nil {
		tradeJournal = journalService
	}
	orderService := orders.NewServiceWithRepositories(a.cfg.App.ConfirmationTTL, executor, orders.ServiceDependencies{
		ConfirmationStore: store,
		IntentStore:       store,
		AuditRecorder:     store,
		Journal:           tradeJournal,
	}, a.logger)
	statusService := orders.NewStatusService(positionProvider)
	planService := plans.NewService(store)
	return orderService, statusService, planService, store, cleanup, nil
}

func (a *App) newSignalProcessor(orderService *orders.Service, signalStore signals.SignalStore) *signals.Processor {
	var advisor signals.Advisor
	if a.cfg.AI.Enabled {
		switch a.cfg.AI.Provider {
		case "openai_compatible":
			advisor = ai.NewOpenAICompatibleAdvisor(ai.OpenAICompatibleConfig{
				APIKey:         a.cfg.AI.APIKey,
				BaseURL:        a.cfg.AI.BaseURL,
				Model:          a.cfg.AI.Model,
				SystemPrompt:   a.cfg.AI.SystemPrompt,
				RequestTimeout: a.cfg.AI.RequestTimeout,
			})
		default:
			a.logger.Warn("ai provider is not supported", "provider", a.cfg.AI.Provider)
		}
	}

	adminUserID := a.cfg.Telegram.AdminUserID
	if adminUserID == 0 && len(a.cfg.Telegram.AllowedUserIDs) > 0 {
		adminUserID = a.cfg.Telegram.AllowedUserIDs[0]
	}
	if adminUserID == 0 {
		a.logger.Warn("signal processor has no admin user id")
	}

	return signals.NewProcessor(signals.ProcessorConfig{
		Advisor:              advisor,
		OrderService:         orderService,
		SignalStore:          signalStore,
		AdminUserID:          adminUserID,
		MaxLeverage:          a.cfg.App.MaxLeverage,
		MinConfidencePercent: a.cfg.AI.MinConfidencePercent,
		AutoTradeEnabled:     a.cfg.AI.AutoTradeEnabled,
		Logger:               a.logger,
	})
}
