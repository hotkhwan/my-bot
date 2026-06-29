package annybasic

import "bottrade/internal/decimal"

type RescueRequest struct {
	SetupValid       bool
	WickOnly         bool
	QQEReversed      bool
	AdditionsMade    int
	AmountUSDT       decimal.Decimal
	CapitalAfterUSDT decimal.Decimal
}

// AllowRescue validates model policy only. Exchange actions still require the
// normal confirmation, risk, margin-mode, and environment gates.
func AllowRescue(req RescueRequest) (bool, string) {
	switch {
	case !req.SetupValid || req.QQEReversed:
		return false, "setup is invalid"
	case !req.WickOnly:
		return false, "adverse move is not a wick"
	case req.AdditionsMade >= 2:
		return false, "rescue limit reached"
	case !req.AmountUSDT.IsPositive() || req.AmountUSDT.Cmp(decimal.NewFromInt(10)) > 0:
		return false, "rescue amount must be between 0 and 10 USDT"
	case req.CapitalAfterUSDT.Cmp(decimal.NewFromInt(20)) < 0:
		return false, "minimum 20 USDT reserve would be violated"
	default:
		return true, "rescue policy passed"
	}
}
