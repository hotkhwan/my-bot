// Command api runs the HTTP server: health checks, the TradingView webhook, and
// the dashboard. It shares MongoDB with the worker, so it can scale
// horizontally without coordinating Telegram polling.
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"bottrade/internal/app"
)

func main() {
	bootstrapLogger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))

	application, logger, err := app.Bootstrap()
	if err != nil {
		bootstrapLogger.Error("startup error", "error", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := application.RunAPI(ctx); err != nil && !app.IsShutdown(ctx, err) {
		logger.Error("api stopped", "error", err)
		os.Exit(1)
	}
}
