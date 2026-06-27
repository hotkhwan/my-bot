package monitor

import (
	"testing"

	"bottrade/internal/decimal"
	"bottrade/internal/domain"
)

func policy() TrailPolicy {
	// activate at +1% of entry, trail 1% of entry behind the mark.
	return TrailPolicy{ActivatePct: decimal.MustParse("1"), TrailGapPct: decimal.MustParse("1")}
}

func dec(s string) decimal.Decimal { return decimal.MustParse(s) }

func TestComputeStopLong(t *testing.T) {
	p := policy()
	cases := []struct {
		name      string
		entry     string
		currentSL string
		mark      string
		wantMoved bool
		wantStop  string
	}{
		{"not yet profitable", "60000", "58000", "60300", false, "58000"},  // +0.5% < 1% activate
		{"activates to trail", "60000", "58000", "61000", true, "60400"},   // +1.67%, 61000-600
		{"floors at break-even", "60000", "58000", "60600", true, "60000"}, // 60600-600=60000 == entry
		{"ratchets up", "60000", "60400", "62000", true, "61400"},          // 62000-600
		{"never moves back", "60000", "61400", "61000", false, "61400"},    // candidate 60400 < current
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			stop, moved := ComputeStop(domain.PositionSideLong, dec(c.entry), dec(c.currentSL), dec(c.mark), p)
			if moved != c.wantMoved {
				t.Fatalf("moved = %v, want %v", moved, c.wantMoved)
			}
			if stop.String() != c.wantStop {
				t.Fatalf("stop = %s, want %s", stop.String(), c.wantStop)
			}
		})
	}
}

func TestComputeStopShort(t *testing.T) {
	p := policy()
	// short entry 60000: profit when price falls. activate at -1% (<=59400).
	cases := []struct {
		name      string
		currentSL string
		mark      string
		wantMoved bool
		wantStop  string
	}{
		{"not yet profitable", "62000", "59700", false, "62000"}, // -0.5% < 1%
		{"activates to trail", "62000", "59000", true, "59600"},  // 59000+600
		{"caps at break-even", "62000", "59400", true, "60000"},  // 59400+600=60000 == entry
		{"ratchets down", "59600", "58000", true, "58600"},       // 58000+600
		{"never moves back", "58600", "59000", false, "58600"},   // candidate 59600 > current
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			stop, moved := ComputeStop(domain.PositionSideShort, dec("60000"), dec(c.currentSL), dec(c.mark), p)
			if moved != c.wantMoved {
				t.Fatalf("moved = %v, want %v", moved, c.wantMoved)
			}
			if stop.String() != c.wantStop {
				t.Fatalf("stop = %s, want %s", stop.String(), c.wantStop)
			}
		})
	}
}

func TestComputeStopInvalidPolicy(t *testing.T) {
	zero := TrailPolicy{}
	stop, moved := ComputeStop(domain.PositionSideLong, dec("60000"), dec("58000"), dec("70000"), zero)
	if moved || stop.String() != "58000" {
		t.Fatalf("invalid policy should not move stop, got %s moved=%v", stop.String(), moved)
	}
}
