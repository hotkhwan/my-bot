package ai

import (
	"context"
	"errors"
	"strings"
	"testing"

	"bottrade/internal/decimal"
	"bottrade/internal/marketdata"
	"bottrade/internal/signals"
)

func TestOrderFlowProviderEnrichRendersMetrics(t *testing.T) {
	mock := marketdata.MockProvider{
		FundingValue:   marketdata.Funding{MarkPrice: decimal.MustParse("68000"), LastFundingRate: decimal.MustParse("0.0001")},
		OIValue:        marketdata.OpenInterest{OpenInterest: decimal.MustParse("12345.6")},
		LongShortValue: marketdata.LongShortRatio{Ratio: decimal.MustParse("1.8"), LongAccount: decimal.MustParse("0.64"), ShortAccount: decimal.MustParse("0.36")},
		TakerValue:     marketdata.TakerFlow{BuySellRatio: decimal.MustParse("1.2")},
	}
	provider := NewOrderFlowProvider(mock, "1h")

	if provider.Category() != CategoryOrderFlow || provider.Name() != "binance_orderflow" {
		t.Fatalf("provider identity = %q/%q", provider.Name(), provider.Category())
	}

	fragment, err := provider.Enrich(context.Background(), signals.MarketSignal{Symbol: "BTCUSDT"})
	if err != nil {
		t.Fatalf("Enrich: %v", err)
	}
	if fragment.Metrics["funding_rate"] != "0.0001" ||
		fragment.Metrics["open_interest"] != "12345.6" ||
		fragment.Metrics["long_short_account_ratio"] != "1.8" ||
		fragment.Metrics["taker_buy_sell_ratio"] != "1.2" ||
		fragment.Metrics["period"] != "1h" {
		t.Fatalf("metrics = %#v", fragment.Metrics)
	}

	// The fragment must render inside the AI prompt under [orderflow].
	prompt := MarketContext{Fragments: []ContextFragment{fragment}}.Prompt()
	if !strings.Contains(prompt, "[orderflow]") || !strings.Contains(prompt, "funding_rate=0.0001") {
		t.Fatalf("prompt missing order-flow block:\n%s", prompt)
	}
}

func TestOrderFlowProviderPartialDataStillRenders(t *testing.T) {
	// Only funding available (other endpoints returned zero-value): the fragment
	// should still render the funding metric rather than failing.
	mock := marketdata.MockProvider{
		FundingValue: marketdata.Funding{MarkPrice: decimal.MustParse("3000"), LastFundingRate: decimal.MustParse("-0.0002")},
	}
	fragment, err := NewOrderFlowProvider(mock, "").Enrich(context.Background(), signals.MarketSignal{Symbol: "ETHUSDT"})
	if err != nil {
		t.Fatalf("Enrich: %v", err)
	}
	if fragment.Metrics["funding_rate"] != "-0.0002" {
		t.Fatalf("funding metric = %q", fragment.Metrics["funding_rate"])
	}
	if _, ok := fragment.Metrics["open_interest"]; ok {
		t.Fatal("open_interest should be absent when zero/unavailable")
	}
}

func TestOrderFlowProviderTotalFailureReturnsError(t *testing.T) {
	mock := marketdata.MockProvider{Err: errors.New("network down")}
	_, err := NewOrderFlowProvider(mock, "5m").Enrich(context.Background(), signals.MarketSignal{Symbol: "BTCUSDT"})
	if err == nil {
		t.Fatal("expected an error when no market data is available")
	}
}

func TestOrderFlowProviderNoSymbol(t *testing.T) {
	_, err := NewOrderFlowProvider(marketdata.MockProvider{}, "5m").Enrich(context.Background(), signals.MarketSignal{})
	if err == nil {
		t.Fatal("expected an error when the signal has no symbol")
	}
}

func TestOrderFlowProviderSatisfiesContextProvider(t *testing.T) {
	var _ ContextProvider = NewOrderFlowProvider(marketdata.MockProvider{}, "5m")
}
