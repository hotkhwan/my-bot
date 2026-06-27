package ai

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"bottrade/internal/signals"
)

func TestFearGreedProviderEnrich(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/fng") {
			t.Errorf("path = %q", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"name":"Fear and Greed Index","data":[{"value":"54","value_classification":"Neutral","timestamp":"1700000000"}]}`))
	}))
	t.Cleanup(server.Close)

	provider := NewFearGreedProvider(server.URL, server.Client())
	if provider.Category() != CategoryNarrative || provider.Name() != "fear_greed" {
		t.Fatalf("identity = %q/%q", provider.Name(), provider.Category())
	}

	fragment, err := provider.Enrich(context.Background(), signals.MarketSignal{Symbol: "BTCUSDT"})
	if err != nil {
		t.Fatalf("Enrich: %v", err)
	}
	if fragment.Metrics["fear_greed_value"] != "54" || fragment.Metrics["fear_greed_classification"] != "Neutral" {
		t.Fatalf("metrics = %#v", fragment.Metrics)
	}

	prompt := MarketContext{Fragments: []ContextFragment{fragment}}.Prompt()
	if !strings.Contains(prompt, "[narrative]") || !strings.Contains(prompt, "fear_greed_value=54") {
		t.Fatalf("prompt missing narrative block:\n%s", prompt)
	}
}

func TestFearGreedProviderHTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "down", http.StatusServiceUnavailable)
	}))
	t.Cleanup(server.Close)
	if _, err := NewFearGreedProvider(server.URL, server.Client()).Enrich(context.Background(), signals.MarketSignal{}); err == nil {
		t.Fatal("expected an error on HTTP 503")
	}
}

func TestFearGreedProviderSatisfiesContextProvider(t *testing.T) {
	var _ ContextProvider = NewFearGreedProvider("", nil)
}
