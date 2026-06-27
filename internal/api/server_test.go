package api

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	"bottrade/internal/auth"
	"bottrade/internal/config"
	"bottrade/internal/decimal"
	"bottrade/internal/journal"
	"bottrade/internal/realtime"
	"bottrade/internal/signals"
	"bottrade/internal/users"
)

func testTokenizer(t *testing.T) *auth.Tokenizer {
	t.Helper()
	tk, err := auth.NewTokenizer(bytes.Repeat([]byte("k"), auth.MinSecretSize), 0)
	if err != nil {
		t.Fatalf("NewTokenizer: %v", err)
	}
	return tk
}

func TestLoginIssuesSessionToken(t *testing.T) {
	userSvc, _ := users.NewService(users.NewMemoryRepository())
	tk := testTokenizer(t)
	server := NewServer(testConfig(), nil, testLogger(), WithUsers(userSvc), WithTokenizer(tk))

	post := func(path, payload string) []byte {
		req := httptest.NewRequest(http.MethodPost, path, bytes.NewBufferString(payload))
		req.Header.Set("Content-Type", "application/json")
		resp, err := server.App().Test(req)
		if err != nil {
			t.Fatalf("Test %s: %v", path, err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return body
	}

	post("/api/register", `{"username":"alice","password":"supersecret"}`)
	body := post("/api/login", `{"username":"alice","password":"supersecret"}`)

	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	token, _ := out["token"].(string)
	if token == "" {
		t.Fatalf("login returned no token: %s", body)
	}
	claims, err := tk.Verify(token)
	if err != nil || claims.Username != "alice" {
		t.Fatalf("token verify = %+v, %v", claims, err)
	}
}

func TestTradingViewWebhookAcceptsSignal(t *testing.T) {
	cfg := testConfig()
	processor := signals.NewProcessor(signals.ProcessorConfig{
		AdminUserID: 12345,
		Logger:      testLogger(),
	})
	server := NewServer(cfg, processor, testLogger())

	req := httptest.NewRequest(http.MethodPost, "/tradingview/webhook", bytes.NewBufferString(`{"secret":"secret","symbol":"BTCUSDT","price":"67500","indicators":{"rsi":28.4}}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := server.App().Test(req)
	if err != nil {
		t.Fatalf("Test returned error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, body = %s", resp.StatusCode, body)
	}

	var result signals.ProcessResult
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !result.Accepted {
		t.Fatal("Accepted = false, want true")
	}
	if result.Signal.Symbol != "BTCUSDT" {
		t.Fatalf("Symbol = %q, want BTCUSDT", result.Signal.Symbol)
	}
}

func TestTradingViewWebhookAcceptsTradingViewAliases(t *testing.T) {
	cfg := testConfig()
	processor := signals.NewProcessor(signals.ProcessorConfig{
		AdminUserID: 12345,
		Logger:      testLogger(),
	})
	server := NewServer(cfg, processor, testLogger())

	req := httptest.NewRequest(http.MethodPost, "/tradingview/webhook", bytes.NewBufferString(`{"secret":"secret","ticker":"ETHUSDT","timeframe":"1h","close":"3300"}`))
	resp, err := server.App().Test(req)
	if err != nil {
		t.Fatalf("Test returned error: %v", err)
	}
	defer resp.Body.Close()

	var result signals.ProcessResult
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if result.Signal.Symbol != "ETHUSDT" {
		t.Fatalf("Symbol = %q, want ETHUSDT", result.Signal.Symbol)
	}
	if result.Signal.Price != "3300" {
		t.Fatalf("Price = %q, want 3300", result.Signal.Price)
	}
}

func TestTradingViewWebhookRejectsBadSecret(t *testing.T) {
	cfg := testConfig()
	server := NewServer(cfg, signals.NewProcessor(signals.ProcessorConfig{Logger: testLogger()}), testLogger())

	req := httptest.NewRequest(http.MethodPost, "/tradingview/webhook", bytes.NewBufferString(`{"secret":"wrong","symbol":"BTCUSDT","price":"67500"}`))
	resp, err := server.App().Test(req)
	if err != nil {
		t.Fatalf("Test returned error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
}

func TestHealthz(t *testing.T) {
	server := NewServer(testConfig(), nil, testLogger())

	resp, err := server.App().Test(httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if err != nil {
		t.Fatalf("Test returned error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
}

func TestReadyzExposesNoTradingConfig(t *testing.T) {
	server := NewServer(testConfig(), nil, testLogger())

	resp, err := server.App().Test(httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if err != nil {
		t.Fatalf("Test returned error: %v", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	for _, leak := range []string{"dry_run", "autotrade", "tradingview", "\"ai\""} {
		if bytes.Contains(body, []byte(leak)) {
			t.Fatalf("/readyz leaked %q: %s", leak, body)
		}
	}
}

func TestStatusDisabledWithoutToken(t *testing.T) {
	server := NewServer(testConfig(), nil, testLogger())

	resp, err := server.App().Test(httptest.NewRequest(http.MethodGet, "/status", nil))
	if err != nil {
		t.Fatalf("Test returned error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 when no token configured", resp.StatusCode)
	}
}

func TestStatusRequiresBearerToken(t *testing.T) {
	cfg := testConfigWith(t, map[string]string{"HTTP_STATUS_TOKEN": "s3cr3t-token"})
	server := NewServer(cfg, nil, testLogger())

	cases := []struct {
		name   string
		header string
		want   int
	}{
		{"no header", "", http.StatusUnauthorized},
		{"wrong token", "Bearer nope", http.StatusUnauthorized},
		{"missing prefix", "s3cr3t-token", http.StatusUnauthorized},
		{"correct token", "Bearer s3cr3t-token", http.StatusOK},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/status", nil)
			if tc.header != "" {
				req.Header.Set("Authorization", tc.header)
			}
			resp, err := server.App().Test(req)
			if err != nil {
				t.Fatalf("Test returned error: %v", err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != tc.want {
				t.Fatalf("status = %d, want %d", resp.StatusCode, tc.want)
			}
			body, _ := io.ReadAll(resp.Body)
			// The configured token must never appear in any response body.
			if bytes.Contains(body, []byte("s3cr3t-token")) {
				t.Fatalf("/status response leaked the token: %s", body)
			}
			if tc.want == http.StatusOK && !bytes.Contains(body, []byte("dry_run")) {
				t.Fatalf("authorized /status missing flags: %s", body)
			}
		})
	}
}

func signLoginWidget(botToken string, fields map[string]string) map[string]string {
	keys := make([]string, 0, len(fields))
	for k := range fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	pairs := make([]string, 0, len(keys))
	for _, k := range keys {
		pairs = append(pairs, k+"="+fields[k])
	}
	secret := sha256.Sum256([]byte(botToken))
	mac := hmac.New(sha256.New, secret[:])
	mac.Write([]byte(strings.Join(pairs, "\n")))
	out := map[string]string{"hash": hex.EncodeToString(mac.Sum(nil))}
	for k, v := range fields {
		out[k] = v
	}
	return out
}

func TestAuthConfigAndTelegramLogin(t *testing.T) {
	tk, err := auth.NewTokenizer(bytes.Repeat([]byte("k"), auth.MinSecretSize), 0)
	if err != nil {
		t.Fatalf("NewTokenizer: %v", err)
	}
	cfg := testConfigWith(t, map[string]string{"TELEGRAM_BOT_USERNAME": "@mytradebot"})
	server := NewServer(cfg, nil, testLogger(), WithTokenizer(tk))

	// auth-config exposes the (leading @ stripped) username and enabled flag.
	resp, err := server.App().Test(httptest.NewRequest(http.MethodGet, "/api/auth-config", nil))
	if err != nil {
		t.Fatalf("Test: %v", err)
	}
	var ac struct {
		Username string `json:"telegram_bot_username"`
		Enabled  bool   `json:"telegram_login_enabled"`
	}
	json.NewDecoder(resp.Body).Decode(&ac)
	resp.Body.Close()
	if ac.Username != "mytradebot" || !ac.Enabled {
		t.Fatalf("auth-config = %+v, want mytradebot/enabled", ac)
	}

	post := func(fields map[string]string) (int, map[string]any) {
		payload, _ := json.Marshal(fields)
		req := httptest.NewRequest(http.MethodPost, "/api/telegram-login", bytes.NewReader(payload))
		req.Header.Set("Content-Type", "application/json")
		r, err := server.App().Test(req)
		if err != nil {
			t.Fatalf("Test: %v", err)
		}
		defer r.Body.Close()
		var out map[string]any
		json.NewDecoder(r.Body).Decode(&out)
		return r.StatusCode, out
	}

	// Valid signature → token, identity tg:42.
	valid := signLoginWidget("123:abc", map[string]string{
		"id": "42", "username": "bob", "auth_date": fmt.Sprint(time.Now().Unix()),
	})
	code, out := post(valid)
	if code != http.StatusOK || out["token"] == nil || out["id"] != "tg:42" {
		t.Fatalf("valid login = %d %+v, want 200 with token and tg:42", code, out)
	}

	// Tampered field → 401.
	valid["id"] = "99"
	if code, _ := post(valid); code != http.StatusUnauthorized {
		t.Fatalf("tampered login = %d, want 401", code)
	}
}

func TestStreamGating(t *testing.T) {
	get := func(server *Server, header string) int {
		req := httptest.NewRequest(http.MethodGet, "/api/stream", nil)
		if header != "" {
			req.Header.Set("Authorization", header)
		}
		resp, err := server.App().Test(req)
		if err != nil {
			t.Fatalf("Test: %v", err)
		}
		defer resp.Body.Close()
		return resp.StatusCode
	}

	// No broadcaster wired: disabled.
	if got := get(NewServer(testConfig(), nil, testLogger()), ""); got != http.StatusNotFound {
		t.Fatalf("no stream: status = %d, want 404", got)
	}

	// Broadcaster wired but no status token: still disabled (position data must
	// never be public).
	noToken := NewServer(testConfig(), nil, testLogger(), WithRealtime(realtime.NewBroadcaster(0)))
	if got := get(noToken, ""); got != http.StatusNotFound {
		t.Fatalf("no token: status = %d, want 404", got)
	}

	// Broadcaster + token, wrong bearer: rejected before the stream opens.
	withToken := NewServer(testConfigWith(t, map[string]string{"HTTP_STATUS_TOKEN": "tok"}), nil, testLogger(),
		WithRealtime(realtime.NewBroadcaster(0)))
	if got := get(withToken, "Bearer nope"); got != http.StatusUnauthorized {
		t.Fatalf("wrong token: status = %d, want 401", got)
	}

	// A wrong ?token= query param (the EventSource path) is also rejected.
	req := httptest.NewRequest(http.MethodGet, "/api/stream?token=nope", nil)
	resp, err := withToken.App().Test(req)
	if err != nil {
		t.Fatalf("Test: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong query token: status = %d, want 401", resp.StatusCode)
	}
}

func TestWebhookRateLimitPerClient(t *testing.T) {
	server := NewServer(testConfig(), signals.NewProcessor(signals.ProcessorConfig{Logger: testLogger()}), testLogger())
	const ip = "203.0.113.7"

	// The first webhookRatePerIP requests from one client must be allowed
	// (reaching the handler -> 401 on the wrong secret), proving the limiter
	// does not reject everything; only the next one is rate-limited.
	for i := 0; i < webhookRatePerIP; i++ {
		if got := postWebhook(t, server, ip); got == http.StatusTooManyRequests {
			t.Fatalf("request %d returned 429 before the per-client limit", i+1)
		}
	}
	if got := postWebhook(t, server, ip); got != http.StatusTooManyRequests {
		t.Fatalf("request %d status = %d, want 429 past the per-client limit", webhookRatePerIP+1, got)
	}
}

func TestWebhookGlobalRateCeiling(t *testing.T) {
	server := NewServer(testConfig(), signals.NewProcessor(signals.ProcessorConfig{Logger: testLogger()}), testLogger())

	// Every request uses a distinct Fly-Client-IP so the per-client limiter
	// never trips. The global ceiling must still bound total attempts, proving a
	// spoofed/rotated client key cannot grant unlimited webhook tries.
	for i := 0; i < webhookRateGlobal; i++ {
		if got := postWebhook(t, server, fmt.Sprintf("198.51.100.%d", i)); got == http.StatusTooManyRequests {
			t.Fatalf("request %d hit 429 before the global ceiling", i+1)
		}
	}
	if got := postWebhook(t, server, "198.51.100.250"); got != http.StatusTooManyRequests {
		t.Fatalf("request %d status = %d, want 429 at the global ceiling", webhookRateGlobal+1, got)
	}
}

func postWebhook(t *testing.T, server *Server, flyClientIP string) int {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/tradingview/webhook", bytes.NewBufferString(`{"secret":"wrong"}`))
	if flyClientIP != "" {
		req.Header.Set("Fly-Client-IP", flyClientIP)
	}
	resp, err := server.App().Test(req)
	if err != nil {
		t.Fatalf("webhook request: %v", err)
	}
	defer resp.Body.Close()
	return resp.StatusCode
}

func TestWebhookBodyLimit(t *testing.T) {
	server := NewServer(testConfig(), signals.NewProcessor(signals.ProcessorConfig{Logger: testLogger()}), testLogger())

	big := bytes.Repeat([]byte("a"), maxRequestBodyLimit+1)
	resp, err := server.App().Test(httptest.NewRequest(http.MethodPost, "/tradingview/webhook", bytes.NewReader(big)))
	if err != nil {
		// Fiber rejects oversized bodies at the transport layer; app.Test
		// surfaces that as an error instead of a 413 response. Either way the
		// request is rejected, which is the behavior under test.
		if !strings.Contains(err.Error(), "limit") {
			t.Fatalf("unexpected error: %v", err)
		}
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413 for oversized body", resp.StatusCode)
	}
}

func TestRegisterAndLogin(t *testing.T) {
	userSvc, err := users.NewService(users.NewMemoryRepository())
	if err != nil {
		t.Fatalf("users.NewService: %v", err)
	}
	server := NewServer(testConfig(), nil, testLogger(), WithUsers(userSvc))

	post := func(path, payload string) (int, []byte) {
		req := httptest.NewRequest(http.MethodPost, path, bytes.NewBufferString(payload))
		req.Header.Set("Content-Type", "application/json")
		resp, err := server.App().Test(req)
		if err != nil {
			t.Fatalf("Test %s: %v", path, err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, body
	}

	if status, _ := post("/api/register", `{"username":"alice","password":"supersecret"}`); status != http.StatusCreated {
		t.Fatalf("register status = %d, want 201", status)
	}
	if status, _ := post("/api/register", `{"username":"alice","password":"supersecret"}`); status != http.StatusConflict {
		t.Fatalf("duplicate register status = %d, want 409", status)
	}
	if status, body := post("/api/login", `{"username":"alice","password":"supersecret"}`); status != http.StatusOK {
		t.Fatalf("login status = %d (%s), want 200", status, body)
	}
	if status, _ := post("/api/login", `{"username":"alice","password":"wrong"}`); status != http.StatusUnauthorized {
		t.Fatalf("bad login status = %d, want 401", status)
	}
	// Response must never echo the password hash.
	_, body := post("/api/login", `{"username":"alice","password":"supersecret"}`)
	if bytes.Contains(body, []byte("password")) || bytes.Contains(body, []byte("hash")) {
		t.Fatalf("login response leaked password material: %s", body)
	}
}

type memCredRepo struct {
	m map[string]auth.BinanceCredential
}

func (r *memCredRepo) Save(_ context.Context, c auth.BinanceCredential) error {
	r.m[c.UserID] = c
	return nil
}
func (r *memCredRepo) Find(_ context.Context, id string) (auth.BinanceCredential, error) {
	c, ok := r.m[id]
	if !ok {
		return auth.BinanceCredential{}, auth.ErrNoCredential
	}
	return c, nil
}
func (r *memCredRepo) Remove(_ context.Context, id string) error { delete(r.m, id); return nil }

func TestCredentialEndpoints(t *testing.T) {
	userSvc, _ := users.NewService(users.NewMemoryRepository())
	tk := testTokenizer(t)
	keyring, err := auth.NewKeyring(map[string][]byte{"v1": bytes.Repeat([]byte("a"), auth.KeySize)}, "v1")
	if err != nil {
		t.Fatalf("keyring: %v", err)
	}
	credSvc, _ := auth.NewCredentialService(keyring, &memCredRepo{m: map[string]auth.BinanceCredential{}})
	server := NewServer(testConfig(), nil, testLogger(), WithUsers(userSvc), WithTokenizer(tk), WithCredentials(credSvc))

	do := func(method, path, token, payload string) (int, []byte) {
		var rdr *bytes.Buffer
		if payload != "" {
			rdr = bytes.NewBufferString(payload)
		} else {
			rdr = bytes.NewBuffer(nil)
		}
		req := httptest.NewRequest(method, path, rdr)
		req.Header.Set("Content-Type", "application/json")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := server.App().Test(req)
		if err != nil {
			t.Fatalf("Test %s %s: %v", method, path, err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, b
	}

	// Unauthenticated -> 401.
	if status, _ := do(http.MethodGet, "/api/credentials", "", ""); status != http.StatusUnauthorized {
		t.Fatalf("no-auth GET status = %d, want 401", status)
	}

	token, _ := tk.Issue("tg:123", "alice", "trader")

	if status, _ := do(http.MethodGet, "/api/credentials", token, ""); status != http.StatusOK {
		t.Fatalf("GET status = %d, want 200", status)
	}
	if status, _ := do(http.MethodPost, "/api/credentials", token, `{"api_key":"pubkey-abcd","api_secret":"secret-xyz","testnet":true}`); status != http.StatusCreated {
		t.Fatalf("POST status = %d, want 201", status)
	}
	status, body := do(http.MethodGet, "/api/credentials", token, "")
	if status != http.StatusOK {
		t.Fatalf("GET after store = %d", status)
	}
	// Must report configured + masked tail, never the secret.
	if !bytes.Contains(body, []byte(`"configured":true`)) || bytes.Contains(body, []byte("secret-xyz")) || bytes.Contains(body, []byte("pubkey-abcd")) {
		t.Fatalf("credential GET leaked or wrong: %s", body)
	}
}

func TestReportEndpoint(t *testing.T) {
	repo := journal.NewMemoryRepository()
	jsvc, err := journal.NewService(repo)
	if err != nil {
		t.Fatalf("journal.NewService: %v", err)
	}
	ctx := context.Background()
	_ = jsvc.Record(ctx, journal.Trade{ID: "1", UserID: 1, Symbol: "BTCUSDT", Outcome: journal.OutcomeWin, PnLUSDT: decimal.MustParse("2")})
	_ = jsvc.Record(ctx, journal.Trade{ID: "2", UserID: 1, Symbol: "BTCUSDT", Outcome: journal.OutcomeLoss, PnLUSDT: decimal.MustParse("-1")})

	server := NewServer(testConfig(), nil, testLogger(), WithReport(jsvc))
	resp, err := server.App().Test(httptest.NewRequest(http.MethodGet, "/api/report", nil))
	if err != nil {
		t.Fatalf("Test: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var report journal.Report
	if err := json.NewDecoder(resp.Body).Decode(&report); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if report.Trades != 2 || report.Wins != 1 || report.Losses != 1 ||
		report.WinRate.String() != "50" || report.TotalPnL.String() != "1" {
		t.Fatalf("report = %+v, want 2 trades 50%% +1", report)
	}

	// Disabled when no report service is wired.
	plain := NewServer(testConfig(), nil, testLogger())
	r2, _ := plain.App().Test(httptest.NewRequest(http.MethodGet, "/api/report", nil))
	defer r2.Body.Close()
	if r2.StatusCode != http.StatusNotImplemented {
		t.Fatalf("disabled report status = %d, want 501", r2.StatusCode)
	}
}

func TestRegisterDisabledWithoutUserService(t *testing.T) {
	server := NewServer(testConfig(), nil, testLogger())
	req := httptest.NewRequest(http.MethodPost, "/api/register", bytes.NewBufferString(`{"username":"a","password":"b"}`))
	resp, err := server.App().Test(req)
	if err != nil {
		t.Fatalf("Test: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501 when users disabled", resp.StatusCode)
	}
}

func testConfigWith(t *testing.T, overrides map[string]string) config.Config {
	t.Helper()
	values := map[string]string{
		"TELEGRAM_BOT_TOKEN":         "123:abc",
		"TELEGRAM_ALLOWED_USER_IDS":  "12345",
		"MONGODB_URI":                "mongodb+srv://mongo.example.invalid/tradebot",
		"MONGODB_DATABASE":           "tradebot",
		"HTTP_ENABLED":               "true",
		"TRADINGVIEW_ENABLED":        "true",
		"TRADINGVIEW_WEBHOOK_SECRET": "secret",
	}
	for k, v := range overrides {
		values[k] = v
	}
	cfg, err := config.LoadFromLookup(func(key string) (string, bool) {
		value, ok := values[key]
		return value, ok
	})
	if err != nil {
		t.Fatalf("config load: %v", err)
	}
	return cfg
}

func testConfig() config.Config {
	cfg, err := config.LoadFromLookup(func(key string) (string, bool) {
		values := map[string]string{
			"TELEGRAM_BOT_TOKEN":         "123:abc",
			"TELEGRAM_ALLOWED_USER_IDS":  "12345",
			"MONGODB_URI":                "mongodb+srv://mongo.example.invalid/tradebot",
			"MONGODB_DATABASE":           "tradebot",
			"HTTP_ENABLED":               "true",
			"TRADINGVIEW_ENABLED":        "true",
			"TRADINGVIEW_WEBHOOK_SECRET": "secret",
		}
		value, ok := values[key]
		return value, ok
	})
	if err != nil {
		panic(err)
	}
	return cfg
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
