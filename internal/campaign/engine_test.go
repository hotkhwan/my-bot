package campaign

import (
	"context"
	"testing"

	"bottrade/internal/decimal"
	"bottrade/internal/signals"
)

type stubSignals struct{}

func (stubSignals) Signal(context.Context, string) (signals.MarketSignal, error) {
	return signals.MarketSignal{Symbol: "BTCUSDT"}, nil
}

type stubAdvisor struct{ action signals.DecisionAction }

func (s stubAdvisor) Decide(context.Context, signals.MarketSignal) (signals.Decision, error) {
	return signals.Decision{Action: s.action, Side: "long"}, nil
}

type stubTrader struct {
	pnl   string
	calls int
}

func (s *stubTrader) Trade(context.Context, signals.Decision) (decimal.Decimal, error) {
	s.calls++
	return decimal.MustParse(s.pnl), nil
}

func campaignGoal(target, drawdown string, maxTrades int) Goal {
	return Goal{
		TargetProfitUSDT: dec(target),
		MaxDrawdownUSDT:  dec(drawdown),
		MaxTrades:        maxTrades,
	}
}

func runEngine(t *testing.T, cfg EngineConfig) (State, Verdict) {
	t.Helper()
	engine, err := NewEngine(cfg)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	state, verdict, err := engine.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return state, verdict
}

func TestEngineReachesTarget(t *testing.T) {
	trader := &stubTrader{pnl: "1"}
	state, verdict := runEngine(t, EngineConfig{
		Goal:    campaignGoal("5", "10", 100),
		Signals: stubSignals{},
		Advisor: stubAdvisor{action: signals.ActionOpen},
		Trader:  trader,
	})
	if verdict != StopTargetReached {
		t.Fatalf("verdict = %q, want target_reached", verdict)
	}
	if state.RealizedPnL.String() != "5" || state.TradesClosed != 5 || trader.calls != 5 {
		t.Fatalf("state = %+v (calls %d), want +5 in 5 trades", state, trader.calls)
	}
}

func TestEngineStopsOnDrawdown(t *testing.T) {
	state, verdict := runEngine(t, EngineConfig{
		Goal:    campaignGoal("10", "3", 100),
		Signals: stubSignals{},
		Advisor: stubAdvisor{action: signals.ActionOpen},
		Trader:  &stubTrader{pnl: "-1"},
	})
	if verdict != StopMaxDrawdown {
		t.Fatalf("verdict = %q, want max_drawdown", verdict)
	}
	if state.RealizedPnL.String() != "-3" || state.TradesClosed != 3 {
		t.Fatalf("state = %+v, want -3 in 3 trades", state)
	}
}

func TestEngineStopsAtMaxTrades(t *testing.T) {
	// Winners too small to ever reach the target before the trade cap.
	state, verdict := runEngine(t, EngineConfig{
		Goal:    campaignGoal("1000", "1000", 4),
		Signals: stubSignals{},
		Advisor: stubAdvisor{action: signals.ActionOpen},
		Trader:  &stubTrader{pnl: "1"},
	})
	if verdict != StopMaxTrades {
		t.Fatalf("verdict = %q, want max_trades", verdict)
	}
	if state.TradesClosed != 4 {
		t.Fatalf("trades = %d, want 4", state.TradesClosed)
	}
}

func TestEngineStopsWhenAdvisorStaysInCash(t *testing.T) {
	trader := &stubTrader{pnl: "1"}
	state, verdict := runEngine(t, EngineConfig{
		Goal:                campaignGoal("10", "10", 100),
		Signals:             stubSignals{},
		Advisor:             stubAdvisor{action: signals.ActionHold},
		Trader:              trader,
		MaxConsecutiveSkips: 3,
	})
	if verdict != Continue {
		t.Fatalf("verdict = %q, want continue (gave up in cash)", verdict)
	}
	if trader.calls != 0 || state.TradesClosed != 0 {
		t.Fatalf("expected no trades, got calls=%d trades=%d", trader.calls, state.TradesClosed)
	}
}

func TestNewEngineValidates(t *testing.T) {
	if _, err := NewEngine(EngineConfig{}); err == nil {
		t.Fatal("expected error for missing dependencies")
	}
}
