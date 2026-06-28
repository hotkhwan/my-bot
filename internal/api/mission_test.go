package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"bottrade/internal/auth"
	"bottrade/internal/orders"
)

func TestMissionPrepareAndConfirm(t *testing.T) {
	stub := stubKlines(t) // uptrend → EMA goes long
	cfg := testConfigWith(t, map[string]string{"MARKETDATA_BASE_URL": stub.URL, "ACCESS_OPEN": "true"})
	tk, _ := auth.NewTokenizer(bytes.Repeat([]byte("k"), auth.MinSecretSize), 0)
	token, _ := tk.Issue("tg:468848033", "u", "user")
	orderSvc := orders.NewService(true, time.Minute, testLogger()) // dry-run executor
	server := NewServer(cfg, nil, testLogger(), WithTokenizer(tk), WithOrders(orderSvc))

	post := func(path, tok string, body any) (int, map[string]any) {
		b, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(b))
		req.Header.Set("Content-Type", "application/json")
		if tok != "" {
			req.Header.Set("Authorization", "Bearer "+tok)
		}
		resp, _ := server.App().Test(req)
		defer resp.Body.Close()
		var out map[string]any
		json.NewDecoder(resp.Body).Decode(&out)
		return resp.StatusCode, out
	}

	// Prepare a live mission — staged, not yet placed.
	code, out := post("/api/mission/prepare", token, map[string]any{
		"symbol": "BTC", "capital": 100, "strategy": "ema", "interval": "1h",
	})
	if code != http.StatusOK {
		t.Fatalf("prepare status = %d (%v)", code, out)
	}
	cid, _ := out["confirm_id"].(string)
	if cid == "" || !strings.Contains(out["output"].(string), "Review this live Mission") {
		t.Fatalf("prepare = %v, want a confirm_id and review text", out)
	}

	// Confirm executes (dry-run, so it reports a DRY-RUN result — nothing real).
	_, conf := post("/api/confirm", token, map[string]any{"id": cid})
	if !strings.Contains(conf["output"].(string), "DRY-RUN") {
		t.Fatalf("confirm output = %v, want DRY-RUN", conf["output"])
	}
}

func TestMissionGated(t *testing.T) {
	stub := stubKlines(t)
	// ACCESS_OPEN=false → non-admin must be approved first.
	cfg := testConfigWith(t, map[string]string{
		"MARKETDATA_BASE_URL": stub.URL, "ACCESS_OPEN": "false", "TELEGRAM_ADMIN_USER_ID": "111",
	})
	tk, _ := auth.NewTokenizer(bytes.Repeat([]byte("k"), auth.MinSecretSize), 0)
	tgUser, _ := tk.Issue("tg:999", "u", "user")     // not admin, not approved
	pwUser, _ := tk.Issue("uuid-abc", "web", "user") // not a Telegram user
	orderSvc := orders.NewService(true, time.Minute, testLogger())
	server := NewServer(cfg, nil, testLogger(), WithTokenizer(tk), WithOrders(orderSvc))

	post := func(tok string) string {
		b, _ := json.Marshal(map[string]any{"symbol": "BTC", "capital": 100})
		req := httptest.NewRequest(http.MethodPost, "/api/mission/prepare", bytes.NewReader(b))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+tok)
		resp, _ := server.App().Test(req)
		defer resp.Body.Close()
		var out map[string]any
		json.NewDecoder(resp.Body).Decode(&out)
		s, _ := out["output"].(string)
		return s
	}

	if got := post(tgUser); !strings.Contains(got, "pending approval") {
		t.Fatalf("unapproved tg user = %q, want pending approval", got)
	}

	// On an open server (approval passes) a non-Telegram user still can't run a
	// live mission — the key is tied to a Telegram account.
	openCfg := testConfigWith(t, map[string]string{"MARKETDATA_BASE_URL": stub.URL, "ACCESS_OPEN": "true"})
	openSrv := NewServer(openCfg, nil, testLogger(), WithTokenizer(tk), WithOrders(orderSvc))
	b, _ := json.Marshal(map[string]any{"symbol": "BTC", "capital": 100})
	req := httptest.NewRequest(http.MethodPost, "/api/mission/prepare", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+pwUser)
	resp, _ := openSrv.App().Test(req)
	defer resp.Body.Close()
	var out map[string]any
	json.NewDecoder(resp.Body).Decode(&out)
	if got, _ := out["output"].(string); !strings.Contains(got, "Telegram login") {
		t.Fatalf("password user (open) = %q, want Telegram-login required", got)
	}
}

func TestMissionLeverageFromRisk(t *testing.T) {
	cases := []struct{ risk, want int }{
		{30, 30}, {100, 100}, {0, 3}, {-5, 3}, {250, 100}, {1, 1},
	}
	for _, c := range cases {
		if got := missionLeverageFor(c.risk); got != c.want {
			t.Errorf("missionLeverageFor(%d) = %d, want %d", c.risk, got, c.want)
		}
	}
}
