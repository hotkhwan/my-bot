// Package indicators computes the classic technical indicators (EMA, RSI, MACD)
// the AI advisor reads alongside order-flow. These are advisory signals rendered
// into the LLM prompt as strings — never used to size or price an order — so the
// math runs in float64 for the exponential smoothing RSI/EMA need. Order-critical
// values stay in the exact decimal type elsewhere.
package indicators

import (
	"errors"
	"math"
)

// ErrInsufficientData is returned when a series is too short for the requested
// period.
var ErrInsufficientData = errors.New("indicators: insufficient data for period")

// EMASeries returns the exponential moving average at every point from the first
// fully-seeded index onward. The seed is the simple average of the first period
// values; the returned slice has len(values)-period+1 entries. It errors when the
// series is shorter than period.
func EMASeries(values []float64, period int) ([]float64, error) {
	if period <= 0 {
		return nil, errors.New("indicators: period must be positive")
	}
	if len(values) < period {
		return nil, ErrInsufficientData
	}

	k := 2.0 / float64(period+1)
	out := make([]float64, 0, len(values)-period+1)

	// Seed with the SMA of the first `period` values.
	sum := 0.0
	for _, v := range values[:period] {
		sum += v
	}
	ema := sum / float64(period)
	out = append(out, ema)

	for _, v := range values[period:] {
		ema = v*k + ema*(1-k)
		out = append(out, ema)
	}
	return out, nil
}

// EMA returns the latest EMA value for the series.
func EMA(values []float64, period int) (float64, error) {
	series, err := EMASeries(values, period)
	if err != nil {
		return 0, err
	}
	return series[len(series)-1], nil
}

// RSI returns the latest Wilder-smoothed Relative Strength Index (0..100) over
// period. It needs at least period+1 values (period price changes).
func RSI(values []float64, period int) (float64, error) {
	if period <= 0 {
		return 0, errors.New("indicators: period must be positive")
	}
	if len(values) < period+1 {
		return 0, ErrInsufficientData
	}

	// Initial average gain/loss over the first `period` changes.
	var gain, loss float64
	for i := 1; i <= period; i++ {
		change := values[i] - values[i-1]
		if change >= 0 {
			gain += change
		} else {
			loss -= change
		}
	}
	avgGain := gain / float64(period)
	avgLoss := loss / float64(period)

	// Wilder smoothing for the remaining changes.
	for i := period + 1; i < len(values); i++ {
		change := values[i] - values[i-1]
		g, l := 0.0, 0.0
		if change >= 0 {
			g = change
		} else {
			l = -change
		}
		avgGain = (avgGain*float64(period-1) + g) / float64(period)
		avgLoss = (avgLoss*float64(period-1) + l) / float64(period)
	}

	if avgLoss == 0 {
		return 100, nil // no losses over the window: maximally overbought
	}
	rs := avgGain / avgLoss
	return 100 - (100 / (1 + rs)), nil
}

// MACD returns the latest MACD line, signal line, and histogram for the standard
// (fast, slow, signal) periods. macd = EMA(fast) - EMA(slow); signal = EMA(signal)
// of the macd line; hist = macd - signal.
func MACD(values []float64, fast, slow, signalPeriod int) (macd, signal, histogram float64, err error) {
	if fast <= 0 || slow <= 0 || signalPeriod <= 0 {
		return 0, 0, 0, errors.New("indicators: periods must be positive")
	}
	if fast >= slow {
		return 0, 0, 0, errors.New("indicators: fast period must be less than slow")
	}
	if len(values) < slow+signalPeriod {
		return 0, 0, 0, ErrInsufficientData
	}

	fastSeries, err := EMASeries(values, fast)
	if err != nil {
		return 0, 0, 0, err
	}
	slowSeries, err := EMASeries(values, slow)
	if err != nil {
		return 0, 0, 0, err
	}

	// Align the two EMA series on their common tail (slow starts later).
	offset := len(fastSeries) - len(slowSeries)
	macdLine := make([]float64, len(slowSeries))
	for i := range slowSeries {
		macdLine[i] = fastSeries[offset+i] - slowSeries[i]
	}

	signalSeries, err := EMASeries(macdLine, signalPeriod)
	if err != nil {
		return 0, 0, 0, err
	}

	macd = macdLine[len(macdLine)-1]
	signal = signalSeries[len(signalSeries)-1]
	return macd, signal, macd - signal, nil
}

// round2 is a small helper for stable rendering in callers/tests.
func round2(v float64) float64 { return math.Round(v*100) / 100 }

// Round2 exposes 2-dp rounding for presentation.
func Round2(v float64) float64 { return round2(v) }
