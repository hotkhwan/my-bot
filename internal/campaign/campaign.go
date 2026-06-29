// Package campaign turns a natural-language profit goal into a bounded sequence
// of trades and decides, after each trade, whether to keep going. It owns the
// planning math (how many trades a target needs given an assumed edge) and the
// stop rules (target reached / max drawdown / max trades). Execution — calling
// the ensemble, placing orders, recording to the journal, trailing stops — wires
// these decisions to the other packages. See AI_TRADING_SYSTEM.md (Phase 4).
package campaign

import (
	"fmt"
	"strconv"

	"bottrade/internal/decimal"
)

// Goal is what the user asked for ("100 USDT capital, make 10 USDT") plus the
// risk assumptions used to plan and to stop.
type Goal struct {
	CapitalUSDT        decimal.Decimal
	TargetProfitUSDT   decimal.Decimal
	RewardPerTradeUSDT decimal.Decimal // intended gain per winning trade
	RiskPerTradeUSDT   decimal.Decimal // amount risked per losing trade
	AssumedWinRate     int             // percent, 0..100 — a hypothesis, measured by the journal
	MaxTrades          int             // hard cap on trades attempted
	MaxDrawdownUSDT    decimal.Decimal // stop if cumulative loss reaches this (capital preservation)
}

// ExpectedPerTrade is the expected value of one trade under the assumed win-rate:
// winRate*reward - (1-winRate)*risk.
func (g Goal) ExpectedPerTrade() decimal.Decimal {
	win := decimal.NewFromInt(int64(g.AssumedWinRate))
	lose := decimal.NewFromInt(int64(100 - g.AssumedWinRate))
	gross := g.RewardPerTradeUSDT.Mul(win).Sub(g.RiskPerTradeUSDT.Mul(lose))
	value, err := gross.QuoFloor(decimal.NewFromInt(100), 8)
	if err != nil {
		return decimal.Zero()
	}
	return value
}

// EstimateTrades returns how many trades the target needs given the assumed
// edge. It errors when the strategy has no positive expectancy — there is no
// trade count that reaches a target on a losing system.
func EstimateTrades(goal Goal) (int, error) {
	if goal.AssumedWinRate < 0 || goal.AssumedWinRate > 100 {
		return 0, fmt.Errorf("assumed win rate must be between 0 and 100, got %d", goal.AssumedWinRate)
	}
	if !goal.TargetProfitUSDT.IsPositive() {
		return 0, fmt.Errorf("target profit must be positive")
	}

	expected := goal.ExpectedPerTrade()
	if !expected.IsPositive() {
		return 0, fmt.Errorf("strategy has no positive expectancy (win rate %d%%, reward %s, risk %s) — no trade count reaches the target",
			goal.AssumedWinRate, goal.RewardPerTradeUSDT.String(), goal.RiskPerTradeUSDT.String())
	}

	// trades = ceil(target / expected)
	floor, err := goal.TargetProfitUSDT.QuoFloor(expected, 0)
	if err != nil {
		return 0, err
	}
	count, err := strconv.Atoi(floor.String())
	if err != nil {
		return 0, fmt.Errorf("estimate trades: %w", err)
	}
	if expected.Mul(decimal.NewFromInt(int64(count))).Cmp(goal.TargetProfitUSDT) < 0 {
		count++
	}
	return count, nil
}

// Verdict is the decision after a trade closes.
type Verdict string

const (
	Continue          Verdict = "continue"
	StopTargetReached Verdict = "target_reached"
	StopMaxDrawdown   Verdict = "max_drawdown"
	StopMaxTrades     Verdict = "max_trades"
	StopStrategyRule  Verdict = "strategy_rule"
)

// State is the live progress of a campaign.
type State struct {
	Goal         Goal
	RealizedPnL  decimal.Decimal // cumulative realised profit/loss
	TradesClosed int
}

// Evaluate decides whether the campaign keeps trading. Target is checked before
// drawdown so a run that hits its goal stops as a success even if the last leg
// was a loss.
func Evaluate(state State) Verdict {
	if state.RealizedPnL.Cmp(state.Goal.TargetProfitUSDT) >= 0 {
		return StopTargetReached
	}
	if state.Goal.MaxDrawdownUSDT.IsPositive() && state.RealizedPnL.Cmp(state.Goal.MaxDrawdownUSDT.Neg()) <= 0 {
		return StopMaxDrawdown
	}
	if state.Goal.MaxTrades > 0 && state.TradesClosed >= state.Goal.MaxTrades {
		return StopMaxTrades
	}
	return Continue
}
