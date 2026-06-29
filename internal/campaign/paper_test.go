package campaign

import (
	"math"
	"testing"
	"time"

	"bottrade/internal/decimal"
	"bottrade/internal/marketdata"
)

// trendCandles builds `n` candles changing by stepPct per bar. highPad/lowPad
// widen each bar's range around the close so stop/target levels can be hit.
func trendCandles(start, stepPct, highPad, lowPad float64, n int) []marketdata.Candle {
	out := make([]marketdata.Candle, n)
	price := start
	base := time.Unix(0, 0).UTC()
	for i := 0; i < n; i++ {
		out[i] = marketdata.Candle{
			OpenTime: base.Add(time.Duration(i) * time.Hour),
			Open:     price,
			Close:    price,
			High:     price * (1 + highPad),
			Low:      price * (1 - lowPad),
			Volume:   1,
		}
		price *= 1 + stepPct
	}
	return out
}

func TestRunPaperUptrendReachesTarget(t *testing.T) {
	goal, err := ParseGoal("goal profit 5 capital 100")
	if err != nil {
		t.Fatalf("ParseGoal: %v", err)
	}
	// Steady +1%/bar uptrend: EMA fast>slow → Long; each long's +2% TP is hit
	// within a couple bars while the -1% stop never is.
	candles := trendCandles(100, 0.01, 0.012, 0.0005, 90)

	res, err := RunPaper(PaperConfig{Goal: goal, Symbol: "BTCUSDT", Strategy: "ema"}, candles)
	if err != nil {
		t.Fatalf("RunPaper: %v", err)
	}
	if len(res.Trades) == 0 {
		t.Fatal("expected trades, got none")
	}
	for _, tr := range res.Trades {
		if tr.Side != "long" {
			t.Fatalf("expected only long trades in an uptrend, got %s", tr.Side)
		}
	}
	if res.Wins == 0 || res.Losses != 0 {
		t.Fatalf("expected all wins in a clean uptrend: wins=%d losses=%d", res.Wins, res.Losses)
	}
	if !res.State.RealizedPnL.IsPositive() {
		t.Fatalf("expected positive PnL, got %s", res.State.RealizedPnL.String())
	}
	if res.Verdict != StopTargetReached {
		t.Fatalf("verdict = %q, want target reached", res.Verdict)
	}
	if res.WinRatePct != 100 {
		t.Fatalf("win rate = %.1f, want 100", res.WinRatePct)
	}
	// Running PnL on the last trade must equal final realized PnL.
	last := res.Trades[len(res.Trades)-1]
	if last.RunningPnL.Cmp(res.State.RealizedPnL) != 0 {
		t.Fatalf("running pnl %s != realized %s", last.RunningPnL, res.State.RealizedPnL)
	}
}

func TestRunPaperDowntrendShortsWin(t *testing.T) {
	goal, _ := ParseGoal("goal profit 5 capital 100")
	// Steady -1%/bar downtrend: EMA fast<slow → Short; each short's TP below
	// entry is hit while the stop above never is.
	candles := trendCandles(100, -0.01, 0.0005, 0.012, 90)

	res, err := RunPaper(PaperConfig{Goal: goal, Symbol: "BTCUSDT", Strategy: "ema"}, candles)
	if err != nil {
		t.Fatalf("RunPaper: %v", err)
	}
	if len(res.Trades) == 0 {
		t.Fatal("expected short trades, got none")
	}
	for _, tr := range res.Trades {
		if tr.Side != "short" {
			t.Fatalf("expected only short trades in a downtrend, got %s", tr.Side)
		}
	}
	if res.Wins == 0 || res.Losses != 0 {
		t.Fatalf("expected all winning shorts: wins=%d losses=%d", res.Wins, res.Losses)
	}
	if res.Verdict != StopTargetReached {
		t.Fatalf("verdict = %q, want target reached", res.Verdict)
	}
}

func TestAdaptiveStopScalesWithVolatility(t *testing.T) {
	volCandles := func(rangePct float64, n int) []marketdata.Candle {
		out := make([]marketdata.Candle, n)
		for i := range out {
			out[i] = marketdata.Candle{Open: 100, Close: 100, High: 100 * (1 + rangePct/2), Low: 100 * (1 - rangePct/2), Volume: 1}
		}
		return out
	}
	cfg := PaperConfig{AtrLookback: 14, AtrStopMult: 1.5, MinStopPct: 0.005, MaxStopPct: 0.06}
	calm := stopPct(cfg, volCandles(0.004, 30), 20) // 0.4% bars → ~0.6% stop
	wild := stopPct(cfg, volCandles(0.02, 30), 20)  // 2% bars → ~3% stop
	if !(wild > calm) {
		t.Fatalf("stop should widen with volatility: wild=%.4f calm=%.4f", wild, calm)
	}
	if calm < cfg.MinStopPct || wild > cfg.MaxStopPct {
		t.Fatalf("stops out of clamp: calm=%.4f wild=%.4f", calm, wild)
	}
	// A fixed override ignores volatility.
	if got := stopPct(PaperConfig{StopLossPct: 0.01}, volCandles(0.05, 30), 20); got != 0.01 {
		t.Fatalf("fixed override = %.4f, want 0.01", got)
	}
}

