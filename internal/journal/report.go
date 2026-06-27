package journal

import "bottrade/internal/decimal"

// Report is the aggregated performance of a set of trades. Counts cover all
// matched trades; PnL/win-rate statistics cover resolved (closed) trades only.
type Report struct {
	Trades     int             `json:"trades"`
	Wins       int             `json:"wins"`
	Losses     int             `json:"losses"`
	Breakeven  int             `json:"breakeven"`
	Open       int             `json:"open"`
	WinRate    decimal.Decimal `json:"win_rate"`   // % over decisive (win+loss)
	TotalPnL   decimal.Decimal `json:"total_pnl"`  // sum over closed trades
	AvgWin     decimal.Decimal `json:"avg_win"`    // mean PnL of winners
	AvgLoss    decimal.Decimal `json:"avg_loss"`   // mean PnL of losers (<= 0)
	Expectancy decimal.Decimal `json:"expectancy"` // TotalPnL / closed count

	ByStrategy map[string]GroupStat `json:"by_strategy"`
	ByModel    map[string]GroupStat `json:"by_model"`
}

// GroupStat is the resolved performance of one strategy or one AI model.
type GroupStat struct {
	Trades   int             `json:"trades"`
	Wins     int             `json:"wins"`
	Losses   int             `json:"losses"`
	WinRate  decimal.Decimal `json:"win_rate"`
	TotalPnL decimal.Decimal `json:"total_pnl"`
}

type groupAcc struct {
	trades, wins, losses int
	pnl                  decimal.Decimal
}

func aggregate(trades []Trade) Report {
	report := Report{ByStrategy: map[string]GroupStat{}, ByModel: map[string]GroupStat{}}

	total := decimal.Zero()
	sumWins := decimal.Zero()
	sumLosses := decimal.Zero()
	closed := 0
	byStrategy := map[string]*groupAcc{}
	byModel := map[string]*groupAcc{}

	for _, trade := range trades {
		report.Trades++
		switch trade.Outcome {
		case OutcomeWin:
			report.Wins++
			sumWins = sumWins.Add(trade.PnLUSDT)
		case OutcomeLoss:
			report.Losses++
			sumLosses = sumLosses.Add(trade.PnLUSDT)
		case OutcomeBreakeven:
			report.Breakeven++
		default:
			report.Open++
		}

		if !trade.Outcome.closed() {
			continue
		}
		total = total.Add(trade.PnLUSDT)
		closed++
		accumulate(byStrategy, strategyKey(trade.Strategy), trade)
		for _, model := range trade.Models {
			accumulate(byModel, model, trade)
		}
	}

	decisive := report.Wins + report.Losses
	report.WinRate = pct(report.Wins, decisive)
	report.TotalPnL = total
	report.AvgWin = div(sumWins, report.Wins)
	report.AvgLoss = div(sumLosses, report.Losses)
	report.Expectancy = div(total, closed)
	report.ByStrategy = finalize(byStrategy)
	report.ByModel = finalize(byModel)
	return report
}

func accumulate(groups map[string]*groupAcc, key string, trade Trade) {
	acc := groups[key]
	if acc == nil {
		acc = &groupAcc{pnl: decimal.Zero()}
		groups[key] = acc
	}
	acc.trades++
	acc.pnl = acc.pnl.Add(trade.PnLUSDT)
	switch trade.Outcome {
	case OutcomeWin:
		acc.wins++
	case OutcomeLoss:
		acc.losses++
	}
}

func finalize(groups map[string]*groupAcc) map[string]GroupStat {
	out := make(map[string]GroupStat, len(groups))
	for key, acc := range groups {
		out[key] = GroupStat{
			Trades:   acc.trades,
			Wins:     acc.wins,
			Losses:   acc.losses,
			WinRate:  pct(acc.wins, acc.wins+acc.losses),
			TotalPnL: acc.pnl,
		}
	}
	return out
}

func strategyKey(strategy string) string {
	if strategy == "" {
		return "(none)"
	}
	return strategy
}

// pct returns numerator/denominator as a percentage with 2 decimal places.
func pct(numerator, denominator int) decimal.Decimal {
	if denominator == 0 {
		return decimal.Zero()
	}
	value, err := decimal.NewFromInt(int64(numerator)*100).QuoFloor(decimal.NewFromInt(int64(denominator)), 2)
	if err != nil {
		return decimal.Zero()
	}
	return value
}

// div returns sum/count to 8 decimal places, or zero when count is zero.
func div(sum decimal.Decimal, count int) decimal.Decimal {
	if count == 0 {
		return decimal.Zero()
	}
	value, err := sum.QuoFloor(decimal.NewFromInt(int64(count)), 8)
	if err != nil {
		return decimal.Zero()
	}
	return value
}
