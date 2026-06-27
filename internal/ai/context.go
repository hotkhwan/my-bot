package ai

import (
	"context"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"bottrade/internal/signals"
)

// ContextCategory groups an enrichment fragment by the kind of edge it adds,
// mirroring the research flow: narrative/news, on-chain, order flow, risk.
type ContextCategory string

const (
	CategoryNarrative ContextCategory = "narrative"
	CategoryOnChain   ContextCategory = "onchain"
	CategoryOrderFlow ContextCategory = "orderflow"
	CategoryTechnical ContextCategory = "technical"
	CategoryRisk      ContextCategory = "risk"
)

// categoryOrder fixes the rendering order so prompts are deterministic.
var categoryOrder = []ContextCategory{
	CategoryNarrative,
	CategoryOnChain,
	CategoryOrderFlow,
	CategoryTechnical,
	CategoryRisk,
}

// ContextFragment is one data source's contribution to the decision context.
// Metrics are kept as strings on purpose: order-critical numbers must not pass
// through float64, and these values are only ever rendered into the LLM prompt.
type ContextFragment struct {
	Provider  string
	Category  ContextCategory
	Summary   string
	Sentiment string            // optional: bullish | bearish | neutral
	Metrics   map[string]string // optional structured metrics, e.g. open_interest_change
}

// ContextProvider enriches a market signal from one external source, e.g.
// on-chain whale tracking, funding/open-interest order flow, or news sentiment.
// Implementations live behind this boundary so the exchange, news, and on-chain
// integrations stay mockable.
type ContextProvider interface {
	Name() string
	Category() ContextCategory
	Enrich(ctx context.Context, signal signals.MarketSignal) (ContextFragment, error)
}

// MarketContext is the merged, ordered set of fragments gathered for a signal.
type MarketContext struct {
	Fragments []ContextFragment
}

// IsEmpty reports whether any context was gathered.
func (c MarketContext) IsEmpty() bool { return len(c.Fragments) == 0 }

// Prompt renders the gathered context as a deterministic text block for the LLM,
// grouped by category. It returns an empty string when no context was gathered.
func (c MarketContext) Prompt() string {
	if len(c.Fragments) == 0 {
		return ""
	}

	blocks := []string{"Multi-source market context (weigh these; never invent missing data):"}

	for _, category := range categoryOrder {
		if block := renderCategory(category, c.fragmentsByCategory(category)); block != "" {
			blocks = append(blocks, block)
		}
	}
	// Any provider using a custom category still gets rendered, after the
	// known buckets, in insertion order.
	for _, category := range c.unknownCategories() {
		if block := renderCategory(category, c.fragmentsByCategory(category)); block != "" {
			blocks = append(blocks, block)
		}
	}

	return strings.Join(blocks, "\n\n")
}

func (c MarketContext) fragmentsByCategory(category ContextCategory) []ContextFragment {
	out := make([]ContextFragment, 0, len(c.Fragments))
	for _, fragment := range c.Fragments {
		if fragment.Category == category {
			out = append(out, fragment)
		}
	}
	return out
}

func (c MarketContext) unknownCategories() []ContextCategory {
	known := map[ContextCategory]bool{}
	for _, category := range categoryOrder {
		known[category] = true
	}

	var unknown []ContextCategory
	seen := map[ContextCategory]bool{}
	for _, fragment := range c.Fragments {
		if known[fragment.Category] || seen[fragment.Category] {
			continue
		}
		seen[fragment.Category] = true
		unknown = append(unknown, fragment.Category)
	}
	return unknown
}

func renderCategory(category ContextCategory, fragments []ContextFragment) string {
	if len(fragments) == 0 {
		return ""
	}

	var b strings.Builder
	b.WriteString("[" + string(category) + "]")
	for _, fragment := range fragments {
		b.WriteString("\n- " + fragment.Provider)
		if fragment.Sentiment != "" {
			b.WriteString(" (" + fragment.Sentiment + ")")
		}
		if fragment.Summary != "" {
			b.WriteString(": " + fragment.Summary)
		}
		for _, key := range sortedKeys(fragment.Metrics) {
			b.WriteString("\n    " + key + "=" + fragment.Metrics[key])
		}
	}
	return b.String()
}

func sortedKeys(metrics map[string]string) []string {
	if len(metrics) == 0 {
		return nil
	}
	keys := make([]string, 0, len(metrics))
	for key := range metrics {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// Aggregator fans out to every configured ContextProvider and merges their
// fragments. *Aggregator satisfies ContextEnricher.
type Aggregator struct {
	providers       []ContextProvider
	providerTimeout time.Duration
	logger          *slog.Logger
}

// AggregatorConfig configures a context Aggregator.
type AggregatorConfig struct {
	Providers       []ContextProvider
	ProviderTimeout time.Duration
	Logger          *slog.Logger
}

// NewAggregator builds an Aggregator with a sane per-provider timeout default.
func NewAggregator(cfg AggregatorConfig) *Aggregator {
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	timeout := cfg.ProviderTimeout
	if timeout <= 0 {
		timeout = 8 * time.Second
	}
	return &Aggregator{
		providers:       cfg.Providers,
		providerTimeout: timeout,
		logger:          logger,
	}
}

// Gather runs all providers concurrently, each under its own timeout, and merges
// the successful fragments in provider order. A slow or failing provider is
// logged and skipped; it never fails the whole decision, so the model still runs
// on whatever context was available. This intentionally favours fewer false
// signals over blocking on a single unavailable source.
func (a *Aggregator) Gather(ctx context.Context, signal signals.MarketSignal) MarketContext {
	if len(a.providers) == 0 {
		return MarketContext{}
	}

	fragments := make([]ContextFragment, len(a.providers))
	ok := make([]bool, len(a.providers))

	var wg sync.WaitGroup
	for i, provider := range a.providers {
		wg.Add(1)
		go func(i int, provider ContextProvider) {
			defer wg.Done()

			providerCtx, cancel := context.WithTimeout(ctx, a.providerTimeout)
			defer cancel()

			fragment, err := provider.Enrich(providerCtx, signal)
			if err != nil {
				a.logger.Warn("context provider failed", "provider", provider.Name(), "category", provider.Category(), "error", err)
				return
			}
			if fragment.Provider == "" {
				fragment.Provider = provider.Name()
			}
			if fragment.Category == "" {
				fragment.Category = provider.Category()
			}

			fragments[i] = fragment
			ok[i] = true
		}(i, provider)
	}
	wg.Wait()

	merged := make([]ContextFragment, 0, len(fragments))
	for i := range fragments {
		if ok[i] {
			merged = append(merged, fragments[i])
		}
	}
	return MarketContext{Fragments: merged}
}
