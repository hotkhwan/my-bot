package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"bottrade/internal/auth"
	"bottrade/internal/marketdata"
	"bottrade/internal/orders"
	"bottrade/internal/strategy/annybasic"
)

func missionArmRuntimeEnv(marketDataURL string) map[string]string {
	return map[string]string{
		"MARKETDATA_BASE_URL":   marketDataURL,
		"ACCESS_OPEN":           "true",
		"CAMPAIGN_LIVE_ENABLED": "true",
		"DRY_RUN":               "false",
		"BINANCE_API_KEY":       "test-key",
		"BINANCE_API_SECRET":    "test-secret",
	}
}

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
		"symbol": "BTC", "capital": 100, "strategy": "ema", "duration": "15m", "leverage_use_pct": 50,
	})
	if code != http.StatusOK {
		t.Fatalf("prepare status = %d (%v)", code, out)
	}
	cid, _ := out["confirm_id"].(string)
	if cid == "" || !strings.Contains(out["output"].(string), "Review this live Mission") {
		t.Fatalf("prepare = %v, want a confirm_id and review text", out)
	}
	mission, _ := out["mission"].(map[string]any)
	if mission["duration"] != "15m" || mission["leverage"] != float64(10) ||
		mission["stop_loss"] == nil || mission["take_profit"] == nil {
		t.Fatalf("mission metadata = %v, want duration/leverage/SL/TP", mission)
	}

	// Confirm executes (dry-run, so it reports a DRY-RUN result — nothing real).
	_, conf := post("/api/confirm", token, map[string]any{"id": cid})
	if !strings.Contains(conf["output"].(string), "DRY-RUN") {
		t.Fatalf("confirm output = %v, want DRY-RUN", conf["output"])
	}
}

