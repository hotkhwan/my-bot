package campaign

import (
	"fmt"
	"math"
	"strconv"

	"bottrade/internal/backtest"
	"bottrade/internal/decimal"
	"bottrade/internal/marketdata"
	"bottrade/internal/strategy/annybasic"
)

// Paper trading turns the synthetic /goal preview into a data-driven one: it
// runs the goal's strategy over REAL Binance OHLC candles and resolves each
// trade from real intrabar highs/lows (whether stop-loss or take-profit was hit
// first), booking PnL with the goal's own risk sizing. No exchange orders are
// ever placed — this is paper/simulation, safe by construction — but the price
// action and win/loss outcomes are real, so the stats answer "does this goal
// actually work?" rather than assuming a win-rate.

// PaperBias constrains which side the strategy may take, e.g. when an AI advisor
// suggests a directional lean for the run.
type PaperBias string

const (
	BiasBoth  PaperBias = "both"
	BiasLong  PaperBias = "long"
	BiasShort PaperBias = "short"
)

// PaperConfig configures one paper run. Zero values fall back to sensible
// defaults in RunPaper.
type PaperConfig struct {
	Goal        Goal
	Symbol      string
	Strategy    string    // "ema" or "rsi"; defaults to "ema"
	Bias        PaperBias // restrict direction (AI lean); defaults to both
	StopLossPct float64   // FIXED SL distance as fraction of entry; when >0 it
	// overrides the adaptive stop. Default 0 → ATR-adaptive (below).
	FeeRate float64 // per-side fee fraction; seeds EntryFee/ExitFee when those
	// are unset. Default 0.0004 (0.04% taker). Kept for back-compat.
	EntryFeeRate float64 // fee on entry; default 0.0002 (maker-first LIMIT).
	ExitFeeRate  float64 // fee on the exit fill; default = FeeRate (taker: SL/TP/close are MARKET).
	MaxHoldBars  int     // force-close after N bars if neither level hits; default 24
	WarmupBars   int     // bars before the first trade is allowed; default 30
	PlanBars     int     // only allow new entries in the final N bars; 0 = all bars
	// MainCandles supplies closed/closing 15m candles for ANNY Basic while the
	// candles argument to RunPaper remains its 1m execution series.
	MainCandles []marketdata.Candle
	// Adaptive stop: the stop distance is AtrStopMult × recent ATR% (clamped to
	// [MinStopPct, MaxStopPct]), so a 1% move on 1m and on 1d aren't treated the
	// same — a fixed % stop is pure noise on higher timeframes and stops out before
	// the target, which is what made every timeframe lose. Zero values default.
	AtrLookback int     // bars of volatility to average; default 14
	AtrStopMult float64 // stop = mult × ATR%; default 1.5
	MinStopPct  float64 // stop floor; default 0.005 (0.5%)
	MaxStopPct  float64 // stop cap; default 0.06 (6%)
}

// PaperTrade is one resolved paper trade.
type PaperTrade struct {
	Index      int             `json:"index"`
	Side       string          `json:"side"`
	EntryIndex int             `json:"entry_index"`
	EntryPrice float64         `json:"entry_price"`
	ExitPrice  float64         `json:"exit_price"`
	StopLoss   float64         `json:"stop_loss"`
	TakeProfit float64         `json:"take_profit"`
	Outcome    string          `json:"outcome"` // "tp", "sl", or "timeout"
	Win        bool            `json:"win"`
	PnLUSDT    decimal.Decimal `json:"pnl_usdt"`
	RunningPnL decimal.Decimal `json:"running_pnl"`
}

// PaperResult bundles the run for the dashboard.
type PaperResult struct {
	Goal       Goal         `json:"goal"`
	Symbol     string       `json:"symbol"`
	Strategy   string       `json:"strategy"`
	Bias       PaperBias    `json:"bias"`
	Bars       int          `json:"bars"`
	Trades     []PaperTrade `json:"trades"`
	State      State        `json:"state"`
	Verdict    Verdict      `json:"verdict"`
	Wins       int          `json:"wins"`
	Losses     int          `json:"losses"`
	WinRatePct float64      `json:"win_rate_pct"`
}

