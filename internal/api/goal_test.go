package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"bottrade/internal/auth"
	"bottrade/internal/campaign"
	"bottrade/internal/signals"
)

type fixedAdvisor struct {
	decision signals.Decision
	err      error
}

func (f fixedAdvisor) Decide(context.Context, signals.MarketSignal) (signals.Decision, error) {
	return f.decision, f.err
}

// stubKlines serves an uptrending klines series so the EMA strategy takes longs
// whose take-profit is hit — a deterministic "goal reached" paper run.
func stubKlines(t *testing.T) *httptest.Server {
	t.Helper()
	var b strings.Builder
	b.WriteString("[")
	price := 100.0
	for i := 0; i < 90; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		open := i * 3600000
		closeT := open + 3600000
		// high padded above close so a +2% TP fills; low barely below so the -1%
		// stop never does.
		fmt.Fprintf(&b, `[%d,"%.4f","%.4f","%.4f","%.4f","1",%d,"0",0,"0","0","0"]`,
			open, price, price*1.012, price*0.9995, price, closeT)
		price *= 1.01
	}
	b.WriteString("]")
	body := b.String()

	mux := http.NewServeMux()
	mux.HandleFunc("/fapi/v1/klines", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(body))
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server
}

func goalServer(t *testing.T) (*Server, string) {
	t.Helper()
	stub := stubKlines(t)
	cfg := testConfigWith(t, map[string]string{"MARKETDATA_BASE_URL": stub.URL})
	tk, err := auth.NewTokenizer(bytes.Repeat([]byte("k"), auth.MinSecretSize), 0)
	if err != nil {
		t.Fatalf("NewTokenizer: %v", err)
	}
	token, err := tk.Issue("tg:468848033", "hotkhwan", "user")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	return NewServer(cfg, nil, testLogger(), WithTokenizer(tk)), token
}

func postGoal(t *testing.T, server *Server, token string, body map[string]any) (int, map[string]any) {
	t.Helper()
	payload, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/goal/run", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := server.App().Test(req)
	if err != nil {
		t.Fatalf("Test: %v", err)
	}
	defer resp.Body.Close()
	var out map[string]any
	json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func TestGoalRunRealPaper(t *testing.T) {
	server, token := goalServer(t)

	// Unauthenticated → 401.
	if code, _ := postGoal(t, server, "", map[string]any{"profit": 5}); code != http.StatusUnauthorized {
		t.Fatalf("no-auth status = %d, want 401", code)
	}

	// Missing/zero profit → 400.
	if code, out := postGoal(t, server, token, map[string]any{"profit": 0, "symbol": "BTC"}); code != http.StatusBadRequest {
		t.Fatalf("zero profit status = %d (%v), want 400", code, out)
	}

	// Happy path: uptrend → all wins → target reached.
	code, out := postGoal(t, server, token, map[string]any{
		"profit": 5, "capital": 100, "capital_risk_pct": 30, "leverage_use_pct": 25,
		"symbol": "BTC", "strategy": "ema", "duration": "1h",
	})
	if code != http.StatusOK {
		t.Fatalf("goal run status = %d (%v)", code, out)
	}
	stats, _ := out["stats"].(map[string]any)
	if stats == nil {
		t.Fatalf("no stats in response: %v", out)
	}
	if trades, _ := stats["trades"].(float64); trades < 1 {
		t.Fatalf("expected trades, got %v", stats["trades"])
	}
	if v, _ := stats["verdict"].(string); v != "target_reached" {
		t.Fatalf("verdict = %q, want target_reached", v)
	}
	if wr, _ := stats["win_rate_pct"].(float64); wr != 100 {
		t.Fatalf("win rate = %v, want 100", wr)
	}
	if stats["duration"] != "1h" || stats["interval"] != "1m" {
		t.Fatalf("plan/execution timeframe = %v/%v, want 1h/1m", stats["duration"], stats["interval"])
	}
	if stats["leverage_use_pct"] != float64(25) {
		t.Fatalf("leverage use = %v, want 25", stats["leverage_use_pct"])
	}
	if stats["actionable"] != true || stats["needs_plan_edit"] == true {
		t.Fatalf("actionability = actionable:%v needs_plan_edit:%v, want actionable run", stats["actionable"], stats["needs_plan_edit"])
	}
	if estimate, _ := stats["estimated_entries"].(float64); estimate <= 0 {
		t.Fatalf("estimated entries = %v, want positive estimate", stats["estimated_entries"])
	}
	if out["output"] == nil || !strings.Contains(out["output"].(string), "Paper run on real") {
		t.Fatalf("missing paper-run summary text: %v", out["output"])
	}

	// History now has the run.
	hreq := httptest.NewRequest(http.MethodGet, "/api/goal/history", nil)
	hreq.Header.Set("Authorization", "Bearer "+token)
	hresp, _ := server.App().Test(hreq)
	var hist struct {
		Runs []map[string]any `json:"runs"`
	}
	json.NewDecoder(hresp.Body).Decode(&hist)
	hresp.Body.Close()
	if len(hist.Runs) == 0 {
		t.Fatal("expected goal history to contain the run")
	}
	if hist.Runs[0]["symbol"] != "BTCUSDT" {
		t.Fatalf("history symbol = %v, want BTCUSDT", hist.Runs[0]["symbol"])
	}
}

func TestGoalHistoryRequiresAuth(t *testing.T) {
	server, _ := goalServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/goal/history", nil)
	resp, _ := server.App().Test(req)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("history no-auth status = %d, want 401", resp.StatusCode)
	}
}