func TestMissionPrepareANNYBasicDoesNotFallbackToEMA(t *testing.T) {
	stub := stubKlines(t)
	cfg := testConfigWith(t, missionArmRuntimeEnv(stub.URL))
	tk, _ := auth.NewTokenizer(bytes.Repeat([]byte("k"), auth.MinSecretSize), 0)
	token, _ := tk.Issue("tg:468848033", "u", "user")
	orderSvc := orders.NewService(true, time.Minute, testLogger())
	server := NewServer(cfg, nil, testLogger(), WithTokenizer(tk), WithOrders(orderSvc))

	body, _ := json.Marshal(map[string]any{
		"symbol": "BTC", "capital": 100, "strategy": "anny_basic", "duration": "15m", "leverage_use_pct": 50,
	})
	req := httptest.NewRequest(http.MethodPost, "/api/mission/prepare", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, _ := server.App().Test(req)
	defer resp.Body.Close()
	var out map[string]any
	json.NewDecoder(resp.Body).Decode(&out)

	if out["confirm_id"] != nil || out["armed"] == nil {
		t.Fatalf("ANNY Basic without setup should arm, not stage an EMA fallback order: %v", out)
	}
	if output, _ := out["output"].(string); !strings.Contains(output, "Armed") {
		t.Fatalf("output = %q, want armed explanation", output)
	}
}

func TestMissionPrepareArmsWhenANNYBasicHasNoSetup(t *testing.T) {
	stub := stubKlines(t)
	cfg := testConfigWith(t, missionArmRuntimeEnv(stub.URL))
	tk, _ := auth.NewTokenizer(bytes.Repeat([]byte("k"), auth.MinSecretSize), 0)
	token, _ := tk.Issue("tg:468848033", "u", "user")
	store := newMemArmedMissions()
	server := NewServer(cfg, nil, testLogger(),
		WithTokenizer(tk), WithOrders(orders.NewService(true, time.Minute, testLogger())), WithArmedMissionStore(store))
	server.annyBasicDecider = func(context.Context, string, []marketdata.Candle) (annybasic.Decision, error) {
		return annybasic.Decision{Reason: "execution not aligned"}, nil
	}

	post := func() map[string]any {
		body, _ := json.Marshal(map[string]any{
			"symbol": "BTC", "capital": 100, "strategy": "anny_basic", "duration": "unlimited", "leverage_use_pct": 50,
		})
		req := httptest.NewRequest(http.MethodPost, "/api/mission/prepare", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+token)
		resp, _ := server.App().Test(req)
		defer resp.Body.Close()
		var out map[string]any
		json.NewDecoder(resp.Body).Decode(&out)
		return out
	}

	out := post()

	if out["confirm_id"] != nil {
		t.Fatalf("arm response staged a confirmation: %v", out)
	}
	armed, _ := out["armed"].(map[string]any)
	if armed["status"] != string(ArmedMissionStatusArmed) || armed["symbol"] != "BTCUSDT" {
		t.Fatalf("armed payload = %v, want armed BTCUSDT", armed)
	}
	if armed["duration"] != armedMissionUnlimitedDuration || armed["duration_window_seconds"] != float64(24*60*60) {
		t.Fatalf("armed duration = %v/%v, want 24h bounded unlimited window", armed["duration"], armed["duration_window_seconds"])
	}
	if output, _ := out["output"].(string); !strings.Contains(output, "24h maximum armed window") {
		t.Fatalf("output = %q, want bounded unlimited copy", output)
	}
	rows, err := store.ListActive(context.Background(), time.Now().UTC())
	if err != nil || len(rows) != 1 || rows[0].ArmReason != "execution not aligned" || rows[0].PurgeAt == nil {
		t.Fatalf("stored armed missions = %+v, err=%v", rows, err)
	}
	out = post()
	rearmed, _ := out["armed"].(map[string]any)
	if rearmed["id"] != armed["id"] {
		t.Fatalf("duplicate arm id = %v, want existing %v", rearmed["id"], armed["id"])
	}
	rows, err = store.ListActive(context.Background(), time.Now().UTC())
	if err != nil || len(rows) != 1 {
		t.Fatalf("stored armed missions after duplicate = %+v, err=%v", rows, err)
	}
}

func TestMissionPrepareArmRuntimeGated(t *testing.T) {
	stub := stubKlines(t)
	cfg := testConfigWith(t, map[string]string{"MARKETDATA_BASE_URL": stub.URL, "ACCESS_OPEN": "true"})
	tk, _ := auth.NewTokenizer(bytes.Repeat([]byte("k"), auth.MinSecretSize), 0)
	token, _ := tk.Issue("tg:468848033", "u", "user")
	store := newMemArmedMissions()
	server := NewServer(cfg, nil, testLogger(),
		WithTokenizer(tk), WithOrders(orders.NewService(true, time.Minute, testLogger())), WithArmedMissionStore(store))
	server.annyBasicDecider = func(context.Context, string, []marketdata.Candle) (annybasic.Decision, error) {
		return annybasic.Decision{Reason: "execution not aligned"}, nil
	}

	body, _ := json.Marshal(map[string]any{
		"symbol": "BTC", "capital": 100, "strategy": "anny_basic", "duration": "15m", "leverage_use_pct": 50,
	})
	req := httptest.NewRequest(http.MethodPost, "/api/mission/prepare", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, _ := server.App().Test(req)
	defer resp.Body.Close()
	var out map[string]any
	json.NewDecoder(resp.Body).Decode(&out)

	if out["armed"] != nil || out["confirm_id"] != nil {
		t.Fatalf("runtime-gated arm should not persist or stage: %v", out)
	}
	if output, _ := out["output"].(string); !strings.Contains(output, "testnet live-mission mode") {
		t.Fatalf("output = %q, want runtime gate explanation", output)
	}
	rows, err := store.ListActive(context.Background(), time.Now().UTC())
	if err != nil || len(rows) != 0 {
		t.Fatalf("stored armed missions = %+v, err=%v; want none", rows, err)
	}
}

func TestArmedMissionDisarmAndExpirePlaceNoOrder(t *testing.T) {
	store := newMemArmedMissions()
	now := time.Now().UTC()
	expiresAt := now.Add(time.Minute)
	mission := ArmedMission{
		ID: "arm_test", UserKey: "tg:7", UserID: 7, Symbol: "BTCUSDT", Strategy: annybasic.ID,
		Duration: "15m", Status: ArmedMissionStatusArmed, IdempotencyKey: "k1",
		ArmedAt: now, ExpiresAt: expiresAt, PurgeAt: armedMissionPurgeAt(expiresAt), CreatedAt: now, UpdatedAt: now,
	}
	if err := store.Save(context.Background(), mission); err != nil {
		t.Fatalf("save: %v", err)
	}
	disarmed, ok, err := store.Disarm(context.Background(), "tg:7", mission.ID, now.Add(time.Second))
	if err != nil || !ok || disarmed.Status != ArmedMissionStatusDisarmed {
		t.Fatalf("disarm = %+v ok=%v err=%v", disarmed, ok, err)
	}
	if disarmed.PurgeAt == nil || !disarmed.PurgeAt.Equal(expiresAt.Add(armedMissionRetention)) {
		t.Fatalf("disarm purge_at = %v, want expires_at + retention", disarmed.PurgeAt)
	}
	if _, changed, err := store.MarkExpired(context.Background(), mission.ID, now.Add(2*time.Minute)); err != nil || changed {
		t.Fatalf("expired disarmed mission changed=%v err=%v", changed, err)
	}

	expiring := mission
	expiring.ID = "arm_expire"
	expiring.Status = ArmedMissionStatusArmed
	expiring.IdempotencyKey = "k2"
	if err := store.Save(context.Background(), expiring); err != nil {
		t.Fatalf("save expiring: %v", err)
	}
	expired, changed, err := store.MarkExpired(context.Background(), expiring.ID, now.Add(2*time.Minute))
	if err != nil || !changed || expired.Status != ArmedMissionStatusExpired {
		t.Fatalf("expire = %+v changed=%v err=%v", expired, changed, err)
	}
	if expired.PurgeAt == nil || !expired.PurgeAt.Equal(expiresAt.Add(armedMissionRetention)) {
		t.Fatalf("expired purge_at = %v, want expires_at + retention", expired.PurgeAt)
	}
}

func TestArmedMissionRehydrateAndTriggerIdempotency(t *testing.T) {
	store := newMemArmedMissions()
	now := time.Now().UTC()
	mission := ArmedMission{
		ID: "arm_trigger", UserKey: "tg:7", UserID: 7, Symbol: "BTCUSDT", Strategy: annybasic.ID,
		Duration: "15m", Status: ArmedMissionStatusArmed, IdempotencyKey: "k1",
		ArmedAt: now, ExpiresAt: now.Add(time.Hour), PurgeAt: armedMissionPurgeAt(now.Add(time.Hour)), CreatedAt: now, UpdatedAt: now,
	}
	if err := store.Save(context.Background(), mission); err != nil {
		t.Fatalf("save: %v", err)
	}
	server := NewServer(testConfigWith(t, map[string]string{"ACCESS_OPEN": "true"}), nil, testLogger(), WithArmedMissionStore(store))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if got := server.startArmedMissionWatchers(ctx); got != 1 {
		t.Fatalf("rehydrated watchers = %d, want 1", got)
	}
	if got := server.startArmedMissionWatchers(ctx); got != 1 {
		t.Fatalf("rehydrate scan count = %d, want active row count even when watcher already running", got)
	}

	first, changed, err := store.MarkTriggered(context.Background(), mission.ID, "long", "CDC and QQE aligned", "", now.Add(time.Minute))
	if err != nil || !changed || first.Status != ArmedMissionStatusTriggered {
		t.Fatalf("first trigger = %+v changed=%v err=%v", first, changed, err)
	}
	if first.PurgeAt != nil {
		t.Fatalf("triggered mission purge_at = %v, want nil audit retention", first.PurgeAt)
	}
	second, changed, err := store.MarkTriggered(context.Background(), mission.ID, "long", "again", "", now.Add(2*time.Minute))
	if err != nil || changed || second.Status != ArmedMissionStatusTriggered {
		t.Fatalf("second trigger changed=%v second=%+v err=%v, want no double trigger", changed, second, err)
	}
}

func TestArmedMissionWatcherObserveOnlyAndGated(t *testing.T) {
	store := newMemArmedMissions()
	now := time.Now().UTC()
	mission := ArmedMission{
		ID: "arm_watch", UserKey: "tg:7", UserID: 7, Symbol: "BTCUSDT", Strategy: annybasic.ID,
		Duration: "15m", Status: ArmedMissionStatusArmed, IdempotencyKey: "k1",
		ArmedAt: now, ExpiresAt: now.Add(time.Hour), PurgeAt: armedMissionPurgeAt(now.Add(time.Hour)), CreatedAt: now, UpdatedAt: now,
	}
	if err := store.Save(context.Background(), mission); err != nil {
		t.Fatalf("save: %v", err)
	}
	server := NewServer(testConfigWith(t, map[string]string{"ACCESS_OPEN": "true"}), nil, testLogger(), WithArmedMissionStore(store))
	calls := 0
	server.annyBasicDecider = func(context.Context, string, []marketdata.Candle) (annybasic.Decision, error) {
		calls++
		return annybasic.Decision{Side: annybasic.SideLong, Reason: "CDC and QQE aligned"}, nil
	}

	// Default config is dry-run and CAMPAIGN_LIVE_ENABLED=false, so the watcher
	// must not even evaluate a would-be trigger.
	if _, done, err := server.checkArmedMission(context.Background(), mission.ID, now.Add(time.Minute)); err != nil || done || calls != 0 {
		t.Fatalf("gated check done=%v calls=%d err=%v, want gated/no-op", done, calls, err)
	}

	server.cfg.App.CampaignLiveEnabled = true
	server.cfg.App.DryRun = false
	triggered, done, err := server.checkArmedMission(context.Background(), mission.ID, now.Add(2*time.Minute))
	if err != nil || !done || triggered.Status != ArmedMissionStatusTriggered || calls != 1 {
		t.Fatalf("observe-only trigger = %+v done=%v calls=%d err=%v", triggered, done, calls, err)
	}
	if triggered.TriggeredConfirmationID != "" {
		t.Fatalf("Phase A must not prepare an order, got confirmation %q", triggered.TriggeredConfirmationID)
	}
	if triggered.PurgeAt != nil {
		t.Fatalf("triggered mission purge_at = %v, want nil audit retention", triggered.PurgeAt)
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

func TestMissionLeverageFromUsePercent(t *testing.T) {
	cases := []struct{ use, max, want int }{
		{25, 20, 5}, {100, 20, 20}, {0, 20, 5}, {-5, 20, 5},
		{250, 20, 20}, {1, 20, 1}, {50, 50, 25},
	}
	for _, c := range cases {
		if got := missionLeverageFor(c.use, c.max); got != c.want {
			t.Errorf("missionLeverageFor(%d, %d) = %d, want %d", c.use, c.max, got, c.want)
		}
	}
}

func TestPlanDuration(t *testing.T) {
	for raw, want := range map[string]time.Duration{
		"15m": 15 * time.Minute, "1h": time.Hour, "48h": 48 * time.Hour, "1w": 7 * 24 * time.Hour,
	} {
		if got := planDuration(raw); got != want {
			t.Errorf("planDuration(%q) = %s, want %s", raw, got, want)
		}
	}
}
