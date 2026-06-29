package annybasic

import (
	"math"
	"testing"
	"time"

	"bottrade/internal/marketdata"
)

func TestCDCZone(t *testing.T) {
	up := sequence(100, 1, 100)
	if got, ok := cdcZone(up); !ok || got != CDCGreen {
		t.Fatalf("uptrend CDC = %q, %v", got, ok)
	}
	down := sequence(200, -1, 100)
	if got, ok := cdcZone(down); !ok || got != CDCRed {
		t.Fatalf("downtrend CDC = %q, %v", got, ok)
	}
}

func TestQQECanonicalFixture(t *testing.T) {
	values := make([]float64, 140)
	for i := range values {
		values[i] = 100 + 0.08*float64(i) + 3*math.Sin(float64(i)*0.31)
	}
	current, previous, err := qqe(values)
	if err != nil {
		t.Fatalf("qqe: %v", err)
	}
	assertNear(t, current.rsi, 42.897065, 0.000001)
	assertNear(t, current.signal, 53.916597, 0.000001)
	assertNear(t, previous.rsi, 41.818538, 0.000001)
	assertNear(t, previous.signal, 53.916597, 0.000001)
}

func TestObserveAtUsesOnlyClosedMainCandles(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	main := candles(base, 15*time.Minute, sequence(100, 1, 120))
	exec := candles(base.Add(100*15*time.Minute), time.Minute, sequence(200, 0.2, 40))

	_, err := ObserveAt(main, exec, 20)
	if err != nil {
		t.Fatalf("ObserveAt: %v", err)
	}
	closed := closedBefore(main, exec[20].OpenTime)
	for _, candle := range closed {
		if candle.OpenTime.Add(15 * time.Minute).After(exec[20].OpenTime) {
			t.Fatal("adapter included an unclosed 15m candle")
		}
	}
}

func sequence(start, step float64, count int) []float64 {
	out := make([]float64, count)
	for i := range out {
		out[i] = start + step*float64(i)
	}
	return out
}

func candles(start time.Time, interval time.Duration, closes []float64) []marketdata.Candle {
	out := make([]marketdata.Candle, len(closes))
	for i, close := range closes {
		out[i] = marketdata.Candle{
			OpenTime: start.Add(time.Duration(i) * interval),
			Open:     close - 0.1, High: close + 0.2, Low: close - 0.2,
			Close: close, Volume: 100 + float64(i),
		}
	}
	return out
}

func assertNear(t *testing.T, got, want, tolerance float64) {
	t.Helper()
	if math.Abs(got-want) > tolerance {
		t.Fatalf("got %.9f, want %.9f", got, want)
	}
}
