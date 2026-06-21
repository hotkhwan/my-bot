// Command worker runs the long-running trading worker: the Telegram poller and
// the trading services it drives. It opens no inbound HTTP port and must run as
// a single instance (Telegram getUpdates allows only one poller).
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

	if err := application.RunWorker(ctx); err != nil && !app.IsShutdown(ctx, err) {
		logger.Error("worker stopped", "error", err)
		os.Exit(1)
	}
}
