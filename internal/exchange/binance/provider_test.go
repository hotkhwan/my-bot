package binance

import (
	"context"
	"testing"

	"bottrade/internal/auth"
)

type stubLoader struct {
	keys auth.BinanceKeys
	err  error
}

func (l stubLoader) Load(context.Context, string) (auth.BinanceKeys, error) {
	return l.keys, l.err
}

func TestExecutorProviderBuildsAndCaches(t *testing.T) {
	provider := NewExecutorProvider(
		ExecutorConfig{BaseURL: "https://example", Testnet: true},
		stubLoader{keys: auth.BinanceKeys{APIKey: "user-key", APISecret: "user-secret"}},
		testLogger(),
	)

	executor, ok, err := provider.ExecutorFor(context.Background(), "tg:1")
	if err != nil || !ok || executor == nil {
		t.Fatalf("ExecutorFor = (%v, %v, %v), want a found executor", executor, ok, err)
	}
	// Cached: a second lookup returns the same instance.
	again, _, _ := provider.ExecutorFor(context.Background(), "tg:1")
	if again != executor {
		t.Fatal("executor not cached per user")
	}
}

func TestExecutorProviderNoCredential(t *testing.T) {
	provider := NewExecutorProvider(ExecutorConfig{Testnet: true}, stubLoader{err: auth.ErrNoCredential}, testLogger())
	executor, ok, err := provider.ExecutorFor(context.Background(), "tg:2")
	if err != nil || ok || executor != nil {
		t.Fatalf("ExecutorFor = (%v, %v, %v), want not-found and no error", executor, ok, err)
	}
}