// StrategyFor maps a name to a backtest direction strategy used for paper runs.
// Unknown names fall back to EMA-cross.
func StrategyFor(name string) backtest.Strategy {
	switch name {
	case "rsi", "rsi_reversion":
		return backtest.RSIReversionStrategy{Period: 14, Low: 30, High: 70}
	case "macd":
		return backtest.MACDStrategy{Fast: 12, Slow: 26, Signal: 9}
	case "sma", "sma_cross":
		return backtest.SMACrossStrategy{Fast: 10, Slow: 30}
	case "breakout", "donchian":
		return backtest.BreakoutStrategy{Period: 20}
	case "auto", "mix", "ensemble":
		return backtest.DefaultEnsemble()
	default:
		return backtest.EMACrossStrategy{Fast: 12, Slow: 26}
	}
}

// RunPaper walks real candles with the goal's strategy and resolves each trade
// from real highs/lows. It applies the same Evaluate() stop rules as a live
// campaign (target reached / max drawdown / max trades), so the verdict and
// equity curve mirror how the goal would actually be pursued. It is
// deterministic given the candles, so it is fully testable offline.
func RunPaper(cfg PaperConfig, candles []marketdata.Candle) (PaperResult, error) {
	strat := StrategyFor(cfg.Strategy)
	strategyName := strat.Name()
	if cfg.Strategy == annybasic.ID {
		strategyName = annybasic.ID + "_v" + annybasic.Version
		if len(cfg.MainCandles) == 0 {
			return PaperResult{}, fmt.Errorf("paper: ANNY Basic requires 15m main candles")
		}
	}
	if cfg.StopLossPct < 0 {
		cfg.StopLossPct = 0 // negative is meaningless; fall through to adaptive
	}
	if cfg.AtrLookback <= 0 {
		cfg.AtrLookback = 14
	}
	if cfg.AtrStopMult <= 0 {
		cfg.AtrStopMult = 1.5
	}
	if cfg.MinStopPct <= 0 {
		cfg.MinStopPct = 0.005
	}
	if cfg.MaxStopPct <= 0 {
		cfg.MaxStopPct = 0.06
	}
	if cfg.FeeRate < 0 {
		cfg.FeeRate = 0
	} else if cfg.FeeRate == 0 {
		cfg.FeeRate = 0.0004
	}
	// Entry and exit are billed separately. Live execution attempts a post-only
	// maker entry first, while protective exits remain taker orders.
	if cfg.EntryFeeRate <= 0 {
		cfg.EntryFeeRate = 0.0002
	}
	if cfg.ExitFeeRate <= 0 {
		cfg.ExitFeeRate = cfg.FeeRate
	}
	if cfg.MaxHoldBars <= 0 {
		cfg.MaxHoldBars = 24
	}
	if cfg.WarmupBars <= 0 {
		cfg.WarmupBars = 30
	}
	if cfg.Bias == "" {
		cfg.Bias = BiasBoth
	}
	if len(candles) < cfg.WarmupBars+2 {
		return PaperResult{}, fmt.Errorf("paper: need at least %d candles, got %d", cfg.WarmupBars+2, len(candles))
	}

	// Reward:risk is fixed by the goal (reward 2% / risk 1% of capital = 2:1), so
	// the take-profit distance is that multiple of the stop distance. A TP hit
	// then books exactly RewardPerTradeUSDT and an SL hit -RiskPerTradeUSDT, which
	// keeps the goal's economics intact while real price decides the outcome.
	risk := floatOf(cfg.Goal.RiskPerTradeUSDT, 1)
	reward := floatOf(cfg.Goal.RewardPerTradeUSDT, 2)
	if !finite(risk) || !finite(reward) || risk <= 0 || reward <= 0 {
		return PaperResult{}, fmt.Errorf("paper: goal reward/risk sizing is invalid")
	}
	rr := reward / risk
	if rr <= 0 || !finite(rr) {
		rr = 2
	}

	result := PaperResult{
		Goal:     cfg.Goal,
		Symbol:   cfg.Symbol,
		Strategy: strategyName,
		Bias:     cfg.Bias,
		Bars:     len(candles),
		State:    State{Goal: cfg.Goal},
	}

	closes := make([]float64, len(candles))
	for i, c := range candles {
		closes[i] = c.Close
	}

	i := cfg.WarmupBars
	if cfg.PlanBars > 0 && len(candles)-cfg.PlanBars > i {
		i = len(candles) - cfg.PlanBars
	}
	consecutiveLosses := 0
	for i < len(candles)-1 {
		if v := Evaluate(result.State); v != Continue {
			result.Verdict = v
			return finalize(result), nil
		}

		side := ""
		if cfg.Strategy == annybasic.ID {
			observation, observeErr := annybasic.ObserveAt(cfg.MainCandles, candles, i)
			if observeErr != nil {
				i++
				continue
			}
			modelDecision := annybasic.Evaluate(observation, annybasic.State{
				TradesClosed:      result.State.TradesClosed,
				ConsecutiveLosses: consecutiveLosses,
				RealizedPnLUSDT:   result.State.RealizedPnL,
			}, 100)
			if modelDecision.Stop {
				result.Verdict = StopStrategyRule
				return finalize(result), nil
			}
			side = string(modelDecision.Side)
		} else {
			side = signalSide(strat.Evaluate(closes[:i+1]))
		}
		if side == "" || !biasAllows(cfg.Bias, side) {
			i++
			continue
		}

		entry := candles[i].Close
		// Per-trade stop distance, sized to this window's volatility. Notional is
		// then sized so a full stop-out still loses exactly one RiskPerTradeUSDT,
		// keeping the goal's $-economics fixed while the price brackets adapt.
		slPct := stopPct(cfg, candles, i)
		notional := risk / slPct
		if !finite(notional) || notional <= 0 {
			i++
			continue
		}
		feeCost := (cfg.EntryFeeRate + cfg.ExitFeeRate) * notional // entry + exit, billed per leg
		sl, tp := bracket(side, entry, slPct, rr)
		exitPrice, outcome, exitIdx := resolve(side, sl, tp, candles, i+1, cfg.MaxHoldBars)

		var pnl float64
		switch outcome {
		case "tp":
			pnl = reward - feeCost
		case "sl":
			pnl = -risk - feeCost
		default: // timeout: book the realized price move on the notional, but
			// clamp to the bracket economics so a timed-out trade can never report
			// a gain bigger than its take-profit or a loss bigger than its stop —
			// keeping the win-rate stat on the same basis as filled trades.
			move := (exitPrice - entry) / entry
			if side == "short" {
				move = -move
			}
			raw := move * notional
			if raw > reward {
				raw = reward
			} else if raw < -risk {
				raw = -risk
			}
			pnl = raw - feeCost
		}

		pnlDec := usdt(pnl)
		result.State.RealizedPnL = result.State.RealizedPnL.Add(pnlDec)
		result.State.TradesClosed++
		win := pnl > 0
		if win {
			result.Wins++
			consecutiveLosses = 0
		} else {
			result.Losses++
			consecutiveLosses++
		}
		result.Trades = append(result.Trades, PaperTrade{
			Index:      len(result.Trades) + 1,
			Side:       side,
			EntryIndex: i,
			EntryPrice: entry,
			ExitPrice:  exitPrice,
			StopLoss:   sl,
			TakeProfit: tp,
			Outcome:    outcome,
			Win:        win,
			PnLUSDT:    pnlDec,
			RunningPnL: result.State.RealizedPnL,
		})

		// Re-enter no earlier than the bar after this trade closed, so trades do
		// not overlap on the same candles.
		if exitIdx > i {
			i = exitIdx + 1
		} else {
			i++
		}
	}

	result.Verdict = Evaluate(result.State)
	return finalize(result), nil
}

