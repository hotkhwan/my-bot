package annybasic

import (
	"testing"

	"bottrade/internal/decimal"
)

func TestEvaluate(t *testing.T) {
	long := Observation{
		CDC15m: CDCGreen, QQEValue: 61, QQECross: QQECrossUp,
		ExecutionAligned: true, MomentumConfirmed: true,
	}
	tests := []struct {
		name         string
		obs          Observation
		state        State
		cap          int
		wantSide     Side
		wantPhase    Phase
		wantLeverage int
		wantStop     bool
	}{
		{"strong long respects platform cap", long, State{}, 20, SideLong, PhaseFast, 20, false},
		{"strong long model cap", long, State{}, 125, SideLong, PhaseFast, 100, false},
		{"defensive removes high leverage", long, State{TradesClosed: 10}, 100, SideLong, PhaseDefensive, 50, false},
		{"mismatched QQE stays out", Observation{CDC15m: CDCGreen, QQEValue: 49, QQECross: QQECrossDown, ExecutionAligned: true}, State{}, 20, SideNone, PhaseFast, 0, false},
		{"sideways stays out", Observation{Sideways: true}, State{}, 20, SideNone, PhaseFast, 0, false},
		{"target stops", long, State{RealizedPnLUSDT: decimal.NewFromInt(10)}, 20, SideNone, PhaseFast, 0, true},
		{"loss streak stops", long, State{ConsecutiveLosses: 2}, 20, SideNone, PhaseFast, 0, true},
		{"trade cap stops", long, State{TradesClosed: 15}, 20, SideNone, PhaseDefensive, 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Evaluate(tt.obs, tt.state, tt.cap)
			if got.Side != tt.wantSide || got.Phase != tt.wantPhase ||
				got.MaxLeverage != tt.wantLeverage || got.Stop != tt.wantStop {
				t.Fatalf("Evaluate() = %+v", got)
			}
		})
	}
}

func TestAllowRescue(t *testing.T) {
	valid := RescueRequest{
		SetupValid: true, WickOnly: true, AmountUSDT: dec(5), CapitalAfterUSDT: dec(20),
	}
	if ok, reason := AllowRescue(valid); !ok {
		t.Fatalf("valid rescue rejected: %s", reason)
	}
	tests := []RescueRequest{
		{WickOnly: true, AmountUSDT: dec(5), CapitalAfterUSDT: dec(50)},
		{SetupValid: true, AmountUSDT: dec(5), CapitalAfterUSDT: dec(50)},
		{SetupValid: true, WickOnly: true, QQEReversed: true, AmountUSDT: dec(5), CapitalAfterUSDT: dec(50)},
		{SetupValid: true, WickOnly: true, AdditionsMade: 2, AmountUSDT: dec(5), CapitalAfterUSDT: dec(50)},
		{SetupValid: true, WickOnly: true, AmountUSDT: dec(11), CapitalAfterUSDT: dec(50)},
		{SetupValid: true, WickOnly: true, AmountUSDT: dec(5), CapitalAfterUSDT: dec(19)},
	}
	for i, req := range tests {
		if ok, _ := AllowRescue(req); ok {
			t.Fatalf("invalid rescue %d accepted", i)
		}
	}
}

func dec(value int64) decimal.Decimal {
	return decimal.NewFromInt(value)
}
