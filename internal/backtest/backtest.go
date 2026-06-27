// Package backtest replays a strategy over a historical close-price series and
// reports win-rate, return, and drawdown. It is the offline counterpart to the
// live journal: a way to check whether a rule has any edge before risking money,
// without any network or exchange. Indicator-driven strategies (EMA cross, RSI
// reversion) ship here; the AI advisor is intentionally not backtested this way
// because that would need a live LLM call per bar.
package backtest

import (
	"errors"

	"bottrade/internal/indicators"
)

// Signal is a strategy's desired exposure for the next bar.
type Signal int

const (
	Flat Signal = iota
	Long
	Short
)

// Strategy decides exposure given the closing prices up to and including the
// current bar (history[len-1] is the latest). It returns Flat until it has
// enough data.
type Strategy interface {
	// Name labels the strategy in results.
	Name() string
	// Evaluate returns the desired position for the bar after history's last
	// close.
	Evaluate(history []float64) Signal
}

// Result is the outcome of a backtest run.
type Result struct {
	Strategy       string
	Bars           int
	Trades         int
	Wins           int
	Losses         int
	WinRatePct     float64
	ReturnPct      float64 // total return on starting equity, in percent
	MaxDrawdownPct float64 // worst peak-to-trough equity decline, in percent
	FinalEquity    float64 // starting equity is 1.0
}

// Config tunes the simulation.
type Config struct {
	// FeeRate is the per-side taker fee as a fraction (0.0004 = 0.04%). Charged on
	// entry and exit.
	FeeRate float64
}

// Run walks closes bar by bar, opening/closing a single full-equity position as
// the strategy's signal changes, and books the realized return at each exit. An
// open position at the end is closed at the final close. It errors on too few
// closes.
func Run(closes []float64, strategy Strategy, cfg Config) (Result, error) {
	if strategy == nil {
		return Result{}, errors.New("backtest: strategy is required")
	}
	if len(closes) < 2 {
		return Result{}, errors.New("backtest: need at least two closes")
	}
	if cfg.FeeRate < 0 {
		return Result{}, errors.New("backtest: fee rate must be non-negative")
	}

	result := Result{Strategy: strategy.Name(), Bars: len(closes)}
	equity := 1.0
	peak := 1.0

	position := Flat
	var entryPrice float64

	// close books the open position's return at price exitPrice.
	closePosition := func(exitPrice float64) {
		if position == Flat {
			return
		}
		ret := (exitPrice - entryPrice) / entryPrice
		if position == Short {
			ret = -ret
		}
		ret -= 2 * cfg.FeeRate // entry + exit fees
		equity *= 1 + ret

		result.Trades++
		if ret > 0 {
			result.Wins++
		} else {
			result.Losses++
		}
		if equity > peak {
			peak = equity
		}
		if dd := (peak - equity) / peak; dd > result.MaxDrawdownPct {
			result.MaxDrawdownPct = dd
		}
		position = Flat
	}

	// Decide on each bar using history up to that bar; act on the next bar's open
	// (approximated by the current close, since we only have closes).
	for i := 1; i < len(closes); i++ {
		want := strategy.Evaluate(closes[:i])
		price := closes[i]

		if want != position {
			closePosition(price)
			if want != Flat {
				position = want
				entryPrice = price
			}
		}

		// Track drawdown on the running mark-to-market equity too, so a deep
		// unrealized dip is not hidden until the exit.
		mark := equity
		if position != Flat {
			ret := (price - entryPrice) / entryPrice
			if position == Short {
				ret = -ret
			}
			mark = equity * (1 + ret)
		}
		if mark > peak {
			peak = mark
		}
		if dd := (peak - mark) / peak; dd > result.MaxDrawdownPct {
			result.MaxDrawdownPct = dd
		}
	}
	closePosition(closes[len(closes)-1])

	result.FinalEquity = equity
	result.ReturnPct = (equity - 1) * 100
	result.MaxDrawdownPct *= 100
	if result.Trades > 0 {
		result.WinRatePct = float64(result.Wins) / float64(result.Trades) * 100
	}
	return result, nil
}

// EMACrossStrategy goes long when the fast EMA is above the slow EMA and short
// when below.
type EMACrossStrategy struct {
	Fast int
	Slow int
}

func (s EMACrossStrategy) Name() string { return "ema_cross" }

func (s EMACrossStrategy) Evaluate(history []float64) Signal {
	fast, err1 := indicators.EMA(history, s.Fast)
	slow, err2 := indicators.EMA(history, s.Slow)
	if err1 != nil || err2 != nil {
		return Flat
	}
	switch {
	case fast > slow:
		return Long
	case fast < slow:
		return Short
	default:
		return Flat
	}
}

// RSIReversionStrategy goes long when RSI is oversold and short when overbought.
type RSIReversionStrategy struct {
	Period int
	Low    float64
	High   float64
}

func (s RSIReversionStrategy) Name() string { return "rsi_reversion" }

func (s RSIReversionStrategy) Evaluate(history []float64) Signal {
	rsi, err := indicators.RSI(history, s.Period)
	if err != nil {
		return Flat
	}
	switch {
	case rsi <= s.Low:
		return Long
	case rsi >= s.High:
		return Short
	default:
		return Flat
	}
}
