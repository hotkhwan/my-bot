package annybasic

import (
	"math"
	"testing"
	"time"

	"bottrade/internal/marketdata"
)

func TestCDCZone(t *testing.T) {
	up := sequence(100, 1, 100)
	if got, ok := cdcZone(up); !ok || got != CDCGreen {
		t.Fatalf("uptrend CDC = %q, %v", got, ok)
	}
	down := sequence(200, -1, 100)
	if got, ok := cdcZone(down); !ok || got != CDCRed {
		t.Fatalf("downtrend CDC = %q, %v", got, ok)
	}
}

func TestQQECanonicalFixture(t *testing.T) {
	values := make([]float64, 140)
	for i := range values {
		values[i] = 100 + 0.08*float64(i) + 3*math.Sin(float64(i)*0.31)
	}
	current, previous, err := qqe(values)
	if err != nil {
		t.Fatalf("qqe: %v", err)
	}
	assertNear(t, current.rsi, 42.897065, 0.000001)
	assertNear(t, current.signal, 53.916597, 0.000001)
	assertNear(t, previous.rsi, 41.818538, 0.000001)
	assertNear(t, previous.signal, 53.916597, 0.000001)
}

func TestObserveAtUsesOnlyClosedMainCandles(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	main := candles(base, 15*time.Minute, sequence(100, 1, 120))
	exec := candles(base.Add(100*15*time.Minute), time.Minute, sequence(200, 0.2, 40))

	_, err := ObserveAt(main, exec, 20)
	if err != nil {
		t.Fatalf("ObserveAt: %v", err)
	}
	closed := closedBefore(main, exec[20].OpenTime)
	for _, candle := range closed {
		if candle.OpenTime.Add(15 * time.Minute).After(exec[20].OpenTime) {
			t.Fatal("adapter included an unclosed 15m candle")
		}
	}
}

func TestCDCTransitionFreshAllowsOneHourConfirmationWindow(t *testing.T) {
	values := make([]float64, 0, 180)
	for i := 0; i < 180; i++ {
		values = append(values, 100+0.03*float64(i)+6*math.Sin(float64(i)*0.18))
	}
	freshIndex := -1
	var zone CDCZone
	for i := 80; i < len(values); i++ {
		gotZone, ok := cdcZone(values[:i])
		if ok && cdcTransitionFresh(values[:i], gotZone, signalFreshMainBars) {
			freshIndex = i
			zone = gotZone
			break
		}
	}
	if freshIndex < 0 {
		t.Fatal("synthetic fixture did not produce a fresh CDC transition")
	}

	aged := append([]float64(nil), values[:freshIndex]...)
	last := aged[len(aged)-1]
	step := 0.15
	if zone == CDCRed {
		step = -0.15
	}
	for i := 0; i < signalFreshMainBars+2; i++ {
		last += step
		aged = append(aged, last)
	}
	agedZone, ok := cdcZone(aged)
	if !ok {
		t.Fatal("aged cdcZone not ready")
	}
	if cdcTransitionFresh(aged, agedZone, signalFreshMainBars) {
		t.Fatal("old transition should expire after the freshness window")
	}
}

func TestRecentQQECrossUsesFreshnessWindow(t *testing.T) {
	values := make([]float64, 0, 180)
	for i := 0; i < 180; i++ {
		values = append(values, 100+0.03*float64(i)+4*math.Sin(float64(i)*0.27))
	}
	crossIndex := -1
	var cross QQECross
	for i := 80; i < len(values); i++ {
		if got := recentQQECross(values[:i], 1); got != QQENone {
			crossIndex = i
			cross = got
			break
		}
	}
	if crossIndex < 0 {
		t.Fatal("synthetic fixture did not produce a QQE cross")
	}
	fresh := append([]float64(nil), values[:crossIndex]...)
	last := fresh[len(fresh)-1]
	for i := 0; i < signalFreshMainBars-1; i++ {
		last += 0.02
		fresh = append(fresh, last)
	}
	if got := recentQQECross(fresh, signalFreshMainBars); got != cross {
		t.Fatalf("fresh QQE cross = %q, want %q", got, cross)
	}
}

func sequence(start, step float64, count int) []float64 {
	out := make([]float64, count)
	for i := range out {
		out[i] = start + step*float64(i)
	}
	return out
}

func candles(start time.Time, interval time.Duration, closes []float64) []marketdata.Candle {
	out := make([]marketdata.Candle, len(closes))
	for i, close := range closes {
		out[i] = marketdata.Candle{
			OpenTime: start.Add(time.Duration(i) * interval),
			Open:     close - 0.1, High: close + 0.2, Low: close - 0.2,
			Close: close, Volume: 100 + float64(i),
		}
	}
	return out
}

func assertNear(t *testing.T, got, want, tolerance float64) {
	t.Helper()
	if math.Abs(got-want) > tolerance {
		t.Fatalf("got %.9f, want %.9f", got, want)
	}
}
