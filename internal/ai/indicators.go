package ai

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"bottrade/internal/indicators"
	"bottrade/internal/signals"
)

// KlineSource provides recent closing prices for a symbol/interval.
// *marketdata.BinanceProvider satisfies it via Closes.
type KlineSource interface {
	Closes(ctx context.Context, symbol, interval string, limit int) ([]float64, error)
}

// IndicatorProvider computes the classic technical indicators (EMA, RSI, MACD)
// from recent klines and renders them into the AI prompt under [technical]. Like
// the order-flow provider, it presents the numbers factually and lets the model
// weigh them.
type IndicatorProvider struct {
	source   KlineSource
	interval string
	limit    int
	name     string
}

// NewIndicatorProvider wraps a kline source. interval defaults to "1h"; limit (the
// number of candles fetched) defaults to 200, enough to seed MACD/EMA cleanly.
func NewIndicatorProvider(source KlineSource, interval string, limit int) *IndicatorProvider {
	if strings.TrimSpace(interval) == "" {
		interval = "1h"
	}
	if limit <= 0 {
		limit = 200
	}
	return &IndicatorProvider{source: source, interval: interval, limit: limit, name: "binance_ta"}
}

func (p *IndicatorProvider) Name() string { return p.name }

func (p *IndicatorProvider) Category() ContextCategory { return CategoryTechnical }

func (p *IndicatorProvider) Enrich(ctx context.Context, signal signals.MarketSignal) (ContextFragment, error) {
	symbol := strings.TrimSpace(signal.Symbol)
	if symbol == "" {
		return ContextFragment{}, fmt.Errorf("indicators: signal has no symbol")
	}

	closes, err := p.source.Closes(ctx, symbol, p.interval, p.limit)
	if err != nil {
		return ContextFragment{}, err
	}

	metrics := map[string]string{"interval": p.interval}
	var parts []string

	if ema, err := indicators.EMA(closes, 20); err == nil {
		metrics["ema_20"] = formatFloat(ema)
		parts = append(parts, "EMA20 "+metrics["ema_20"])
	}
	if ema, err := indicators.EMA(closes, 50); err == nil {
		metrics["ema_50"] = formatFloat(ema)
		parts = append(parts, "EMA50 "+metrics["ema_50"])
	}
	if rsi, err := indicators.RSI(closes, 14); err == nil {
		metrics["rsi_14"] = formatFloat(indicators.Round2(rsi))
		parts = append(parts, "RSI14 "+metrics["rsi_14"])
	}
	if macd, sig, hist, err := indicators.MACD(closes, 12, 26, 9); err == nil {
		metrics["macd"] = formatFloat(indicators.Round2(macd))
		metrics["macd_signal"] = formatFloat(indicators.Round2(sig))
		metrics["macd_histogram"] = formatFloat(indicators.Round2(hist))
		parts = append(parts, fmt.Sprintf("MACD %s (signal %s, hist %s)", metrics["macd"], metrics["macd_signal"], metrics["macd_histogram"]))
	}

	if len(parts) == 0 {
		return ContextFragment{}, fmt.Errorf("indicators: not enough klines to compute indicators for %s", symbol)
	}

	return ContextFragment{
		Provider: p.name,
		Category: CategoryTechnical,
		Summary:  fmt.Sprintf("%s %s indicators — %s.", symbol, p.interval, strings.Join(parts, ", ")),
		Metrics:  metrics,
	}, nil
}

// formatFloat renders an indicator value without trailing zeros or scientific
// notation, so the prompt stays readable.
func formatFloat(v float64) string {
	return strconv.FormatFloat(v, 'f', -1, 64)
}
