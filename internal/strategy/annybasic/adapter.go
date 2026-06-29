package annybasic

import (
	"fmt"
	"math"
	"time"

	"bottrade/internal/marketdata"
)

const mainInterval = 15 * time.Minute

// ObserveAt builds a closed-candle 15m CDC/QQE observation and confirms it
// against 1m execution candles through executionIndex.
func ObserveAt(main15m, execution1m []marketdata.Candle, executionIndex int) (Observation, error) {
	if executionIndex < 0 || executionIndex >= len(execution1m) {
		return Observation{}, fmt.Errorf("anny basic: execution index out of range")
	}
	at := execution1m[executionIndex].OpenTime
	main := closedBefore(main15m, at)
	if len(main) == 0 {
		return Observation{}, errInsufficientData
	}
	mainCloses := candleCloses(main)
	zone, ok := cdcZone(mainCloses)
	if !ok {
		return Observation{}, errInsufficientData
	}
	previousZone, ok := cdcZone(mainCloses[:len(mainCloses)-1])
	if !ok {
		return Observation{}, errInsufficientData
	}
	if previousZone == zone {
		zone = CDCNeutral
	}
	currentQQE, previousQQE, err := qqe(mainCloses)
	if err != nil {
		return Observation{}, err
	}
	cross := QQENone
	switch {
	case previousQQE.rsi <= previousQQE.signal && currentQQE.rsi > currentQQE.signal:
		cross = QQECrossUp
	case previousQQE.rsi >= previousQQE.signal && currentQQE.rsi < currentQQE.signal:
		cross = QQECrossDown
	}
	// A closed 15m signal is eligible only on the first 1m candle after that
	// close. This prevents re-entering several times from one crossover.
	if !at.Equal(main[len(main)-1].OpenTime.Add(mainInterval)) {
		cross = QQENone
	}

	exec := execution1m[:executionIndex+1]
	execCloses := candleCloses(exec)
	fast, fastOK := emaSeries(execCloses, execFastPeriod)
	slow, slowOK := emaSeries(execCloses, execSlowPeriod)
	if !fastOK || !slowOK || len(exec) < volumePeriod+1 {
		return Observation{}, errInsufficientData
	}
	aligned := (zone == CDCGreen && fast[len(fast)-1] > slow[len(slow)-1]) ||
		(zone == CDCRed && fast[len(fast)-1] < slow[len(slow)-1])

	atr := averageTrueRange(exec, executionIndex, volatilityPeriod)
	last := exec[executionIndex]
	avgVolume := averageVolume(exec, executionIndex, volumePeriod)
	body := math.Abs(last.Close - last.Open)
	mainFast, _ := emaSeries(mainCloses, cdcFastPeriod)
	extended := atr > 0 && math.Abs(last.Close-mainFast[len(mainFast)-1]) > 1.5*atr
	abnormal := atr > 0 && trueRange(exec, executionIndex) > 3*atr
	sideways := last.Close > 0 && cdcSpread(mainCloses)/last.Close < 0.001

	return Observation{
		CDC15m:             zone,
		QQEValue:           currentQQE.rsi,
		QQECross:           cross,
		ExecutionAligned:   aligned,
		MomentumConfirmed:  avgVolume > 0 && last.Volume > avgVolume && body >= atr,
		EntryExtended:      extended,
		AbnormalVolatility: abnormal,
		Sideways:           sideways,
	}, nil
}

func closedBefore(candles []marketdata.Candle, at time.Time) []marketdata.Candle {
	end := 0
	for i, candle := range candles {
		if candle.OpenTime.Add(mainInterval).After(at) {
			break
		}
		end = i + 1
	}
	return candles[:end]
}

func candleCloses(candles []marketdata.Candle) []float64 {
	out := make([]float64, len(candles))
	for i, candle := range candles {
		out[i] = candle.Close
	}
	return out
}

func cdcSpread(closes []float64) float64 {
	fast, _ := emaSeries(closes, cdcFastPeriod)
	slow, _ := emaSeries(closes, cdcSlowPeriod)
	return math.Abs(fast[len(fast)-1] - slow[len(slow)-1])
}

func averageTrueRange(candles []marketdata.Candle, index, period int) float64 {
	start := index - period + 1
	if start < 1 {
		start = 1
	}
	var total float64
	var count int
	for i := start; i <= index; i++ {
		total += trueRange(candles, i)
		count++
	}
	if count == 0 {
		return 0
	}
	return total / float64(count)
}

func trueRange(candles []marketdata.Candle, index int) float64 {
	value := candles[index].High - candles[index].Low
	if index == 0 {
		return value
	}
	value = math.Max(value, math.Abs(candles[index].High-candles[index-1].Close))
	return math.Max(value, math.Abs(candles[index].Low-candles[index-1].Close))
}

func averageVolume(candles []marketdata.Candle, index, period int) float64 {
	start := index - period
	if start < 0 {
		start = 0
	}
	if start == index {
		return 0
	}
	var total float64
	for _, candle := range candles[start:index] {
		total += candle.Volume
	}
	return total / float64(index-start)
}
