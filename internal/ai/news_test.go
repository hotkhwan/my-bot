package ai

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"bottrade/internal/signals"
)

func TestNewsProviderEnrich(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Api-Key") != "key123" {
			t.Errorf("missing/incorrect api key header: %q", r.Header.Get("X-Api-Key"))
		}
		if got := r.URL.Query().Get("q"); got != "BTC crypto" {
			t.Errorf("query = %q, want 'BTC crypto'", got)
		}
		_, _ = w.Write([]byte(`{"status":"ok","articles":[{"title":"Bitcoin ETF inflows surge","publishedAt":"2026-06-27T00:00:00Z","source":{"name":"Reuters"}},{"title":"BTC funding turns positive","source":{"name":"CoinDesk"}}]}`))
	}))
	t.Cleanup(server.Close)

	provider := NewNewsProvider("key123", server.URL, 5, server.Client())
	if provider.Category() != CategoryNarrative || provider.Name() != "news" {
		t.Fatalf("identity = %q/%q", provider.Name(), provider.Category())
	}

	fragment, err := provider.Enrich(context.Background(), signals.MarketSignal{Symbol: "BTCUSDT"})
	if err != nil {
		t.Fatalf("Enrich: %v", err)
	}
	if fragment.Metrics["headline_count"] != "2" {
		t.Fatalf("headline_count = %q, want 2", fragment.Metrics["headline_count"])
	}
	if !strings.Contains(fragment.Summary, "Bitcoin ETF inflows surge (Reuters)") {
		t.Fatalf("summary missing headline: %q", fragment.Summary)
	}
}

func TestNewsProviderRequiresKey(t *testing.T) {
	_, err := NewNewsProvider("", "", 5, nil).Enrich(context.Background(), signals.MarketSignal{Symbol: "BTCUSDT"})
	if err == nil {
		t.Fatal("expected an error without an API key")
	}
}

func TestNewsProviderHTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "rate limited", http.StatusTooManyRequests)
	}))
	t.Cleanup(server.Close)
	_, err := NewNewsProvider("k", server.URL, 5, server.Client()).Enrich(context.Background(), signals.MarketSignal{Symbol: "ETHUSDT"})
	if err == nil {
		t.Fatal("expected an error on HTTP 429")
	}
}

func TestNewsQuery(t *testing.T) {
	cases := map[string]string{"BTCUSDT": "BTC crypto", "ETHUSD": "ETH crypto", "": "cryptocurrency"}
	for in, want := range cases {
		if got := newsQuery(in); got != want {
			t.Fatalf("newsQuery(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNewsProviderSatisfiesContextProvider(t *testing.T) {
	var _ ContextProvider = NewNewsProvider("k", "", 5, nil)
}
