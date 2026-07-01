package campaign

import (
	"fmt"
	"time"

	"bottrade/internal/marketdata"
)

// WalkForwardConfig controls rolling out-of-sample paper validation. FoldCount
// splits the candle series into N consecutive blocks; FoldBars can be used
// instead to request a fixed number of test bars per fold. Each block owns its
// warmup bars, so later folds never warm up on a previous fold's test window.
type WalkForwardConfig struct {
	FoldCount int
	FoldBars  int
}

// WalkForwardWindow describes one fold over the original candle slice. End is
// exclusive. Warmup is [WarmupStart, TestStart); test/OOS is [TestStart, End).
type WalkForwardWindow struct {
	Index       int
	WarmupStart int
	TestStart   int
	End         int
	WarmupBars  int
	TestBars    int
	StartTime   time.Time
	EndTime     time.Time
}

// WalkForwardFoldResult is one independent RunPaper invocation plus its source
// window. Result metrics are fee-adjusted because they come directly from
// RunPaper.
type WalkForwardFoldResult struct {
	Window WalkForwardWindow
	Result PaperResult
}

// WalkForwardResult contains per-fold evidence and an aggregate row. The
// aggregate sums net PnL/trade counts and reports the worst fold drawdown.
type WalkForwardResult struct {
	Folds     []WalkForwardFoldResult
	Aggregate PaperResult
}

const defaultWalkForwardFoldCount = 4

// SplitWalkForwardFolds returns consecutive folds that each include their own
// warmup before the OOS test window. Warmup never points into a previous fold's
// test bars, which is the leakage guard this validation depends on.
func SplitWalkForwardFolds(candles []marketdata.Candle, cfg PaperConfig, wf WalkForwardConfig) ([]WalkForwardWindow, error) {
	warmup := cfg.WarmupBars
	if warmup <= 0 {
		warmup = 30
	}
	minFoldBars := warmup + 2
	if len(candles) < minFoldBars {
		return nil, fmt.Errorf("walk-forward: need at least %d candles, got %d", minFoldBars, len(candles))
	}

	var windows []WalkForwardWindow
	if wf.FoldBars > 0 {
		blockBars := warmup + wf.FoldBars
		for start := 0; start+minFoldBars <= len(candles); {
			end := start + blockBars
			if end > len(candles) {
				end = len(candles)
			}
			windows = append(windows, newWalkForwardWindow(len(windows)+1, start, start+warmup, end, candles))
			start = end
		}
		if len(windows) == 0 {
			return nil, fmt.Errorf("walk-forward: no complete folds")
		}
		return windows, nil
	}

	foldCount := wf.FoldCount
	if foldCount <= 0 {
		foldCount = defaultWalkForwardFoldCount
	}
	if foldCount < 1 {
		return nil, fmt.Errorf("walk-forward: fold count must be positive")
	}
	if len(candles) < foldCount*minFoldBars {
		return nil, fmt.Errorf("walk-forward: need at least %d candles for %d folds with %d warmup bars, got %d",
			foldCount*minFoldBars, foldCount, warmup, len(candles))
	}

	base := len(candles) / foldCount
	remainder := len(candles) % foldCount
	start := 0
	for i := 0; i < foldCount; i++ {
		size := base
		if i < remainder {
			size++
		}
		end := start + size
		windows = append(windows, newWalkForwardWindow(i+1, start, start+warmup, end, candles))
		start = end
	}
	return windows, nil
}

