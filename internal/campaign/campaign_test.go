package campaign

import (
	"testing"

	"bottrade/internal/decimal"
)

func dec(s string) decimal.Decimal { return decimal.MustParse(s) }

func goal(target string, winRate int, reward, risk string) Goal {
	return Goal{
		CapitalUSDT:        dec("100"),
		TargetProfitUSDT:   dec(target),
		RewardPerTradeUSDT: dec(reward),
		RiskPerTradeUSDT:   dec(risk),
		AssumedWinRate:     winRate,
	}
}

func TestEstimateTrades(t *testing.T) {
	cases := []struct {
		name string
		goal Goal
		want int
	}{
		// the user's example: 70% win, +1/-1, target 10 -> expectancy 0.4 -> 25 trades.
		{"70/30 example", goal("10", 70, "1", "1"), 25},
		{"60/40", goal("10", 60, "1", "1"), 50},       // expectancy 0.2 -> 50
		{"rounds up", goal("10", 70, "1.1", "1"), 22}, // expectancy 0.47 -> ceil(10/0.47)=22
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := EstimateTrades(c.goal)
			if err != nil {
				t.Fatalf("EstimateTrades: %v", err)
			}
			if got != c.want {
				t.Fatalf("trades = %d, want %d (expectancy %s)", got, c.want, c.goal.ExpectedPerTrade().String())
			}
		})
	}
}

func TestEstimateTradesRejectsNoEdge(t *testing.T) {
	// 50/50 with symmetric reward/risk -> zero expectancy -> infeasible.
	if _, err := EstimateTrades(goal("10", 50, "1", "1")); err == nil {
		t.Fatal("expected error for zero-expectancy strategy")
	}
	// losing system -> negative expectancy -> infeasible.
	if _, err := EstimateTrades(goal("10", 40, "1", "1")); err == nil {
		t.Fatal("expected error for negative-expectancy strategy")
	}
}

func TestEvaluate(t *testing.T) {
	base := Goal{
		TargetProfitUSDT: dec("10"),
		MaxDrawdownUSDT:  dec("5"),
		MaxTrades:        25,
	}
	cases := []struct {
		name   string
		pnl    string
		trades int
		want   Verdict
	}{
		{"still going", "4", 10, Continue},
		{"target hit", "10", 12, StopTargetReached},
		{"target exceeded", "11", 12, StopTargetReached},
		{"drawdown hit", "-5", 8, StopMaxDrawdown},
		{"drawdown exceeded", "-6", 8, StopMaxDrawdown},
		{"max trades", "3", 25, StopMaxTrades},
		// target takes priority over a same-step drawdown breach
		{"target beats trades cap", "10", 25, StopTargetReached},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Evaluate(State{Goal: base, RealizedPnL: dec(c.pnl), TradesClosed: c.trades})
			if got != c.want {
				t.Fatalf("verdict = %q, want %q", got, c.want)
			}
		})
	}
}
