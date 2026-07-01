package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"bottrade/internal/auth"
	"bottrade/internal/orders"
)

// getJSON issues an authenticated GET and decodes the JSON body.
func getJSON(t *testing.T, server *Server, path, tok string) map[string]any {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, _ := server.App().Test(req)
	defer resp.Body.Close()
	var out map[string]any
	json.NewDecoder(resp.Body).Decode(&out)
	return out
}

func TestSharedAIFreeForApprovedCrew(t *testing.T) {
	tk, _ := auth.NewTokenizer(bytes.Repeat([]byte("k"), auth.MinSecretSize), 0)
	user, _ := tk.Issue("tg:5", "crew", "user")

	// Closed beta (FreeSubOpen=false): an approved crew member gets unlimited AI.
	beta := NewServer(testConfigWith(t, nil), nil, testLogger(), WithTokenizer(tk))
	if err := beta.access.Approve(context.Background(), "tg:5"); err != nil {
		t.Fatalf("approve: %v", err)
	}
	if me := getJSON(t, beta, "/api/me", user); me["ai_limit"].(float64) != -1 {
		t.Fatalf("approved crew ai_limit = %v, want -1 (unlimited)", me["ai_limit"])
	}

	// Public free launch (FreeSubOpen=true): the same user is metered by tier.
	open := NewServer(testConfigWith(t, map[string]string{"PRIVATE_BETA": "false"}), nil, testLogger(), WithTokenizer(tk))
	if err := open.access.Approve(context.Background(), "tg:5"); err != nil {
		t.Fatalf("approve: %v", err)
	}
	if me := getJSON(t, open, "/api/me", user); me["ai_limit"].(float64) != 10 {
		t.Fatalf("free-tier ai_limit = %v, want 10", me["ai_limit"])
	}
}

func TestPioneerPerkDuringClosedBeta(t *testing.T) {
	tk, _ := auth.NewTokenizer(bytes.Repeat([]byte("k"), auth.MinSecretSize), 0)
	user, _ := tk.Issue("tg:7", "pioneer", "user")

	// Closed beta: an approved crew member runs as Commander (pioneer perk).
	beta := NewServer(testConfigWith(t, nil), nil, testLogger(), WithTokenizer(tk))
	beta.access.Approve(context.Background(), "tg:7")
	if me := getJSON(t, beta, "/api/me", user); me["tier"] != "commander" {
		t.Fatalf("closed-beta approved tier = %v, want commander", me["tier"])
	}
	// An admin-set paid tier still wins over the pioneer default.
	beta.access.SetTier(context.Background(), "tg:7", "captain")
	if me := getJSON(t, beta, "/api/me", user); me["tier"] != "captain" {
		t.Fatalf("explicit captain should win, got %v", me["tier"])
	}

	// Public launch (PRIVATE_BETA=false): a plain approved user falls to Crew.
	open := NewServer(testConfigWith(t, map[string]string{"PRIVATE_BETA": "false"}), nil, testLogger(), WithTokenizer(tk))
	open.access.Approve(context.Background(), "tg:7")
	if me := getJSON(t, open, "/api/me", user); me["tier"] != "free" {
		t.Fatalf("post-launch approved tier = %v, want free (Crew)", me["tier"])
	}
}

func TestAdminEndpointsRejectNonAdmin(t *testing.T) {
	tk, _ := auth.NewTokenizer(bytes.Repeat([]byte("k"), auth.MinSecretSize), 0)
	member, _ := tk.Issue("tg:8", "member", "user")
	// ACCESS_OPEN so the user is "approved" yet still NOT admin — the strongest case.
	srv := NewServer(testConfigWith(t, map[string]string{"ACCESS_OPEN": "true", "TELEGRAM_ADMIN_USER_ID": "111"}), nil, testLogger(), WithTokenizer(tk))

	do := func(method, path string) int {
		req := httptest.NewRequest(method, path, bytes.NewReader([]byte(`{"subject":"tg:9","tier":"captain"}`)))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+member)
		resp, _ := srv.App().Test(req)
		resp.Body.Close()
		return resp.StatusCode
	}
	cases := []struct{ method, path string }{
		{http.MethodGet, "/api/admin/members"},
		{http.MethodGet, "/api/admin/pending"},
		{http.MethodPost, "/api/admin/approve"},
		{http.MethodPost, "/api/admin/revoke"},
		{http.MethodPost, "/api/admin/make-admin"},
		{http.MethodPost, "/api/admin/tier"},
	}
	for _, c := range cases {
		if code := do(c.method, c.path); code != http.StatusForbidden {
			t.Errorf("%s %s as non-admin = %d, want 403", c.method, c.path, code)
		}
	}
}

func TestFounderAssignmentAndCap(t *testing.T) {
	store := newMemAccess()
	// First MissionZeroCap subjects get numbers 1..cap; the next gets 0.
	for i := 1; i <= MissionZeroCap; i++ {
		n, _ := store.AssignFounder(context.Background(), "tg:"+strconv.Itoa(i), MissionZeroCap)
		if n != i {
			t.Fatalf("founder %d got number %d", i, n)
		}
	}
	if n, _ := store.AssignFounder(context.Background(), "tg:999", MissionZeroCap); n != 0 {
		t.Fatalf("over-cap founder = %d, want 0", n)
	}
	// Idempotent: re-assigning an existing founder keeps the same number.
	if n, _ := store.AssignFounder(context.Background(), "tg:5", MissionZeroCap); n != 5 {
		t.Fatalf("re-assign founder 5 = %d", n)
	}
}