// RunPaperWalkForward reuses RunPaper for each fold. It does not fit or tune any
// parameters; it only asks whether the same fixed rules hold up across
// consecutive windows.
func RunPaperWalkForward(cfg PaperConfig, candles []marketdata.Candle, wf WalkForwardConfig) (WalkForwardResult, error) {
	windows, err := SplitWalkForwardFolds(candles, cfg, wf)
	if err != nil {
		return WalkForwardResult{}, err
	}

	out := WalkForwardResult{Folds: make([]WalkForwardFoldResult, 0, len(windows))}
	for _, window := range windows {
		foldCfg := cfg
		foldCfg.WarmupBars = window.WarmupBars
		foldCfg.PlanBars = window.TestBars
		if len(cfg.MainCandles) > 0 {
			foldCfg.MainCandles = filterCandlesByTime(cfg.MainCandles, window.StartTime, window.EndTime)
		}
		result, err := RunPaper(foldCfg, candles[window.WarmupStart:window.End])
		if err != nil {
			return WalkForwardResult{}, fmt.Errorf("walk-forward fold %d: %w", window.Index, err)
		}
		out.Folds = append(out.Folds, WalkForwardFoldResult{Window: window, Result: result})
	}
	out.Aggregate = aggregateWalkForward(cfg, out.Folds)
	return out, nil
}

func newWalkForwardWindow(index, warmupStart, testStart, end int, candles []marketdata.Candle) WalkForwardWindow {
	return WalkForwardWindow{
		Index:       index,
		WarmupStart: warmupStart,
		TestStart:   testStart,
		End:         end,
		WarmupBars:  testStart - warmupStart,
		TestBars:    end - testStart,
		StartTime:   candles[warmupStart].OpenTime,
		EndTime:     candleWindowEnd(candles[warmupStart:end]),
	}
}

func aggregateWalkForward(cfg PaperConfig, folds []WalkForwardFoldResult) PaperResult {
	agg := PaperResult{
		Goal:     cfg.Goal,
		Symbol:   cfg.Symbol,
		Strategy: cfg.Strategy,
		Bias:     cfg.Bias,
		Trades:   []PaperTrade{},
		State:    State{Goal: cfg.Goal},
	}
	if agg.Bias == "" {
		agg.Bias = BiasBoth
	}
	for _, fold := range folds {
		r := fold.Result
		if agg.Symbol == "" {
			agg.Symbol = r.Symbol
		}
		if agg.Strategy == "" || agg.Strategy == cfg.Strategy {
			agg.Strategy = r.Strategy
		}
		agg.Bars += fold.Window.TestBars
		agg.Wins += r.Wins
		agg.Losses += r.Losses
		agg.State.TradesClosed += r.State.TradesClosed
		agg.State.RealizedPnL = agg.State.RealizedPnL.Add(r.State.RealizedPnL)
		agg.Diagnostics.ObservedBars += r.Diagnostics.ObservedBars
		agg.Diagnostics.SetupsFound += r.Diagnostics.SetupsFound
		agg.Diagnostics.BiasRejected += r.Diagnostics.BiasRejected
		for reason, count := range r.Diagnostics.Blocked {
			for i := 0; i < count; i++ {
				agg.Diagnostics.recordBlock(reason)
			}
		}
		if r.MaxDrawdownUSDT.Cmp(agg.MaxDrawdownUSDT) > 0 {
			agg.MaxDrawdownUSDT = r.MaxDrawdownUSDT
		}
	}
	if total := agg.Wins + agg.Losses; total > 0 {
		agg.WinRatePct = float64(agg.Wins) / float64(total) * 100
	}
	agg.Verdict = Evaluate(agg.State)
	agg.Diagnostics.finalize()
	return agg
}

func filterCandlesByTime(candles []marketdata.Candle, start, end time.Time) []marketdata.Candle {
	if start.IsZero() || end.IsZero() || !end.After(start) {
		return candles
	}
	out := make([]marketdata.Candle, 0, len(candles))
	for _, candle := range candles {
		if candle.OpenTime.Before(start) || !candle.OpenTime.Before(end) {
			continue
		}
		out = append(out, candle)
	}
	return out
}

func candleWindowEnd(candles []marketdata.Candle) time.Time {
	if len(candles) == 0 {
		return time.Time{}
	}
	last := candles[len(candles)-1].OpenTime
	if len(candles) == 1 {
		return last
	}
	step := last.Sub(candles[len(candles)-2].OpenTime)
	if step <= 0 {
		step = time.Nanosecond
	}
	return last.Add(step)
}
