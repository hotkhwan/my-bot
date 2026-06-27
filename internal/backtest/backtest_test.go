package backtest

import (
	"math"
	"testing"
)

// constLongStrategy always wants to be long once it has any history.
type constLongStrategy struct{}

func (constLongStrategy) Name() string                { return "const_long" }
func (constLongStrategy) Evaluate(_ []float64) Signal { return Long }

func TestRunLongOnUptrendIsProfitable(t *testing.T) {
	// Steady uptrend: a permanently-long strategy should finish in profit.
	closes := make([]float64, 0, 50)
	for i := 0; i < 50; i++ {
		closes = append(closes, 100+float64(i))
	}
	result, err := Run(closes, constLongStrategy{}, Config{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.ReturnPct <= 0 {
		t.Fatalf("return = %.2f%%, want positive on an uptrend", result.ReturnPct)
	}
	if result.FinalEquity <= 1.0 {
		t.Fatalf("final equity = %.4f, want > 1", result.FinalEquity)
	}
}

func TestRunLongOnDowntrendLoses(t *testing.T) {
	closes := make([]float64, 0, 50)
	for i := 0; i < 50; i++ {
		closes = append(closes, 200-float64(i))
	}
	result, err := Run(closes, constLongStrategy{}, Config{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.ReturnPct >= 0 {
		t.Fatalf("return = %.2f%%, want negative being long into a downtrend", result.ReturnPct)
	}
	if result.MaxDrawdownPct <= 0 {
		t.Fatalf("max drawdown = %.2f%%, want positive", result.MaxDrawdownPct)
	}
}

func TestRunFeesReduceReturn(t *testing.T) {
	closes := []float64{100, 110, 100, 110, 100, 110}
	noFee, _ := Run(closes, constLongStrategy{}, Config{})
	withFee, _ := Run(closes, constLongStrategy{}, Config{FeeRate: 0.001})
	if withFee.ReturnPct >= noFee.ReturnPct {
		t.Fatalf("fees should reduce return: noFee=%.4f withFee=%.4f", noFee.ReturnPct, withFee.ReturnPct)
	}
}

func TestRunWinRateAndTradeCount(t *testing.T) {
	// EMA cross over an uptrend produces at least one trade and a sensible win rate.
	closes := make([]float64, 0, 80)
	for i := 0; i < 80; i++ {
		closes = append(closes, 100+float64(i))
	}
	result, err := Run(closes, EMACrossStrategy{Fast: 5, Slow: 20}, Config{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Trades == 0 {
		t.Fatal("expected at least one trade")
	}
	if result.WinRatePct < 0 || result.WinRatePct > 100 {
		t.Fatalf("win rate = %.2f, out of range", result.WinRatePct)
	}
	if result.Wins+result.Losses != result.Trades {
		t.Fatalf("wins+losses = %d, want %d", result.Wins+result.Losses, result.Trades)
	}
}

func TestRunRejectsBadInput(t *testing.T) {
	if _, err := Run([]float64{100}, constLongStrategy{}, Config{}); err == nil {
		t.Fatal("expected error for too few closes")
	}
	if _, err := Run([]float64{100, 101}, nil, Config{}); err == nil {
		t.Fatal("expected error for nil strategy")
	}
	if _, err := Run([]float64{100, 101}, constLongStrategy{}, Config{FeeRate: -1}); err == nil {
		t.Fatal("expected error for negative fee")
	}
}

func TestRSIReversionStrategySignals(t *testing.T) {
	// Pure uptrend -> RSI saturates high -> reversion strategy wants short.
	closes := make([]float64, 0, 40)
	for i := 0; i < 40; i++ {
		closes = append(closes, 100+float64(i))
	}
	if got := (RSIReversionStrategy{Period: 14, Low: 30, High: 70}).Evaluate(closes); got != Short {
		t.Fatalf("RSI reversion in an uptrend = %v, want Short", got)
	}
}

func TestEMACrossNeedsData(t *testing.T) {
	if got := (EMACrossStrategy{Fast: 5, Slow: 20}).Evaluate([]float64{1, 2, 3}); got != Flat {
		t.Fatalf("EMA cross with too little data = %v, want Flat", got)
	}
}

func TestReturnIsFinite(t *testing.T) {
	closes := []float64{100, 101, 102, 101, 100}
	result, _ := Run(closes, EMACrossStrategy{Fast: 2, Slow: 3}, Config{})
	if math.IsNaN(result.ReturnPct) || math.IsInf(result.ReturnPct, 0) {
		t.Fatalf("return is not finite: %v", result.ReturnPct)
	}
}