func TestFounderKeepsCaptainAfterLaunch(t *testing.T) {
	tk, _ := auth.NewTokenizer(bytes.Repeat([]byte("k"), auth.MinSecretSize), 0)
	user, _ := tk.Issue("tg:5", "founder", "user")
	// Public launch (PRIVATE_BETA=false): a founder keeps Captain; a plain
	// approved member is Crew.
	srv := NewServer(testConfigWith(t, map[string]string{"PRIVATE_BETA": "false"}), nil, testLogger(), WithTokenizer(tk))
	srv.access.Approve(context.Background(), "tg:5")
	if me := getJSON(t, srv, "/api/me", user); me["tier"] != "free" {
		t.Fatalf("approved non-founder post-launch = %v, want free", me["tier"])
	}
	srv.access.AssignFounder(context.Background(), "tg:5", MissionZeroCap)
	if me := getJSON(t, srv, "/api/me", user); me["tier"] != "captain" || me["founder_number"].(float64) != 1 {
		t.Fatalf("founder post-launch = %v, want captain #1", me)
	}
}

func TestPromoteToAdmin(t *testing.T) {
	tk, _ := auth.NewTokenizer(bytes.Repeat([]byte("k"), auth.MinSecretSize), 0)
	root, _ := tk.Issue("tg:111", "root", "user")
	member, _ := tk.Issue("tg:8", "member", "user")
	cfg := testConfigWith(t, map[string]string{"TELEGRAM_ADMIN_USER_ID": "111"})
	srv := NewServer(cfg, nil, testLogger(), WithTokenizer(tk))

	post := func(tok, path string, body any) int {
		raw, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+tok)
		resp, _ := srv.App().Test(req)
		resp.Body.Close()
		return resp.StatusCode
	}

	// Member is not admin; cannot promote anyone.
	if getJSON(t, srv, "/api/me", member)["admin"] != false {
		t.Fatal("member should not be admin")
	}
	if code := post(member, "/api/admin/make-admin", map[string]any{"subject": "tg:8"}); code != http.StatusForbidden {
		t.Fatalf("member make-admin = %d, want 403", code)
	}
	// Root admin promotes the member → they become admin.
	if code := post(root, "/api/admin/make-admin", map[string]any{"subject": "tg:8"}); code != http.StatusOK {
		t.Fatalf("root make-admin = %d", code)
	}
	if getJSON(t, srv, "/api/me", member)["admin"] != true {
		t.Fatal("promoted member should be admin")
	}
	// The root admin cannot be demoted.
	if code := post(root, "/api/admin/make-admin", map[string]any{"subject": "tg:111", "demote": true}); code != http.StatusBadRequest {
		t.Fatalf("demote root = %d, want 400", code)
	}
}

func TestMissionRequiresActiveKey(t *testing.T) {
	stub := stubKlines(t)
	cfg := testConfigWith(t, map[string]string{"MARKETDATA_BASE_URL": stub.URL, "ACCESS_OPEN": "true"})
	tk, _ := auth.NewTokenizer(bytes.Repeat([]byte("k"), auth.MinSecretSize), 0)
	user, _ := tk.Issue("tg:7", "u", "user")
	keyring, err := auth.NewKeyring(map[string][]byte{"v1": bytes.Repeat([]byte("a"), auth.KeySize)}, "v1")
	if err != nil {
		t.Fatalf("keyring: %v", err)
	}
	credSvc, _ := auth.NewCredentialService(keyring, &memCredRepo{m: map[string][]auth.BinanceCredential{}})
	server := NewServer(cfg, nil, testLogger(), WithTokenizer(tk),
		WithOrders(orders.NewService(true, time.Minute, testLogger())), WithCredentials(credSvc))

	prepare := func() (string, bool) {
		b, _ := json.Marshal(map[string]any{"symbol": "BTC", "capital": 100})
		req := httptest.NewRequest(http.MethodPost, "/api/mission/prepare", bytes.NewReader(b))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+user)
		resp, _ := server.App().Test(req)
		defer resp.Body.Close()
		var out map[string]any
		json.NewDecoder(resp.Body).Decode(&out)
		s, _ := out["output"].(string)
		need, _ := out["need_key"].(bool)
		return s, need
	}

	// No active key yet → blocked with a Settings nudge.
	if out, need := prepare(); !need || !strings.Contains(out, "No active testnet Binance key") {
		t.Fatalf("without key: need=%v out=%q, want need_key + nudge", need, out)
	}
	// A mainnet profile is not enough for testnet Missions.
	if err := credSvc.StoreProfile(context.Background(), "tg:7", "mainnet",
		auth.BinanceKeys{APIKey: "k-main1234", APISecret: "s-main1234", Testnet: false}); err != nil {
		t.Fatalf("store mainnet profile: %v", err)
	}
	if err := credSvc.SetActive(context.Background(), "tg:7", "mainnet"); err != nil {
		t.Fatalf("set mainnet active: %v", err)
	}
	if out, need := prepare(); !need || !strings.Contains(out, "No active testnet Binance key") {
		t.Fatalf("with mainnet active key: need=%v out=%q, want testnet-key nudge", need, out)
	}
	// Activate a testnet key → the mission flows to the Confirm step.
	if err := credSvc.StoreProfile(context.Background(), "tg:7", "testnet",
		auth.BinanceKeys{APIKey: "k-abcd1234", APISecret: "s-abcd1234", Testnet: true}); err != nil {
		t.Fatalf("store profile: %v", err)
	}
	if err := credSvc.SetActive(context.Background(), "tg:7", "testnet"); err != nil {
		t.Fatalf("set active: %v", err)
	}
	if out, need := prepare(); need || !strings.Contains(out, "Review this live Mission") {
		t.Fatalf("with active key: need=%v out=%q, want the Confirm step", need, out)
	}
}

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
