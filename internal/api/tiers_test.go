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

func TestTierLimitsAndUpgrade(t *testing.T) {
	stub := stubKlines(t)
	cfg := testConfigWith(t, map[string]string{
		"MARKETDATA_BASE_URL": stub.URL, "ACCESS_OPEN": "true", "TELEGRAM_ADMIN_USER_ID": "111",
	})
	tk, _ := auth.NewTokenizer(bytes.Repeat([]byte("k"), auth.MinSecretSize), 0)
	user, _ := tk.Issue("tg:999", "u", "user")
	admin, _ := tk.Issue("tg:111", "boss", "user")
	server := NewServer(cfg, nil, testLogger(), WithTokenizer(tk), WithOrders(orders.NewService(true, time.Minute, testLogger())))

	call := func(method, path, tok string, body any) (int, map[string]any) {
		var rdr *bytes.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			rdr = bytes.NewReader(b)
		} else {
			rdr = bytes.NewReader(nil)
		}
		req := httptest.NewRequest(method, path, rdr)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+tok)
		resp, _ := server.App().Test(req)
		defer resp.Body.Close()
		var out map[string]any
		json.NewDecoder(resp.Body).Decode(&out)
		return resp.StatusCode, out
	}
	mission := func(tok string) string {
		_, out := call(http.MethodPost, "/api/mission/prepare", tok, map[string]any{"symbol": "BTC", "capital": 100})
		s, _ := out["output"].(string)
		return s
	}

	// Free tier: 5 live missions allowed, the 6th is blocked.
	for i := 0; i < 5; i++ {
		if got := mission(user); !strings.Contains(got, "Review this live Mission") {
			t.Fatalf("mission %d blocked early: %q", i+1, got)
		}
	}
	if got := mission(user); !strings.Contains(got, "limit (5)") {
		t.Fatalf("6th mission = %q, want daily limit", got)
	}
	if _, me := call(http.MethodGet, "/api/me", user, nil); me["tier"] != "free" || me["mission_used"].(float64) != 5 {
		t.Fatalf("me = %v, want free with 5 used", me)
	}

	// Non-admin cannot set tiers.
	if code, _ := call(http.MethodPost, "/api/admin/tier", user, map[string]any{"subject": "tg:999", "tier": "captain"}); code != http.StatusForbidden {
		t.Fatalf("non-admin tier set = %d, want 403", code)
	}
	// Admin upgrades the user to Captain.
	if code, _ := call(http.MethodPost, "/api/admin/tier", admin, map[string]any{"subject": "tg:999", "tier": "captain"}); code != http.StatusOK {
		t.Fatalf("admin tier set = %d", code)
	}
	if _, me := call(http.MethodGet, "/api/me", user, nil); me["tier"] != "captain" || me["mission_limit"].(float64) != 50 {
		t.Fatalf("after upgrade me = %v, want captain/50", me)
	}
	// Now missions flow again (5 < 50).
	if got := mission(user); !strings.Contains(got, "Review this live Mission") {
		t.Fatalf("captain mission blocked: %q", got)
	}
}