func TestRunPaperTimeoutBooksClampedLoss(t *testing.T) {
	goal, _ := ParseGoal("goal profit 50 capital 100")
	// Rise to establish an EMA long, then go flat: long entries time out. A timeout
	// books the realized move clamped to the bracket — never a gain beyond reward
	// (2) nor a loss beyond risk (1) plus the round-trip fee — which is the
	// invariant this test guards (it stays true under the adaptive stop).
	candles := make([]marketdata.Candle, 0, 90)
	price := 100.0
	for i := 0; i < 50; i++ {
		candles = append(candles, marketdata.Candle{Open: price, Close: price, High: price * 1.012, Low: price * 0.9995, Volume: 1})
		price *= 1.01
	}
	for i := 0; i < 40; i++ {
		candles = append(candles, marketdata.Candle{Open: price, Close: price, High: price * 1.0001, Low: price * 0.9999, Volume: 1})
	}

	res, err := RunPaper(PaperConfig{Goal: goal, Symbol: "BTCUSDT", Strategy: "ema"}, candles)
	if err != nil {
		t.Fatalf("RunPaper: %v", err)
	}
	maxGain, _ := decimal.Parse("2")    // = reward
	maxLoss, _ := decimal.Parse("-1.2") // = -risk minus a generous fee allowance
	var sawTimeout bool
	for _, tr := range res.Trades {
		if tr.Outcome == "timeout" {
			sawTimeout = true
			if tr.PnLUSDT.Cmp(maxGain) > 0 {
				t.Fatalf("timeout gain exceeds reward (clamp broken): %s", tr.PnLUSDT)
			}
			if tr.PnLUSDT.Cmp(maxLoss) < 0 {
				t.Fatalf("timeout loss exceeds risk+fee (clamp broken): %s", tr.PnLUSDT)
			}
		}
	}
	if !sawTimeout {
		t.Fatal("expected at least one timeout trade")
	}
}

func TestRunPaperRSIStrategyRuns(t *testing.T) {
	goal, _ := ParseGoal("goal profit 5 capital 100")
	res, err := RunPaper(PaperConfig{Goal: goal, Symbol: "BTCUSDT", Strategy: "rsi"}, trendCandles(100, -0.01, 0.0005, 0.012, 90))
	if err != nil {
		t.Fatalf("RunPaper(rsi): %v", err)
	}
	if res.Strategy != "rsi_reversion" {
		t.Fatalf("strategy = %q, want rsi_reversion", res.Strategy)
	}
}

