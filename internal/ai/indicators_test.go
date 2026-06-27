package ai

import (
	"context"
	"errors"
	"strings"
	"testing"

	"bottrade/internal/signals"
)

type stubKlines struct {
	closes []float64
	err    error
}

func (s stubKlines) Closes(context.Context, string, string, int) ([]float64, error) {
	return s.closes, s.err
}

func uptrend(n int) []float64 {
	out := make([]float64, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, 100+float64(i))
	}
	return out
}

func TestIndicatorProviderRendersIndicators(t *testing.T) {
	provider := NewIndicatorProvider(stubKlines{closes: uptrend(80)}, "1h", 200)
	if provider.Category() != CategoryTechnical || provider.Name() != "binance_ta" {
		t.Fatalf("identity = %q/%q", provider.Name(), provider.Category())
	}

	fragment, err := provider.Enrich(context.Background(), signals.MarketSignal{Symbol: "BTCUSDT"})
	if err != nil {
		t.Fatalf("Enrich: %v", err)
	}
	for _, key := range []string{"ema_20", "ema_50", "rsi_14", "macd", "macd_signal", "macd_histogram", "interval"} {
		if _, ok := fragment.Metrics[key]; !ok {
			t.Fatalf("metric %q missing: %#v", key, fragment.Metrics)
		}
	}
	if fragment.Metrics["rsi_14"] != "100" {
		t.Fatalf("rsi in a pure uptrend = %q, want 100", fragment.Metrics["rsi_14"])
	}

	prompt := MarketContext{Fragments: []ContextFragment{fragment}}.Prompt()
	if !strings.Contains(prompt, "[technical]") || !strings.Contains(prompt, "rsi_14=100") {
		t.Fatalf("prompt missing technical block:\n%s", prompt)
	}
}

func TestIndicatorProviderInsufficientData(t *testing.T) {
	// Too few candles to seed EMA50/MACD: Enrich should error rather than emit a
	// partial, misleading fragment.
	_, err := NewIndicatorProvider(stubKlines{closes: uptrend(5)}, "1h", 200).
		Enrich(context.Background(), signals.MarketSignal{Symbol: "BTCUSDT"})
	if err == nil {
		t.Fatal("expected an error when there are too few klines")
	}
}

func TestIndicatorProviderSourceError(t *testing.T) {
	_, err := NewIndicatorProvider(stubKlines{err: errors.New("boom")}, "1h", 200).
		Enrich(context.Background(), signals.MarketSignal{Symbol: "BTCUSDT"})
	if err == nil {
		t.Fatal("expected the source error to propagate")
	}
}

func TestIndicatorProviderSatisfiesContextProvider(t *testing.T) {
	var _ ContextProvider = NewIndicatorProvider(stubKlines{}, "1h", 200)
}
