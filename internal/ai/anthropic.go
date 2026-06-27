package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"bottrade/internal/signals"
)

// AnthropicConfig configures the native Claude advisor (Anthropic Messages API).
type AnthropicConfig struct {
	APIKey         string
	BaseURL        string // default https://api.anthropic.com/v1
	Model          string // default claude-opus-4-8
	SystemPrompt   string
	MaxTokens      int
	RequestTimeout time.Duration
	HTTPClient     *http.Client
	Enricher       ContextEnricher
}

// AnthropicAdvisor decides trades with Claude. It implements signals.Advisor, so
// it drops into the ensemble alongside the OpenAI-compatible advisors
// (DeepSeek/Qwen).
type AnthropicAdvisor struct {
	cfg    AnthropicConfig
	client *http.Client
}

// NewAnthropicAdvisor applies defaults and returns the advisor.
func NewAnthropicAdvisor(cfg AnthropicConfig) *AnthropicAdvisor {
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://api.anthropic.com/v1"
	}
	if cfg.Model == "" {
		cfg.Model = "claude-opus-4-8"
	}
	if cfg.SystemPrompt == "" {
		cfg.SystemPrompt = defaultSystemPrompt
	}
	if cfg.MaxTokens <= 0 {
		cfg.MaxTokens = 1024
	}
	if cfg.RequestTimeout <= 0 {
		cfg.RequestTimeout = 20 * time.Second
	}
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: cfg.RequestTimeout}
	}
	return &AnthropicAdvisor{cfg: cfg, client: client}
}

func (a *AnthropicAdvisor) Decide(ctx context.Context, signal signals.MarketSignal) (signals.Decision, error) {
	if strings.TrimSpace(a.cfg.APIKey) == "" {
		return signals.Decision{}, fmt.Errorf("Anthropic API key is required")
	}
	if strings.TrimSpace(a.cfg.Model) == "" {
		return signals.Decision{}, fmt.Errorf("Anthropic model is required")
	}

	userContent := signalPrompt(signal)
	if a.cfg.Enricher != nil {
		if block := a.cfg.Enricher.Gather(ctx, signal).Prompt(); block != "" {
			userContent = block + "\n\n" + userContent
		}
	}

	payload := anthropicRequest{
		Model:     a.cfg.Model,
		MaxTokens: a.cfg.MaxTokens,
		System:    a.cfg.SystemPrompt,
		Messages:  []anthropicMessage{{Role: "user", Content: userContent}},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return signals.Decision{}, err
	}

	endpoint := strings.TrimRight(a.cfg.BaseURL, "/") + "/messages"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return signals.Decision{}, err
	}
	req.Header.Set("x-api-key", a.cfg.APIKey)
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("content-type", "application/json")

	resp, err := a.client.Do(req)
	if err != nil {
		return signals.Decision{}, err
	}
	defer resp.Body.Close()

	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return signals.Decision{}, err
	}
	if resp.StatusCode >= 400 {
		return signals.Decision{}, fmt.Errorf("Anthropic API returned %d: %s", resp.StatusCode, string(responseBody))
	}

	var message anthropicResponse
	if err := json.Unmarshal(responseBody, &message); err != nil {
		return signals.Decision{}, fmt.Errorf("decode Anthropic response: %w", err)
	}
	text := message.firstText()
	if text == "" {
		return signals.Decision{}, fmt.Errorf("Anthropic response had no text content")
	}

	var decision signals.Decision
	if err := json.Unmarshal([]byte(extractJSON(text)), &decision); err != nil {
		return signals.Decision{}, fmt.Errorf("decode Anthropic decision JSON: %w", err)
	}
	return decision, nil
}

// extractJSON returns the JSON object embedded in text, in case the model wraps
// it in prose or a code fence despite being told to return only JSON.
func extractJSON(text string) string {
	start := strings.IndexByte(text, '{')
	end := strings.LastIndexByte(text, '}')
	if start >= 0 && end > start {
		return text[start : end+1]
	}
	return text
}

type anthropicRequest struct {
	Model     string             `json:"model"`
	MaxTokens int                `json:"max_tokens"`
	System    string             `json:"system"`
	Messages  []anthropicMessage `json:"messages"`
}

type anthropicMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type anthropicResponse struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
}

func (r anthropicResponse) firstText() string {
	for _, block := range r.Content {
		if block.Type == "text" && block.Text != "" {
			return block.Text
		}
	}
	return ""
}
