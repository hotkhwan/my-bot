package annybasic

import (
	"errors"
	"math"
)

const (
	cdcFastPeriod    = 12
	cdcSlowPeriod    = 26
	qqeRSIPeriod     = 14
	qqeSmoothPeriod  = 5
	qqeFactor        = 4.236
	execFastPeriod   = 5
	execSlowPeriod   = 13
	volatilityPeriod = 14
	volumePeriod     = 20
)

var errInsufficientData = errors.New("anny basic: insufficient indicator data")

type qqePoint struct {
	rsi    float64
	signal float64
}

func cdcZone(closes []float64) (CDCZone, bool) {
	fast, ok := emaSeries(closes, cdcFastPeriod)
	if !ok {
		return CDCNeutral, false
	}
	slow, ok := emaSeries(closes, cdcSlowPeriod)
	if !ok {
		return CDCNeutral, false
	}
	offset := len(fast) - len(slow)
	if fast[offset+len(slow)-1] > slow[len(slow)-1] {
		return CDCGreen, true
	}
	if fast[offset+len(slow)-1] < slow[len(slow)-1] {
		return CDCRed, true
	}
	return CDCNeutral, true
}

func qqe(closes []float64) (current, previous qqePoint, err error) {
	rsi, ok := rsiSeries(closes, qqeRSIPeriod)
	if !ok {
		return qqePoint{}, qqePoint{}, errInsufficientData
	}
	smoothed, ok := emaSeries(rsi, qqeSmoothPeriod)
	if !ok {
		return qqePoint{}, qqePoint{}, errInsufficientData
	}

	wilders := qqeRSIPeriod*2 - 1
	delta := make([]float64, len(smoothed))
	for i := 1; i < len(smoothed); i++ {
		delta[i] = math.Abs(smoothed[i] - smoothed[i-1])
	}
	atrRsi, ok := emaSeries(delta[1:], wilders)
	if !ok {
		return qqePoint{}, qqePoint{}, errInsufficientData
	}
	smoothedAtr, ok := emaSeries(atrRsi, wilders)
	if !ok {
		return qqePoint{}, qqePoint{}, errInsufficientData
	}

	offset := len(smoothed) - len(smoothedAtr)
	longBand := make([]float64, len(smoothedAtr))
	shortBand := make([]float64, len(smoothedAtr))
	trailing := make([]float64, len(smoothedAtr))
	trend := 1
	for i := range smoothedAtr {
		r := smoothed[offset+i]
		dar := smoothedAtr[i] * qqeFactor
		newLong := r - dar
		newShort := r + dar
		if i == 0 {
			longBand[i], shortBand[i], trailing[i] = newLong, newShort, newLong
			continue
		}
		prevR := smoothed[offset+i-1]
		if prevR > longBand[i-1] && r > longBand[i-1] {
			longBand[i] = math.Max(longBand[i-1], newLong)
		} else {
			longBand[i] = newLong
		}
		if prevR < shortBand[i-1] && r < shortBand[i-1] {
			shortBand[i] = math.Min(shortBand[i-1], newShort)
		} else {
			shortBand[i] = newShort
		}
		switch {
		case r > shortBand[i-1]:
			trend = 1
		case r < longBand[i-1]:
			trend = -1
		}
		if trend == 1 {
			trailing[i] = longBand[i]
		} else {
			trailing[i] = shortBand[i]
		}
	}
	if len(trailing) < 2 {
		return qqePoint{}, qqePoint{}, errInsufficientData
	}
	last := len(trailing) - 1
	return qqePoint{rsi: smoothed[offset+last], signal: trailing[last]},
		qqePoint{rsi: smoothed[offset+last-1], signal: trailing[last-1]}, nil
}

func emaSeries(values []float64, period int) ([]float64, bool) {
	if period <= 0 || len(values) < period {
		return nil, false
	}
	var sum float64
	for _, value := range values[:period] {
		sum += value
	}
	out := make([]float64, 0, len(values)-period+1)
	value := sum / float64(period)
	out = append(out, value)
	k := 2 / float64(period+1)
	for _, next := range values[period:] {
		value = next*k + value*(1-k)
		out = append(out, value)
	}
	return out, true
}

func rsiSeries(values []float64, period int) ([]float64, bool) {
	if period <= 0 || len(values) < period+1 {
		return nil, false
	}
	var gain, loss float64
	for i := 1; i <= period; i++ {
		change := values[i] - values[i-1]
		if change >= 0 {
			gain += change
		} else {
			loss -= change
		}
	}
	avgGain, avgLoss := gain/float64(period), loss/float64(period)
	out := []float64{rsiValue(avgGain, avgLoss)}
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
		out = append(out, rsiValue(avgGain, avgLoss))
	}
	return out, true
}

func rsiValue(gain, loss float64) float64 {
	if loss == 0 {
		if gain == 0 {
			return 50
		}
		return 100
	}
	return 100 - 100/(1+gain/loss)
}