func TestPlanDurationExecutionIntervals(t *testing.T) {
	want := map[string]durationSpec{
		"15m": {ExecutionInterval: "1m", PlanBars: 15},
		"1h":  {ExecutionInterval: "1m", PlanBars: 60},
		"2h":  {ExecutionInterval: "1m", PlanBars: 120},
		"4h":  {ExecutionInterval: "5m", PlanBars: 48},
		"8h":  {ExecutionInterval: "5m", PlanBars: 96},
		"12h": {ExecutionInterval: "5m", PlanBars: 144},
		"24h": {ExecutionInterval: "15m", PlanBars: 96},
		"48h": {ExecutionInterval: "15m", PlanBars: 192},
		"1w":  {ExecutionInterval: "1h", PlanBars: 168},
	}
	for duration, expected := range want {
		if got := allowedDurations[duration]; got != expected {
			t.Errorf("%s spec = %+v, want %+v", duration, got, expected)
		}
	}
}

func TestAnnyBasicGoalPreservesRequestedDuration(t *testing.T) {
	server, token := goalServer(t)

	code, out := postGoal(t, server, token, map[string]any{
		"profit": 10, "capital": 100, "capital_risk_pct": 50, "leverage_use_pct": 50,
		"symbol": "BTC", "strategy": "anny_basic", "duration": "1h",
	})
	if code != http.StatusOK {
		t.Fatalf("anny goal status = %d (%v)", code, out)
	}
	stats, _ := out["stats"].(map[string]any)
	if stats == nil {
		t.Fatalf("no stats in response: %v", out)
	}
	if stats["duration"] != "1h" || stats["interval"] != "1m" {
		t.Fatalf("plan/execution timeframe = %v/%v, want 1h/1m", stats["duration"], stats["interval"])
	}
	if stats["validation_window"] != "90 x 1m" {
		t.Fatalf("validation window = %v, want actual loaded candles 90 x 1m", stats["validation_window"])
	}
	if stats["actionable"] != false || stats["needs_plan_edit"] != true {
		t.Fatalf("actionability = actionable:%v needs_plan_edit:%v, want plan edit", stats["actionable"], stats["needs_plan_edit"])
	}
	if reason, _ := stats["blocked_reason"].(string); !strings.Contains(reason, "No CDC/QQE setup") {
		t.Fatalf("blocked reason = %q, want CDC/QQE no-setup", reason)
	}
	if estimate, _ := stats["estimated_entries"].(float64); estimate <= 0 {
		t.Fatalf("estimated entries = %v, want positive estimate", stats["estimated_entries"])
	}
	if output, _ := out["output"].(string); !strings.Contains(output, "edit plan") || !strings.Contains(output, "Market data loaded") || !strings.Contains(output, "Estimated entries needed") {
		t.Fatalf("output = %q, want edit-plan guidance with entry estimate", output)
	}
	hreq := httptest.NewRequest(http.MethodGet, "/api/goal/history", nil)
	hreq.Header.Set("Authorization", "Bearer "+token)
	hresp, _ := server.App().Test(hreq)
	defer hresp.Body.Close()
	var hist struct {
		Runs []map[string]any `json:"runs"`
	}
	json.NewDecoder(hresp.Body).Decode(&hist)
	if len(hist.Runs) != 0 {
		t.Fatalf("no-setup assessment should not persist as a paper mission, got history: %+v", hist.Runs)
	}
}

func TestAIBiasLowConfidenceUsesBothSides(t *testing.T) {
	cfg := testConfigWith(t, nil)
	server := NewServer(cfg, nil, testLogger(), WithAdvisor(fixedAdvisor{decision: signals.Decision{
		Action:            signals.ActionOpen,
		Side:              "long",
		ConfidencePercent: 35,
	}}))

	bias, note := server.aiBias(context.Background(), "tg:468848033", "BTCUSDT", 60000)
	if bias != campaign.BiasBoth {
		t.Fatalf("bias = %q, want both", bias)
	}
	if !strings.Contains(note, "confidence 35% is low") {
		t.Fatalf("note = %q, want low-confidence explanation", note)
	}
}
