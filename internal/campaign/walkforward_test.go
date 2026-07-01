package campaign

import (
	"testing"

	"bottrade/internal/decimal"
)

func TestSplitWalkForwardFoldsNoLeakage(t *testing.T) {
	candles := trendCandles(100, 0.001, 0.002, 0.002, 120)
	windows, err := SplitWalkForwardFolds(candles, PaperConfig{WarmupBars: 10}, WalkForwardConfig{FoldCount: 3})
	if err != nil {
		t.Fatalf("SplitWalkForwardFolds: %v", err)
	}
	if len(windows) != 3 {
		t.Fatalf("folds = %d, want 3", len(windows))
	}
	for i, window := range windows {
		if window.WarmupBars != 10 || window.TestStart != window.WarmupStart+10 {
			t.Fatalf("fold %d window = %+v, want 10 warmup bars before test", i+1, window)
		}
		if window.TestBars <= 0 {
			t.Fatalf("fold %d has no OOS test bars: %+v", i+1, window)
		}
		if i > 0 && window.WarmupStart < windows[i-1].End {
			t.Fatalf("fold %d warmup starts at %d, leaks previous fold ending at %d", i+1, window.WarmupStart, windows[i-1].End)
		}
	}
	if windows[1].WarmupStart != windows[0].End || windows[2].WarmupStart != windows[1].End {
		t.Fatalf("fold blocks should be consecutive with no cross-boundary warmup: %+v", windows)
	}
}

func TestPaperMaxDrawdownFromEquityCurve(t *testing.T) {
	trades := []PaperTrade{
		{RunningPnL: decimal.MustParse("5")},
		{RunningPnL: decimal.MustParse("2")},
		{RunningPnL: decimal.MustParse("7")},
		{RunningPnL: decimal.MustParse("-1")},
	}
	if got := maxDrawdownUSDT(trades); got.String() != "8" {
		t.Fatalf("max drawdown = %s, want 8", got)
	}
	upOnly := []PaperTrade{
		{RunningPnL: decimal.MustParse("1")},
		{RunningPnL: decimal.MustParse("3")},
		{RunningPnL: decimal.MustParse("4")},
	}
	if got := maxDrawdownUSDT(upOnly); !got.IsZero() {
		t.Fatalf("monotonic-up drawdown = %s, want 0", got)
	}
}

func TestRunPaperWalkForwardAppliesFeesInsideEachFold(t *testing.T) {
	goal, err := ParseGoal("goal profit 100 capital 100")
	if err != nil {
		t.Fatalf("ParseGoal: %v", err)
	}
	wf, err := RunPaperWalkForward(PaperConfig{
		Goal: goal, Symbol: "BTCUSDT", Strategy: "ema", WarmupBars: 30,
		StopLossPct: 0.01, EntryFeeRate: 0.0002, ExitFeeRate: 0.0004,
	}, trendCandles(100, 0.01, 0.012, 0.0005, 180), WalkForwardConfig{FoldCount: 2})
	if err != nil {
		t.Fatalf("RunPaperWalkForward: %v", err)
	}
	var sawTrade bool
	for _, fold := range wf.Folds {
		if len(fold.Result.Trades) == 0 {
			t.Fatalf("fold %d produced no trades", fold.Window.Index)
		}
		for _, trade := range fold.Result.Trades {
			sawTrade = true
			if trade.Outcome == "tp" && trade.PnLUSDT.Cmp(goal.RewardPerTradeUSDT) >= 0 {
				t.Fatalf("fold %d trade %+v did not include maker/taker fees; reward=%s", fold.Window.Index, trade, goal.RewardPerTradeUSDT)
			}
		}
	}
	if !sawTrade {
		t.Fatal("expected walk-forward trades")
	}
}

func TestRunPaperWalkForwardAggregateSumsPnLAndWorstDrawdown(t *testing.T) {
	goal, _ := ParseGoal("goal profit 100 capital 100")
	wf, err := RunPaperWalkForward(PaperConfig{
		Goal: goal, Symbol: "BTCUSDT", Strategy: "ema", WarmupBars: 30, StopLossPct: 0.01,
	}, trendCandles(100, 0.01, 0.012, 0.0005, 180), WalkForwardConfig{FoldCount: 2})
	if err != nil {
		t.Fatalf("RunPaperWalkForward: %v", err)
	}
	sum := decimal.Zero()
	worstDD := decimal.Zero()
	trades := 0
	for _, fold := range wf.Folds {
		sum = sum.Add(fold.Result.State.RealizedPnL)
		trades += fold.Result.State.TradesClosed
		if fold.Result.MaxDrawdownUSDT.Cmp(worstDD) > 0 {
			worstDD = fold.Result.MaxDrawdownUSDT
		}
	}
	if wf.Aggregate.State.RealizedPnL.Cmp(sum) != 0 {
		t.Fatalf("aggregate pnl = %s, want fold sum %s", wf.Aggregate.State.RealizedPnL, sum)
	}
	if wf.Aggregate.State.TradesClosed != trades {
		t.Fatalf("aggregate trades = %d, want %d", wf.Aggregate.State.TradesClosed, trades)
	}
	if wf.Aggregate.MaxDrawdownUSDT.Cmp(worstDD) != 0 {
		t.Fatalf("aggregate drawdown = %s, want worst fold %s", wf.Aggregate.MaxDrawdownUSDT, worstDD)
	}
}
