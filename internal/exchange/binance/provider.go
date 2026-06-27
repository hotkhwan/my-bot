package binance

import (
	"context"
	"errors"
	"log/slog"
	"sync"

	"bottrade/internal/auth"
	"bottrade/internal/orders"
)

// CredentialLoader loads a user's decrypted Binance keys. *auth.CredentialService
// implements it.
type CredentialLoader interface {
	Load(ctx context.Context, userID string) (auth.BinanceKeys, error)
}

// ExecutorProvider builds and caches a Binance executor per user from their
// stored key, so each user trades on their own account. It implements
// orders.ExecutorProvider.
type ExecutorProvider struct {
	base   ExecutorConfig
	loader CredentialLoader
	logger *slog.Logger

	mu    sync.Mutex
	cache map[string]*Executor
}

// NewExecutorProvider uses base for the non-key settings (base URL, testnet
// gate, timeouts) and swaps in each user's key. The base testnet/real-trading
// gates always win, so a user key can never escape them.
func NewExecutorProvider(base ExecutorConfig, loader CredentialLoader, logger *slog.Logger) *ExecutorProvider {
	if logger == nil {
		logger = slog.Default()
	}
	return &ExecutorProvider{base: base, loader: loader, logger: logger, cache: make(map[string]*Executor)}
}

// ExecutorFor returns the user's executor and true, or (nil, false) when the
// user has no stored credential — so the caller falls back to the default.
func (p *ExecutorProvider) ExecutorFor(ctx context.Context, userKey string) (orders.Executor, bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if executor, ok := p.cache[userKey]; ok {
		return executor, true, nil
	}

	keys, err := p.loader.Load(ctx, userKey)
	if errors.Is(err, auth.ErrNoCredential) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}

	cfg := p.base
	cfg.APIKey = keys.APIKey
	cfg.APISecret = keys.APISecret
	// Force the global testnet / real-trading gates; the user's stored testnet
	// flag is informational only and can never widen them.
	cfg.Testnet = p.base.Testnet
	cfg.RealTradingEnabled = p.base.RealTradingEnabled

	executor := NewExecutor(cfg, p.logger)
	p.cache[userKey] = executor
	return executor, true, nil
}
