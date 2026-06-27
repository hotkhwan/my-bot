package campaign

import (
	"fmt"
	"strconv"
	"strings"

	"bottrade/internal/decimal"
)

// ParseGoal reads a goal from free text using the simple, user-facing model:
//
//	profit <usdt>    required — the target profit this round
//	capital <usdt>   optional — the amount allocated this round (default 100);
//	                 the base for sizing and for the risk percentage
//	risk <percent>   optional — max loss as a percent of capital before stopping
//	                 (default 30); "risk 50" stops once down 50% of capital
//	winrate <pct>    optional — assumed win-rate (default 55)
//	maxtrades <n>    optional — hard cap on trades (default 50)
//
// Per-trade reward and risk are derived from capital (2% reward, 1% risk at a
// 2:1 reward:risk) so the user only thinks in profit + risk%. Leverage is chosen
// by the AI per trade, bounded by the campaign's drawdown stop — it is not part
// of the goal math. A "<n>usdt" amount anywhere is taken as the target when no
// explicit "profit" keyword is given (so "ทำกำไร 10usdt" works).
func ParseGoal(text string) (Goal, error) {
	_, rest, _ := strings.Cut(strings.TrimSpace(text), " ")
	tokens := strings.Fields(rest)

	goal := Goal{AssumedWinRate: 55, MaxTrades: 50}
	var haveTarget, haveCapital bool
	riskPct := 30

	for i, tok := range tokens {
		key := strings.ToLower(strings.TrimRight(tok, ":"))
		next := ""
		if i+1 < len(tokens) {
			next = tokens[i+1]
		}
		switch key {
		case "profit", "target", "กำไร":
			if v, ok := parseUSDT(next); ok {
				goal.TargetProfitUSDT, haveTarget = v, true
			}
		case "capital", "ทุน":
			if v, ok := parseUSDT(next); ok {
				goal.CapitalUSDT, haveCapital = v, true
			}
		case "risk", "เสี่ยง":
			if n, err := strconv.Atoi(strings.TrimSuffix(next, "%")); err == nil && n > 0 {
				riskPct = n
			}
		case "winrate", "wr":
			if n, err := strconv.Atoi(strings.TrimSuffix(next, "%")); err == nil {
				goal.AssumedWinRate = n
			}
		case "maxtrades", "trades":
			if n, err := strconv.Atoi(next); err == nil {
				goal.MaxTrades = n
			}
		}
	}

	if !haveTarget {
		for _, tok := range tokens {
			if strings.HasSuffix(strings.ToLower(tok), "usdt") {
				if v, ok := parseUSDT(tok); ok {
					goal.TargetProfitUSDT, haveTarget = v, true
					break
				}
			}
		}
	}
	if !haveTarget || !goal.TargetProfitUSDT.IsPositive() {
		return Goal{}, fmt.Errorf("I need a target profit, e.g. \"profit 10\"")
	}
	if !haveCapital {
		goal.CapitalUSDT = decimal.NewFromInt(100)
	}

	// Derived, so the user only sets profit + risk%.
	goal.RewardPerTradeUSDT = percentOf(goal.CapitalUSDT, 2)
	goal.RiskPerTradeUSDT = percentOf(goal.CapitalUSDT, 1)
	goal.MaxDrawdownUSDT = percentOf(goal.CapitalUSDT, int64(riskPct))
	return goal, nil
}

// RiskPercent reports the goal's max-drawdown as a percent of capital, for
// display.
func (g Goal) RiskPercent() int {
	if !g.CapitalUSDT.IsPositive() || !g.MaxDrawdownUSDT.IsPositive() {
		return 0
	}
	pct, err := g.MaxDrawdownUSDT.Mul(decimal.NewFromInt(100)).QuoFloor(g.CapitalUSDT, 0)
	if err != nil {
		return 0
	}
	n, _ := strconv.Atoi(pct.String())
	return n
}

func parseUSDT(s string) (decimal.Decimal, bool) {
	s = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(s)), "usdt")
	s = strings.TrimSuffix(s, "$")
	v, err := decimal.Parse(s)
	if err != nil || v.Cmp(decimal.Zero()) < 0 {
		return decimal.Zero(), false
	}
	return v, true
}

func percentOf(amount decimal.Decimal, pct int64) decimal.Decimal {
	v, err := amount.Mul(decimal.NewFromInt(pct)).QuoFloor(decimal.NewFromInt(100), 8)
	if err != nil {
		return decimal.Zero()
	}
	return v
}
