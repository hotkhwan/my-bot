package journal

import (
	"context"
	"testing"

	"bottrade/internal/decimal"
)

func newService(t *testing.T) *Service {
	t.Helper()
	svc, err := NewService(NewMemoryRepository())
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	return svc
}

func closedTrade(id string, outcome Outcome, pnl string) Trade {
	return Trade{
		ID:      id,
		UserID:  1,
		Symbol:  "BTCUSDT",
		Side:    "long",
		Mode:    "binance_testnet",
		PnLUSDT: decimal.MustParse(pnl),
		Outcome: outcome,
	}
}

func TestRecordValidation(t *testing.T) {
	svc := newService(t)
	ctx := context.Background()

	bad := []Trade{
		{UserID: 1, Symbol: "BTCUSDT", Outcome: OutcomeOpen},         // no id
		{ID: "a", Symbol: "BTCUSDT", Outcome: OutcomeOpen},           // no user
		{ID: "a", UserID: 1, Outcome: OutcomeOpen},                   // no symbol
		{ID: "a", UserID: 1, Symbol: "BTCUSDT", Outcome: "nonsense"}, // bad outcome
	}
	for i, trade := range bad {
		if err := svc.Record(ctx, trade); err == nil {
			t.Fatalf("bad trade %d: expected validation error", i)
		}
	}

	// A trade with no explicit outcome defaults to open and is accepted.
	if err := svc.Record(ctx, Trade{ID: "ok", UserID: 1, Symbol: "BTCUSDT"}); err != nil {
		t.Fatalf("valid trade rejected: %v", err)
	}
	rep, err := svc.Report(ctx, Filter{})
	if err != nil {
		t.Fatalf("Report: %v", err)
	}
	if rep.Open != 1 || rep.Trades != 1 {
		t.Fatalf("report = %+v, want 1 open trade", rep)
	}
}

func TestReportAggregation(t *testing.T) {
	svc := newService(t)
	ctx := context.Background()

	// The campaign example: 10 trades, 7 win (+1), 3 lose (-1) -> +4 net.
	for i := 0; i < 7; i++ {
		mustRecord(t, svc, closedTrade(itoa("w", i), OutcomeWin, "1"))
	}
	for i := 0; i < 3; i++ {
		mustRecord(t, svc, closedTrade(itoa("l", i), OutcomeLoss, "-1"))
	}

	rep, err := svc.Report(ctx, Filter{})
	if err != nil {
		t.Fatalf("Report: %v", err)
	}

	checks := map[string]struct{ got, want string }{
		"win_rate":   {rep.WinRate.String(), "70"},
		"total_pnl":  {rep.TotalPnL.String(), "4"},
		"avg_win":    {rep.AvgWin.String(), "1"},
		"avg_loss":   {rep.AvgLoss.String(), "-1"},
		"expectancy": {rep.Expectancy.String(), "0.4"},
	}
	for name, c := range checks {
		if c.got != c.want {
			t.Fatalf("%s = %q, want %q", name, c.got, c.want)
		}
	}
	if rep.Trades != 10 || rep.Wins != 7 || rep.Losses != 3 {
		t.Fatalf("counts = %+v, want 10/7/3", rep)
	}
}

func TestReportGroupsByStrategyAndModel(t *testing.T) {
	svc := newService(t)
	ctx := context.Background()

	// scalp: 3 win / 2 lose (60%); swing: 4 win / 1 lose (80%).
	add := func(id string, strat string, models []string, outcome Outcome, pnl string) {
		tr := closedTrade(id, outcome, pnl)
		tr.Strategy = strat
		tr.Models = models
		mustRecord(t, svc, tr)
	}
	for i := 0; i < 3; i++ {
		add(itoa("sw", i), "scalp", []string{"claude"}, OutcomeWin, "1")
	}
	for i := 0; i < 2; i++ {
		add(itoa("sl", i), "scalp", []string{"claude", "qwen"}, OutcomeLoss, "-1")
	}
	for i := 0; i < 4; i++ {
		add(itoa("gw", i), "swing", []string{"qwen"}, OutcomeWin, "1")
	}
	add("gl0", "swing", []string{"qwen"}, OutcomeLoss, "-1")

	rep, err := svc.Report(ctx, Filter{})
	if err != nil {
		t.Fatalf("Report: %v", err)
	}

	if got := rep.ByStrategy["scalp"].WinRate.String(); got != "60" {
		t.Fatalf("scalp win rate = %q, want 60", got)
	}
	if got := rep.ByStrategy["swing"].WinRate.String(); got != "80" {
		t.Fatalf("swing win rate = %q, want 80", got)
	}
	// qwen voted on 2 scalp losses + 4 swing wins + 1 swing loss = 4 win / 3 lose.
	qwen := rep.ByModel["qwen"]
	if qwen.Wins != 4 || qwen.Losses != 3 {
		t.Fatalf("qwen stat = %+v, want 4 win / 3 lose", qwen)
	}
}

func TestReportFilterAndEmpty(t *testing.T) {
	svc := newService(t)
	ctx := context.Background()

	a := closedTrade("a", OutcomeWin, "2")
	a.CampaignID = "camp1"
	b := closedTrade("b", OutcomeLoss, "-1")
	b.CampaignID = "camp2"
	mustRecord(t, svc, a)
	mustRecord(t, svc, b)

	rep, err := svc.Report(ctx, Filter{CampaignID: "camp1"})
	if err != nil {
		t.Fatalf("Report: %v", err)
	}
	if rep.Trades != 1 || rep.TotalPnL.String() != "2" {
		t.Fatalf("filtered report = %+v, want only camp1", rep)
	}

	// Empty result must not divide by zero.
	empty, err := svc.Report(ctx, Filter{CampaignID: "none"})
	if err != nil {
		t.Fatalf("Report: %v", err)
	}
	if empty.Trades != 0 || empty.WinRate.String() != "0" || empty.Expectancy.String() != "0" {
		t.Fatalf("empty report = %+v, want zeros", empty)
	}
}

func mustRecord(t *testing.T, svc *Service, trade Trade) {
	t.Helper()
	if err := svc.Record(context.Background(), trade); err != nil {
		t.Fatalf("Record %s: %v", trade.ID, err)
	}
}

func itoa(prefix string, i int) string {
	return prefix + string(rune('0'+i))
}