func TestStrategyForNames(t *testing.T) {
	cases := map[string]string{
		"ema": "ema_cross", "rsi": "rsi_reversion", "macd": "macd",
		"sma": "sma_cross", "breakout": "breakout", "unknown": "ema_cross",
	}
	for in, want := range cases {
		if got := StrategyFor(in).Name(); got != want {
			t.Errorf("StrategyFor(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRunPaperNewStrategiesRun(t *testing.T) {
	goal, _ := ParseGoal("goal profit 5 capital 100")
	candles := trendCandles(100, 0.01, 0.012, 0.0005, 120)
	for _, strat := range []string{"macd", "sma", "breakout"} {
		res, err := RunPaper(PaperConfig{Goal: goal, Symbol: "BTCUSDT", Strategy: strat}, candles)
		if err != nil {
			t.Fatalf("RunPaper(%s): %v", strat, err)
		}
		if len(res.Trades) == 0 {
			t.Fatalf("%s produced no trades on an uptrend", strat)
		}
	}
}

func TestRunPaperDegenerateGoalUsesSafeDefaults(t *testing.T) {
	// A goal missing its derived reward/risk (zero) must not panic or emit NaN;
	// the engine falls back to safe defaults and still produces finite PnL.
	bare := Goal{CapitalUSDT: decimal.NewFromInt(100), TargetProfitUSDT: decimal.NewFromInt(5)}
	res, err := RunPaper(PaperConfig{Goal: bare, Symbol: "BTCUSDT"}, trendCandles(100, 0.01, 0.012, 0.0005, 90))
	if err != nil {
		t.Fatalf("RunPaper with bare goal: %v", err)
	}
	for _, tr := range res.Trades {
		if _, perr := decimal.Parse(tr.PnLUSDT.String()); perr != nil {
			t.Fatalf("non-finite PnL leaked: %q", tr.PnLUSDT.String())
		}
	}
}

func TestRunPaperBiasFiltersDirection(t *testing.T) {
	goal, _ := ParseGoal("goal profit 5 capital 100")
	// Uptrend yields long signals; a short-only bias must trade nothing.
	candles := trendCandles(100, 0.01, 0.012, 0.0005, 90)

	res, err := RunPaper(PaperConfig{Goal: goal, Symbol: "BTCUSDT", Strategy: "ema", Bias: BiasShort}, candles)
	if err != nil {
		t.Fatalf("RunPaper: %v", err)
	}
	if len(res.Trades) != 0 {
		t.Fatalf("short-only bias in an uptrend should make no trades, got %d", len(res.Trades))
	}
}

func TestRunPaperNeedsEnoughCandles(t *testing.T) {
	goal, _ := ParseGoal("goal profit 5 capital 100")
	if _, err := RunPaper(PaperConfig{Goal: goal, Symbol: "BTCUSDT"}, trendCandles(100, 0.01, 0.01, 0.01, 5)); err == nil {
		t.Fatal("expected error for too few candles")
	}
}

func TestRunPaperANNYBasicUsesDualTimeframes(t *testing.T) {
	goal, _ := ParseGoal("goal profit 5 capital 100")
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	main := make([]marketdata.Candle, 180)
	for i := range main {
		close := 100 + 0.04*float64(i) + 4*math.Sin(float64(i)*0.24)
		main[i] = marketdata.Candle{
			OpenTime: base.Add(time.Duration(i) * 15 * time.Minute),
			Open:     close - 0.1, High: close + 0.3, Low: close - 0.3,
			Close: close, Volume: 100 + float64(i%10),
		}
	}
	execStart := base.Add(130 * 15 * time.Minute)
	exec := make([]marketdata.Candle, 300)
	for i := range exec {
		close := 105 + 0.01*float64(i) + math.Sin(float64(i)*0.12)
		exec[i] = marketdata.Candle{
			OpenTime: execStart.Add(time.Duration(i) * time.Minute),
			Open:     close - 0.05, High: close + 0.2, Low: close - 0.2,
			Close: close, Volume: 100 + float64(i%25),
		}
	}

	res, err := RunPaper(PaperConfig{
		Goal: goal, Symbol: "BTCUSDT", Strategy: "anny_basic",
		MainCandles: main, PlanBars: 120,
	}, exec)
	if err != nil {
		t.Fatalf("RunPaper(anny_basic): %v", err)
	}
	if res.Strategy != "anny_basic_v1.2" {
		t.Fatalf("strategy = %q", res.Strategy)
	}
	if res.Bars != len(exec) {
		t.Fatalf("bars = %d, want %d execution bars", res.Bars, len(exec))
	}
}

func TestRunPaperANNYBasicRequiresMainCandles(t *testing.T) {
	goal, _ := ParseGoal("goal profit 5 capital 100")
	_, err := RunPaper(PaperConfig{
		Goal: goal, Symbol: "BTCUSDT", Strategy: "anny_basic",
	}, trendCandles(100, 0.01, 0.01, 0.01, 90))
	if err == nil {
		t.Fatal("expected missing 15m candles error")
	}
}

func TestResolveStopBeforeTarget(t *testing.T) {
	// A long with SL 99 / TP 102: a candle that straddles both resolves as a
	// stop (pessimistic convention).
	candles := []marketdata.Candle{
		{Close: 100},
		{High: 103, Low: 98, Close: 100}, // hits both → stop wins
	}
	price, outcome, idx := resolve("long", 99, 102, candles, 1, 24)
	if outcome != "sl" || price != 99 || idx != 1 {
		t.Fatalf("resolve = %v %s %d, want 99 sl 1", price, outcome, idx)
	}
}

func TestBracketDirections(t *testing.T) {
	sl, tp := bracket("long", 100, 0.01, 2)
	if sl != 99 || tp != 102 {
		t.Fatalf("long bracket = %v/%v, want 99/102", sl, tp)
	}
	sl, tp = bracket("short", 100, 0.01, 2)
	if sl != 101 || tp != 98 {
		t.Fatalf("short bracket = %v/%v, want 101/98", sl, tp)
	}
}
