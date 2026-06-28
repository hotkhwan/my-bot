package api

import (
	"math"
	"sort"
	"strconv"

	"github.com/gofiber/fiber/v3"
)

// The Community leaderboard aggregates paper goal runs across ALL users into
// per-strategy and per-coin stats — proof that real people make this work, by
// their own style. It exposes only aggregates (never who traded what) and is
// framed as a probability, not a signal: showing backward-looking proven win
// rates, not forward "buy now" calls, so it can't herd the market or hand a
// whale a front-run. Not financial advice.

const communitySampleLimit = 2000

type stratStat struct {
	Name         string  `json:"name"`
	Runs         int     `json:"runs"`
	WinRate      float64 `json:"win_rate"`
	AvgProfitPct float64 `json:"avg_profit_pct"`
	Trades       int     `json:"trades"`
	Sharpe       float64 `json:"sharpe"`
}

type coinStat struct {
	Symbol       string  `json:"symbol"`
	Runs         int     `json:"runs"`
	WinRate      float64 `json:"win_rate"`
	AvgProfitPct float64 `json:"avg_profit_pct"`
	Edge         int     `json:"edge"` // 0–100 probability badge (community win rate)
}

type agg struct {
	runs       int
	wins       int
	trades     int
	profitPcts []float64
}

func (a *agg) add(r GoalRun) {
	a.runs++
	a.wins += r.Wins
	a.trades += r.Trades
	capital, _ := strconv.ParseFloat(r.Capital, 64)
	pnl, perr := strconv.ParseFloat(r.RealizedPnL, 64)
	if perr == nil && capital > 0 {
		a.profitPcts = append(a.profitPcts, pnl/capital*100)
	}
}

func (a *agg) winRate() float64 {
	if a.trades == 0 {
		return 0
	}
	return float64(a.wins) / float64(a.trades) * 100
}

func (a *agg) avgProfitPct() float64 {
	if len(a.profitPcts) == 0 {
		return 0
	}
	var sum float64
	for _, p := range a.profitPcts {
		sum += p
	}
	return sum / float64(len(a.profitPcts))
}

// sharpe is a consistency proxy: mean / std-dev of per-run profit%. Zero when
// there is too little spread to be meaningful.
func (a *agg) sharpe() float64 {
	n := len(a.profitPcts)
	if n < 2 {
		return 0
	}
	mean := a.avgProfitPct()
	var v float64
	for _, p := range a.profitPcts {
		v += (p - mean) * (p - mean)
	}
	std := math.Sqrt(v / float64(n))
	if std == 0 {
		return 0
	}
	return round2(mean / std)
}

func (s *Server) handleLeaderboard(c fiber.Ctx) error {
	runs, err := s.goalRuns.Community(c.Context(), communitySampleLimit)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "could not load community stats"})
	}

	byStrat := map[string]*agg{}
	byCoin := map[string]*agg{}
	for _, r := range runs {
		if r.Strategy != "" {
			ensure(byStrat, r.Strategy).add(r)
		}
		if r.Symbol != "" {
			ensure(byCoin, r.Symbol).add(r)
		}
	}

	strategies := make([]stratStat, 0, len(byStrat))
	for name, a := range byStrat {
		strategies = append(strategies, stratStat{
			Name: name, Runs: a.runs, WinRate: round2(a.winRate()),
			AvgProfitPct: round2(a.avgProfitPct()), Trades: a.trades, Sharpe: a.sharpe(),
		})
	}
	sort.Slice(strategies, func(i, j int) bool { return strategies[i].AvgProfitPct > strategies[j].AvgProfitPct })

	coins := make([]coinStat, 0, len(byCoin))
	for sym, a := range byCoin {
		coins = append(coins, coinStat{
			Symbol: sym, Runs: a.runs, WinRate: round2(a.winRate()),
			AvgProfitPct: round2(a.avgProfitPct()), Edge: int(math.Round(a.winRate())),
		})
	}
	sort.Slice(coins, func(i, j int) bool { return coins[i].Edge > coins[j].Edge })
	if len(coins) > 24 {
		coins = coins[:24]
	}

	return c.JSON(fiber.Map{
		"strategies":  strategies,
		"coins":       coins,
		"total_runs":  len(runs),
		"note":        "Community paper performance — proven probability by real members' own styles, not a signal. Not financial advice.",
		"paper_based": true,
	})
}

func ensure(m map[string]*agg, key string) *agg {
	if m[key] == nil {
		m[key] = &agg{}
	}
	return m[key]
}

func round2(f float64) float64 { return math.Round(f*100) / 100 }
