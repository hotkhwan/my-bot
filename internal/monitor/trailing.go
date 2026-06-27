package monitor

// trailing.go holds the pure stop-loss trailing decision logic (no exchange
// calls); the polling runner and exchange wiring build on top of it.

import (
	"bottrade/internal/decimal"
	"bottrade/internal/domain"
)

// TrailPolicy configures a trailing stop, expressed in percent of the entry
// price so it scales across symbols.
type TrailPolicy struct {
	// ActivatePct: start trailing once unrealised profit reaches this percent of
	// entry (e.g. 1 = +1%). Before activation the stop is left untouched.
	ActivatePct decimal.Decimal
	// TrailGapPct: once active, keep the stop this percent of entry behind the
	// mark price.
	TrailGapPct decimal.Decimal
}

// Valid reports whether the policy can drive a trail (both gaps positive).
func (p TrailPolicy) Valid() bool {
	return p.ActivatePct.IsPositive() && p.TrailGapPct.IsPositive()
}

// ComputeStop returns the adjusted stop-loss for a position and whether it
// changed. Guarantees:
//   - never loosens the stop (longs only ratchet up, shorts only down);
//   - does nothing until unrealised profit reaches ActivatePct;
//   - once active, floors the stop at break-even (entry) so a winning trade
//     cannot turn into a loss.
func ComputeStop(side domain.PositionSide, entry, currentStop, mark decimal.Decimal, policy TrailPolicy) (decimal.Decimal, bool) {
	if !policy.Valid() {
		return currentStop, false
	}

	activate := percentOf(entry, policy.ActivatePct)
	gap := percentOf(entry, policy.TrailGapPct)

	switch side {
	case domain.PositionSideLong:
		if mark.Sub(entry).Cmp(activate) < 0 {
			return currentStop, false
		}
		candidate := mark.Sub(gap)
		if candidate.Cmp(entry) < 0 {
			candidate = entry // break-even floor
		}
		if candidate.Cmp(currentStop) > 0 {
			return candidate, true
		}
	case domain.PositionSideShort:
		if entry.Sub(mark).Cmp(activate) < 0 {
			return currentStop, false
		}
		candidate := mark.Add(gap)
		if candidate.Cmp(entry) > 0 {
			candidate = entry // break-even cap
		}
		if candidate.Cmp(currentStop) < 0 {
			return candidate, true
		}
	}
	return currentStop, false
}

func percentOf(value, pct decimal.Decimal) decimal.Decimal {
	result, err := value.Mul(pct).QuoFloor(decimal.NewFromInt(100), 8)
	if err != nil {
		return decimal.Zero()
	}
	return result
}
