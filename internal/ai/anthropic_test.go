package ai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"bottrade/internal/signals"
)

func TestAnthropicAdvisorDecide(t *testing.T) {
	var gotPath, gotKey, gotVersion string
	var reqBody anthropicRequest

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotKey = r.Header.Get("x-api-key")
		gotVersion = r.Header.Get("anthropic-version")
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &reqBody)

		// Claude wraps the JSON in prose to exercise extractJSON.
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"Here is my call:\n{\"action\":\"open\",\"symbol\":\"BTCUSDT\",\"side\":\"long\",\"leverage\":3,\"entry\":\"60000\",\"stop_loss\":\"58000\",\"take_profit\":\"64000\",\"size_usdt\":\"100\",\"confidence_percent\":72,\"reason\":\"trend\"}"}]}`))
	}))
	defer server.Close()

	advisor := NewAnthropicAdvisor(AnthropicConfig{
		APIKey:     "sk-test",
		BaseURL:    server.URL,
		Model:      "claude-opus-4-8",
		HTTPClient: server.Client(),
	})

	decision, err := advisor.Decide(context.Background(), signals.MarketSignal{Symbol: "BTCUSDT"})
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}

	if gotPath != "/messages" || gotKey != "sk-test" || gotVersion != "2023-06-01" {
		t.Fatalf("request path/headers = %q / %q / %q", gotPath, gotKey, gotVersion)
	}
	if reqBody.Model != "claude-opus-4-8" || reqBody.System == "" || len(reqBody.Messages) != 1 {
		t.Fatalf("request body = %+v, want model+system+1 message", reqBody)
	}
	if decision.Action != signals.ActionOpen || decision.Side != "long" ||
		decision.Entry != "60000" || decision.ConfidencePercent != 72 {
		t.Fatalf("decision = %+v, want open long 60000 @72%%", decision)
	}
}

func TestAnthropicAdvisorAPIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"bad key"}}`))
	}))
	defer server.Close()

	advisor := NewAnthropicAdvisor(AnthropicConfig{APIKey: "x", BaseURL: server.URL, HTTPClient: server.Client()})
	_, err := advisor.Decide(context.Background(), signals.MarketSignal{Symbol: "BTCUSDT"})
	if err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("err = %v, want 401 surfaced", err)
	}
}

func TestAnthropicAdvisorRequiresKey(t *testing.T) {
	advisor := NewAnthropicAdvisor(AnthropicConfig{})
	if _, err := advisor.Decide(context.Background(), signals.MarketSignal{Symbol: "BTCUSDT"}); err == nil {
		t.Fatal("expected error for missing API key")
	}
}
