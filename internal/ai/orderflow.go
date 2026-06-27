package ai

import (
	"context"
	"fmt"
	"strings"
	"time"

	"bottrade/internal/marketdata"
	"bottrade/internal/signals"
)

// OrderFlowProvider turns a marketdata.Provider (funding, open interest,
// long/short ratio, taker buy/sell flow) into a ContextProvider, so the
// derivatives picture flows into the AI prompt under the [orderflow] category.
// It is the bridge that lets the advisor weigh Binance's free Futures data
// without binding to any specific vendor — swap the underlying provider and the
// AI is unchanged.
//
// It presents the metrics factually and does not editorialise a sentiment: the
// model weighs the combined evidence itself.
type OrderFlowProvider struct {
	source marketdata.Provider
	period string
	name   string
	clock  func() time.Time
}

// NewOrderFlowProvider wraps source. period is the sampling window for the ratio
// endpoints ("5m", "1h", ...); an empty period defaults inside the provider.
func NewOrderFlowProvider(source marketdata.Provider, period string) *OrderFlowProvider {
	if strings.TrimSpace(period) == "" {
		period = "5m"
	}
	return &OrderFlowProvider{
		source: source,
		period: period,
		name:   "binance_orderflow",
		clock:  time.Now,
	}
}

func (p *OrderFlowProvider) Name() string { return p.name }

func (p *OrderFlowProvider) Category() ContextCategory { return CategoryOrderFlow }

// Enrich collects a market-data snapshot and renders the available metrics into
// a fragment. A partially-failing snapshot still yields whatever was fetched;
// only a total failure (no metric available) returns the underlying error so the
// aggregator skips this provider for the decision.
func (p *OrderFlowProvider) Enrich(ctx context.Context, signal signals.MarketSignal) (ContextFragment, error) {
	symbol := strings.TrimSpace(signal.Symbol)
	if symbol == "" {
		return ContextFragment{}, fmt.Errorf("orderflow: signal has no symbol")
	}

	snapshot, collectErr := marketdata.Collect(ctx, p.source, symbol, p.period, p.clock())

	metrics := map[string]string{}
	var parts []string

	if snapshot.Funding.MarkPrice.IsPositive() {
		metrics["mark_price"] = snapshot.Funding.MarkPrice.String()
		metrics["funding_rate"] = snapshot.Funding.LastFundingRate.String()
		parts = append(parts, "funding "+snapshot.Funding.LastFundingRate.String()+" (per 8h)")
	}
	if snapshot.OpenInterest.OpenInterest.IsPositive() {
		metrics["open_interest"] = snapshot.OpenInterest.OpenInterest.String()
		parts = append(parts, "OI "+snapshot.OpenInterest.OpenInterest.String())
	}
	if snapshot.LongShort.Ratio.IsPositive() {
		metrics["long_short_account_ratio"] = snapshot.LongShort.Ratio.String()
		metrics["long_account"] = snapshot.LongShort.LongAccount.String()
		metrics["short_account"] = snapshot.LongShort.ShortAccount.String()
		parts = append(parts, "long/short accounts "+snapshot.LongShort.Ratio.String())
	}
	if snapshot.Taker.BuySellRatio.IsPositive() {
		metrics["taker_buy_sell_ratio"] = snapshot.Taker.BuySellRatio.String()
		parts = append(parts, "taker buy/sell "+snapshot.Taker.BuySellRatio.String())
	}

	if len(metrics) == 0 {
		if collectErr != nil {
			return ContextFragment{}, collectErr
		}
		return ContextFragment{}, fmt.Errorf("orderflow: no market data available for %s", symbol)
	}
	metrics["period"] = p.period

	summary := fmt.Sprintf("Binance %s derivatives — %s.", symbol, strings.Join(parts, ", "))
	return ContextFragment{
		Provider: p.name,
		Category: CategoryOrderFlow,
		Summary:  summary,
		Metrics:  metrics,
	}, nil
}