// resolve scans forward from `from` for up to maxHold bars and reports whether
// the take-profit or stop-loss was hit first (stop-loss assumed first when a
// single candle straddles both, the pessimistic convention), or a timeout close
// at the last scanned candle's close. Returns the exit price, outcome, and the
// candle index it resolved on.
func resolve(side string, sl, tp float64, candles []marketdata.Candle, from, maxHold int) (float64, string, int) {
	last := from
	for j := from; j < len(candles) && j < from+maxHold; j++ {
		c := candles[j]
		last = j
		if side == "long" {
			if c.Low <= sl {
				return sl, "sl", j
			}
			if c.High >= tp {
				return tp, "tp", j
			}
		} else {
			if c.High >= sl {
				return sl, "sl", j
			}
			if c.Low <= tp {
				return tp, "tp", j
			}
		}
	}
	return candles[last].Close, "timeout", last
}

// stopPct returns the stop distance (fraction of entry) for the trade opening at
// candle idx. A fixed StopLossPct overrides; otherwise it is AtrStopMult × the
// recent ATR%, clamped to [MinStopPct, MaxStopPct]. This makes the stop track the
// timeframe's real volatility instead of a one-size 1% that whipsaws on 4h/1d.
func stopPct(cfg PaperConfig, candles []marketdata.Candle, idx int) float64 {
	if cfg.StopLossPct > 0 {
		return cfg.StopLossPct
	}
	v := atrPct(candles, idx, cfg.AtrLookback)
	sl := cfg.AtrStopMult * v
	if sl < cfg.MinStopPct {
		sl = cfg.MinStopPct
	}
	if sl > cfg.MaxStopPct {
		sl = cfg.MaxStopPct
	}
	return sl
}

