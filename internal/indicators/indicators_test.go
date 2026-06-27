package indicators

import (
	"errors"
	"math"
	"testing"
)

func approx(a, b, tol float64) bool { return math.Abs(a-b) <= tol }

func TestEMASeedsWithSMA(t *testing.T) {
	// First EMA value is the SMA of the first `period` points.
	values := []float64{1, 2, 3, 4, 5}
	series, err := EMASeries(values, 5)
	if err != nil {
		t.Fatalf("EMASeries: %v", err)
	}
	if len(series) != 1 || !approx(series[0], 3.0, 1e-9) {
		t.Fatalf("series = %v, want single value 3.0 (SMA)", series)
	}
}

func TestEMATracksRecentValues(t *testing.T) {
	values := []float64{10, 10, 10, 10, 10, 20}
	ema, err := EMA(values, 5)
	if err != nil {
		t.Fatalf("EMA: %v", err)
	}
	// Seed 10, then 20 with k=2/6: 20*0.3333 + 10*0.6667 = 13.333.
	if !approx(ema, 13.333, 1e-3) {
		t.Fatalf("EMA = %f, want ~13.333", ema)
	}
}

func TestRSIAllGainsIs100(t *testing.T) {
	values := []float64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15}
	rsi, err := RSI(values, 14)
	if err != nil {
		t.Fatalf("RSI: %v", err)
	}
	if !approx(rsi, 100, 1e-9) {
		t.Fatalf("RSI = %f, want 100 (no losses)", rsi)
	}
}

func TestRSIBalancedAround50(t *testing.T) {
	// Alternating +1/-1 changes give roughly balanced gains and losses.
	values := make([]float64, 0, 30)
	price := 100.0
	for i := 0; i < 30; i++ {
		if i%2 == 0 {
			price++
		} else {
			price--
		}
		values = append(values, price)
	}
	rsi, err := RSI(values, 14)
	if err != nil {
		t.Fatalf("RSI: %v", err)
	}
	if rsi < 40 || rsi > 60 {
		t.Fatalf("RSI = %f, want roughly balanced (40..60)", rsi)
	}
}

func TestMACDPositiveInUptrend(t *testing.T) {
	values := make([]float64, 0, 60)
	for i := 0; i < 60; i++ {
		values = append(values, 100+float64(i)) // steady uptrend
	}
	macd, signal, hist, err := MACD(values, 12, 26, 9)
	if err != nil {
		t.Fatalf("MACD: %v", err)
	}
	if macd <= 0 {
		t.Fatalf("MACD line = %f, want positive in an uptrend", macd)
	}
	if !approx(hist, macd-signal, 1e-9) {
		t.Fatalf("histogram = %f, want macd-signal = %f", hist, macd-signal)
	}
}

func TestInsufficientData(t *testing.T) {
	if _, err := EMA([]float64{1, 2}, 5); !errors.Is(err, ErrInsufficientData) {
		t.Fatalf("EMA short series err = %v, want ErrInsufficientData", err)
	}
	if _, err := RSI([]float64{1, 2, 3}, 14); !errors.Is(err, ErrInsufficientData) {
		t.Fatalf("RSI short series err = %v, want ErrInsufficientData", err)
	}
	if _, _, _, err := MACD(make([]float64, 10), 12, 26, 9); !errors.Is(err, ErrInsufficientData) {
		t.Fatalf("MACD short series err = %v, want ErrInsufficientData", err)
	}
}

func TestMACDRejectsBadPeriods(t *testing.T) {
	if _, _, _, err := MACD(make([]float64, 100), 26, 12, 9); err == nil {
		t.Fatal("expected error when fast >= slow")
	}
}
