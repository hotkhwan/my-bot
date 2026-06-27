package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"bottrade/internal/signals"
)

// DefaultFearGreedBaseURL is the free, key-less Crypto Fear & Greed Index API.
const DefaultFearGreedBaseURL = "https://api.alternative.me"

// FearGreedProvider adds the market-wide Crypto Fear & Greed Index (0 = extreme
// fear, 100 = extreme greed) to the AI prompt under [narrative]. It is a single
// free endpoint with no API key. The index is market-wide, not per-symbol.
type FearGreedProvider struct {
	baseURL string
	client  *http.Client
	name    string
}

// NewFearGreedProvider builds the provider. An empty baseURL defaults to the
// public host; a nil client gets an 8s-timeout client.
func NewFearGreedProvider(baseURL string, client *http.Client) *FearGreedProvider {
	if strings.TrimSpace(baseURL) == "" {
		baseURL = DefaultFearGreedBaseURL
	}
	if client == nil {
		client = &http.Client{Timeout: 8 * time.Second}
	}
	return &FearGreedProvider{baseURL: strings.TrimRight(baseURL, "/"), client: client, name: "fear_greed"}
}

func (p *FearGreedProvider) Name() string { return p.name }

func (p *FearGreedProvider) Category() ContextCategory { return CategoryNarrative }

func (p *FearGreedProvider) Enrich(ctx context.Context, _ signals.MarketSignal) (ContextFragment, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.baseURL+"/fng/?limit=1", nil)
	if err != nil {
		return ContextFragment{}, err
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return ContextFragment{}, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return ContextFragment{}, err
	}
	if resp.StatusCode >= 400 {
		return ContextFragment{}, fmt.Errorf("fear_greed: API returned %d", resp.StatusCode)
	}

	var payload struct {
		Data []struct {
			Value          string `json:"value"`
			Classification string `json:"value_classification"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return ContextFragment{}, fmt.Errorf("fear_greed: decode: %w", err)
	}
	if len(payload.Data) == 0 {
		return ContextFragment{}, fmt.Errorf("fear_greed: empty response")
	}

	entry := payload.Data[0]
	metrics := map[string]string{"fear_greed_value": entry.Value}
	summary := "Crypto Fear & Greed Index: " + entry.Value
	if entry.Classification != "" {
		metrics["fear_greed_classification"] = entry.Classification
		summary += " (" + entry.Classification + ")"
	}
	return ContextFragment{
		Provider: p.name,
		Category: CategoryNarrative,
		Summary:  summary,
		Metrics:  metrics,
	}, nil
}