// atrPct is the average true range over the last `lookback` bars up to idx,
// expressed as a fraction of price — a volatility estimate that includes gaps.
func atrPct(candles []marketdata.Candle, idx, lookback int) float64 {
	if lookback < 1 {
		lookback = 14
	}
	start := idx - lookback + 1
	if start < 1 {
		start = 1
	}
	var sum float64
	var n int
	for j := start; j <= idx && j < len(candles); j++ {
		c := candles[j]
		tr := c.High - c.Low
		prevClose := candles[j-1].Close
		if d := math.Abs(c.High - prevClose); d > tr {
			tr = d
		}
		if d := math.Abs(c.Low - prevClose); d > tr {
			tr = d
		}
		if c.Close > 0 {
			sum += tr / c.Close
			n++
		}
	}
	if n == 0 {
		return 0
	}
	return sum / float64(n)
}

// bracket returns the stop-loss and take-profit prices for a side, given the
// stop distance (fraction of entry) and the reward:risk multiple.
func bracket(side string, entry, slPct, rr float64) (sl, tp float64) {
	if side == "long" {
		return entry * (1 - slPct), entry * (1 + slPct*rr)
	}
	return entry * (1 + slPct), entry * (1 - slPct*rr)
}

func signalSide(s backtest.Signal) string {
	switch s {
	case backtest.Long:
		return "long"
	case backtest.Short:
		return "short"
	default:
		return ""
	}
}

func biasAllows(bias PaperBias, side string) bool {
	switch bias {
	case BiasLong:
		return side == "long"
	case BiasShort:
		return side == "short"
	default:
		return true
	}
}

func finalize(r PaperResult) PaperResult {
	if total := r.Wins + r.Losses; total > 0 {
		r.WinRatePct = float64(r.Wins) / float64(total) * 100
	}
	if r.Trades == nil {
		r.Trades = []PaperTrade{}
	}
	return r
}

// floatOf converts a decimal USDT amount to float64 for simulation math,
// falling back to `def` when the value is non-positive or unparseable.
func floatOf(d decimal.Decimal, def float64) float64 {
	if !d.IsPositive() {
		return def
	}
	f, err := strconv.ParseFloat(d.String(), 64)
	if err != nil || f <= 0 {
		return def
	}
	return f
}

// finite reports whether f is a usable real number (not NaN or ±Inf), guarding
// the simulation against degenerate user input flowing into PnL/JSON.
func finite(f float64) bool { return !math.IsNaN(f) && !math.IsInf(f, 0) }

// usdt converts a float PnL back to a 4dp decimal so it composes with the
// decimal-based goal state and stop-rule evaluation.
func usdt(f float64) decimal.Decimal {
	d, err := decimal.Parse(strconv.FormatFloat(f, 'f', 4, 64))
	if err != nil {
		return decimal.Zero()
	}
	return d
}
