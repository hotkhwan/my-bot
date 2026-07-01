package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"bottrade/internal/auth"
	"bottrade/internal/decimal"
	"bottrade/internal/journal"
	"bottrade/internal/transparency"
)

func TestRecorderFeed(t *testing.T) {
	repo := journal.NewMemoryRepository()
	svc, err := journal.NewService(repo)
	if err != nil {
		t.Fatalf("journal service: %v", err)
	}
	// Seed two real testnet trades (one win, one loss) for the Telegram user the
	// token below belongs to.
	seed := func(id, symbol, side string, pnl int64, outcome journal.Outcome, campaignID string) {
		if err := svc.Record(t.Context(), journal.Trade{
			ID: id, UserID: 468848033, Symbol: symbol, Side: side, Mode: "binance_testnet",
			CampaignID: campaignID, Leverage: 3, Strategy: "ema_cross",
			Entry: decimal.NewFromInt(100), Exit: decimal.NewFromInt(100 + pnl),
			PnLUSDT: decimal.NewFromInt(pnl), Outcome: outcome,
			OpenedAt: time.Unix(1000, 0), ClosedAt: time.Unix(2000, 0),
		}); err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
	}
	seed("t1", "BTCUSDT", "long", 2, journal.OutcomeWin, "campaign-1") // autonomous mission
	seed("t2", "ETHUSDT", "short", -1, journal.OutcomeLoss, "")        // manual

	tk, _ := auth.NewTokenizer(bytes.Repeat([]byte("k"), auth.MinSecretSize), 0)
	token, _ := tk.Issue("tg:468848033", "hotkhwan", "user")
	server := NewServer(testConfig(), nil, testLogger(), WithTokenizer(tk), WithReport(svc))

	req := httptest.NewRequest(http.MethodGet, "/api/recorder", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := server.App().Test(req)
	if err != nil {
		t.Fatalf("Test: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var out struct {
		Stats struct {
			RealTrades  int     `json:"real_trades"`
			RealWins    int     `json:"real_wins"`
			RealLosses  int     `json:"real_losses"`
			RealWinRate float64 `json:"real_win_rate"`
			RealPnL     string  `json:"real_pnl"`
		} `json:"stats"`
		Entries []struct {
			MissionNo  int    `json:"mission_no"`
			Label      string `json:"label"`
			PnLSource  string `json:"pnl_source"`
			Autonomous bool   `json:"autonomous"`
			Leverage   int    `json:"leverage"`
			Reason     string `json:"reason"`
			Hash       string `json:"hash"`
		} `json:"entries"`
		MerkleRoot string `json:"merkle_root"`
		Anchored   bool   `json:"anchored"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Stats.RealTrades != 2 || out.Stats.RealWins != 1 || out.Stats.RealLosses != 1 {
		t.Fatalf("stats = %+v", out.Stats)
	}
	if out.Stats.RealWinRate != 50 {
		t.Fatalf("win rate = %v, want 50", out.Stats.RealWinRate)
	}
	if out.Stats.RealPnL != "1.00" {
		t.Fatalf("pnl = %q, want 1.00", out.Stats.RealPnL)
	}
	if len(out.Entries) != 2 {
		t.Fatalf("want 2 entries, got %d", len(out.Entries))
	}
	// Missions numbered oldest=1; both seeded at the same time so order is stable
	// by sort but numbers must cover {1,2}.
	nums := map[int]bool{}
	var sawAutonomous bool
	for _, e := range out.Entries {
		if e.Label != "testnet" {
			t.Fatalf("label = %q, want testnet", e.Label)
		}
		if e.PnLSource != "testnet" {
			t.Fatalf("pnl_source = %q, want testnet", e.PnLSource)
		}
		if len(e.Hash) != 64 {
			t.Fatalf("hash len = %d, want 64", len(e.Hash))
		}
		if e.Leverage != 3 {
			t.Fatalf("leverage = %d, want 3", e.Leverage)
		}
		if e.Reason == "" {
			t.Fatalf("mission reason should not be empty")
		}
		nums[e.MissionNo] = true
		if e.Autonomous {
			sawAutonomous = true
		}
	}
	if !nums[1] || !nums[2] {
		t.Fatalf("mission numbers = %v, want {1,2}", nums)
	}
	if !sawAutonomous {
		t.Fatal("expected one autonomous mission (campaign-1)")
	}
	if out.MerkleRoot == "" || out.Anchored {
		t.Fatalf("merkle root must be present and not yet anchored: root=%q anchored=%v", out.MerkleRoot, out.Anchored)
	}

	// Unauthenticated → 401.
	r2 := httptest.NewRequest(http.MethodGet, "/api/recorder", nil)
	resp2, _ := server.App().Test(r2)
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no-auth status = %d, want 401", resp2.StatusCode)
	}
}

func TestRecorderPnLSourceMapping(t *testing.T) {
	cases := map[string]string{
		transparency.LabelPaper: "paper",
		"testnet":               "testnet",
		"live":                  "exchange_realized",
		"unknown":               "unknown",
	}
	for label, want := range cases {
		if got := recorderPnLSource(label); got != want {
			t.Fatalf("recorderPnLSource(%q) = %q, want %q", label, got, want)
		}
	}
}
