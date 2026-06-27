package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"bottrade/internal/signals"
)

// DefaultNewsBaseURL is the NewsAPI v2 host.
const DefaultNewsBaseURL = "https://newsapi.org/v2"

// NewsProvider adds recent crypto headlines for the traded asset to the AI
// prompt under [narrative]. It uses NewsAPI's free tier (an API key is required),
// so it is only registered when a key is configured.
type NewsProvider struct {
	apiKey   string
	baseURL  string
	client   *http.Client
	pageSize int
	name     string
}

// NewNewsProvider builds the provider. An empty baseURL defaults to NewsAPI; a
// nil client gets an 8s-timeout client; pageSize defaults to 5.
func NewNewsProvider(apiKey, baseURL string, pageSize int, client *http.Client) *NewsProvider {
	if strings.TrimSpace(baseURL) == "" {
		baseURL = DefaultNewsBaseURL
	}
	if pageSize <= 0 {
		pageSize = 5
	}
	if client == nil {
		client = &http.Client{Timeout: 8 * time.Second}
	}
	return &NewsProvider{
		apiKey:   apiKey,
		baseURL:  strings.TrimRight(baseURL, "/"),
		client:   client,
		pageSize: pageSize,
		name:     "news",
	}
}

func (p *NewsProvider) Name() string { return p.name }

func (p *NewsProvider) Category() ContextCategory { return CategoryNarrative }

func (p *NewsProvider) Enrich(ctx context.Context, signal signals.MarketSignal) (ContextFragment, error) {
	if strings.TrimSpace(p.apiKey) == "" {
		return ContextFragment{}, fmt.Errorf("news: API key is required")
	}

	query := newsQuery(signal.Symbol)
	params := url.Values{
		"q":        {query},
		"pageSize": {strconv.Itoa(p.pageSize)},
		"sortBy":   {"publishedAt"},
		"language": {"en"},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.baseURL+"/everything?"+params.Encode(), nil)
	if err != nil {
		return ContextFragment{}, err
	}
	// Pass the key as a header so it never lands in URLs or logs.
	req.Header.Set("X-Api-Key", p.apiKey)

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
		return ContextFragment{}, fmt.Errorf("news: API returned %d", resp.StatusCode)
	}

	var payload struct {
		Articles []struct {
			Title       string `json:"title"`
			PublishedAt string `json:"publishedAt"`
			Source      struct {
				Name string `json:"name"`
			} `json:"source"`
		} `json:"articles"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return ContextFragment{}, fmt.Errorf("news: decode: %w", err)
	}
	if len(payload.Articles) == 0 {
		return ContextFragment{}, fmt.Errorf("news: no headlines for %s", query)
	}

	headlines := make([]string, 0, len(payload.Articles))
	for _, article := range payload.Articles {
		title := strings.TrimSpace(article.Title)
		if title == "" {
			continue
		}
		if article.Source.Name != "" {
			title += " (" + article.Source.Name + ")"
		}
		headlines = append(headlines, title)
	}
	if len(headlines) == 0 {
		return ContextFragment{}, fmt.Errorf("news: no usable headlines for %s", query)
	}

	return ContextFragment{
		Provider: p.name,
		Category: CategoryNarrative,
		Summary:  fmt.Sprintf("Recent headlines for %s:\n- %s", query, strings.Join(headlines, "\n- ")),
		Metrics:  map[string]string{"headline_count": strconv.Itoa(len(headlines))},
	}, nil
}

// newsQuery derives a search term from a symbol: the base asset plus "crypto" to
// keep results on-topic. BTCUSDT -> "BTC crypto".
func newsQuery(symbol string) string {
	base := strings.ToUpper(strings.TrimSpace(symbol))
	base = strings.TrimSuffix(base, "USDT")
	base = strings.TrimSuffix(base, "USD")
	if base == "" {
		return "cryptocurrency"
	}
	return base + " crypto"
}
