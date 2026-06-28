package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestLeaderboardAggregatesCommunity(t *testing.T) {
	server, token := goalServer(t)

	// Two paper runs on BTC contribute to the community aggregate.
	for i := 0; i < 2; i++ {
		if code, out := postGoal(t, server, token, map[string]any{
			"profit": 5, "capital": 100, "risk": 30, "symbol": "BTC", "strategy": "ema", "interval": "1h", "bars": 90,
		}); code != http.StatusOK {
			t.Fatalf("seed goal status = %d (%v)", code, out)
		}
	}

	req := httptest.NewRequest(http.MethodGet, "/api/leaderboard", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, _ := server.App().Test(req)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("leaderboard status = %d", resp.StatusCode)
	}
	var out struct {
		Strategies []struct {
			Name string `json:"name"`
			Runs int    `json:"runs"`
		} `json:"strategies"`
		Coins []struct {
			Symbol string `json:"symbol"`
			Edge   int    `json:"edge"`
			Runs   int    `json:"runs"`
		} `json:"coins"`
		TotalRuns  int  `json:"total_runs"`
		PaperBased bool `json:"paper_based"`
	}
	json.NewDecoder(resp.Body).Decode(&out)

	if out.TotalRuns < 2 || !out.PaperBased {
		t.Fatalf("total_runs = %d paper_based=%v", out.TotalRuns, out.PaperBased)
	}
	var foundStrat bool
	for _, s := range out.Strategies {
		if s.Name == "ema_cross" && s.Runs >= 2 {
			foundStrat = true
		}
	}
	if !foundStrat {
		t.Fatalf("ema_cross not aggregated: %+v", out.Strategies)
	}
	var btc bool
	for _, c := range out.Coins {
		if c.Symbol == "BTCUSDT" && c.Runs >= 2 && c.Edge >= 0 && c.Edge <= 100 {
			btc = true
		}
	}
	if !btc {
		t.Fatalf("BTCUSDT not in coin leaderboard: %+v", out.Coins)
	}

	// Auth required.
	r2 := httptest.NewRequest(http.MethodGet, "/api/leaderboard", nil)
	resp2, _ := server.App().Test(r2)
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no-auth status = %d, want 401", resp2.StatusCode)
	}
}
