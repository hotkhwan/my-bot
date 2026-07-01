package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"bottrade/internal/auth"
	"bottrade/internal/decimal"
	"bottrade/internal/domain"
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

type missionTestExecutor struct {
	calls         int
	confirmations []orders.Confirmation
}

func (e *missionTestExecutor) Execute(_ context.Context, confirmation orders.Confirmation) (orders.ExecutionResult, error) {
	e.calls++
	e.confirmations = append(e.confirmations, confirmation)
	return orders.ExecutionResult{
		Mode:          "binance_testnet",
		ClientOrderID: "testnet-" + confirmation.ID,
		Message:       "TESTNET accepted",
	}, nil
}

type scheduledCloseExecutor struct {
	positions     []domain.Position
	err           error
	calls         int
	confirmations []orders.Confirmation
}

func (e *scheduledCloseExecutor) Execute(_ context.Context, confirmation orders.Confirmation) (orders.ExecutionResult, error) {
	e.calls++
	e.confirmations = append(e.confirmations, confirmation)
	if e.err != nil {
		return orders.ExecutionResult{}, e.err
	}
	return orders.ExecutionResult{
		Mode:          "binance_testnet",
		ClientOrderID: "timed-close-" + confirmation.ID,
		Message:       "TESTNET close accepted",
	}, nil
}

func (e *scheduledCloseExecutor) Positions(context.Context) ([]domain.Position, error) {
	return e.positions, nil
}

type missionTestProvider struct {
	executor orders.Executor
	found    bool
	err      error
}

func (p missionTestProvider) ExecutorFor(context.Context, string) (orders.Executor, bool, error) {
	return p.executor, p.found, p.err
}

func missionOrderService(exec orders.Executor) *orders.Service {
	return orders.NewServiceWithRepositories(time.Minute, orders.DryRunExecutor{DryRun: true}, orders.ServiceDependencies{
		ExecutorProvider: missionTestProvider{executor: exec, found: true},
	}, testLogger())
}

func testCredentialService(t *testing.T, subject string, testnet bool) *auth.CredentialService {
	t.Helper()
	keyring, err := auth.NewKeyring(map[string][]byte{"v1": bytes.Repeat([]byte("a"), auth.KeySize)}, "v1")
	if err != nil {
		t.Fatalf("keyring: %v", err)
	}
	credSvc, err := auth.NewCredentialService(keyring, &memCredRepo{m: map[string][]auth.BinanceCredential{}})
	if err != nil {
		t.Fatalf("credential service: %v", err)
	}
	profile := "testnet"
	if !testnet {
		profile = "mainnet"
	}
	if err := credSvc.StoreProfile(context.Background(), subject, profile,
		auth.BinanceKeys{APIKey: "k-abcd1234", APISecret: "s-abcd1234", Testnet: testnet}); err != nil {
		t.Fatalf("store profile: %v", err)
	}
	if err := credSvc.SetActive(context.Background(), subject, profile); err != nil {
		t.Fatalf("set active: %v", err)
	}
	return credSvc
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

func TestMissionPrepareGateOffOmitsTimedClosePromise(t *testing.T) {
	stub := stubKlines(t)
	cfg := testConfigWith(t, map[string]string{"MARKETDATA_BASE_URL": stub.URL, "ACCESS_OPEN": "true"})
	tk, _ := auth.NewTokenizer(bytes.Repeat([]byte("k"), auth.MinSecretSize), 0)
	token, _ := tk.Issue("tg:468848033", "u", "user")
	closeStore := newMemScheduledCloses()
	server := NewServer(cfg, nil, testLogger(),
		WithTokenizer(tk), WithOrders(orders.NewService(true, time.Minute, testLogger())),
		WithScheduledCloseStore(closeStore))

	body, _ := json.Marshal(map[string]any{
		"symbol": "BTC", "capital": 100, "strategy": "ema", "duration": "15m", "leverage_use_pct": 50,
	})
	req := httptest.NewRequest(http.MethodPost, "/api/mission/prepare", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, _ := server.App().Test(req)
	defer resp.Body.Close()
	var out map[string]any
	json.NewDecoder(resp.Body).Decode(&out)

	output, _ := out["output"].(string)
	if out["confirm_id"] == nil {
		t.Fatalf("prepare = %v, want staged dry-run mission", out)
	}
	if strings.Contains(strings.ToLower(output), "timed close") {
		t.Fatalf("gate-off output promised timed close: %q", output)
	}
	if len(closeStore.rows) != 0 {
		t.Fatalf("scheduled closes = %+v, want none while runtime gate is off", closeStore.rows)
	}
}

func TestMissionConfirmRequiresPerUserExecutorWhenRuntimeEnabled(t *testing.T) {
	stub := stubKlines(t)
	cfg := testConfigWith(t, missionArmRuntimeEnv(stub.URL))
	tk, _ := auth.NewTokenizer(bytes.Repeat([]byte("k"), auth.MinSecretSize), 0)
	token, _ := tk.Issue("tg:468848033", "u", "user")
	fallbackExec := &missionTestExecutor{}
	orderSvc := orders.NewServiceWithRepositories(time.Minute, fallbackExec, orders.ServiceDependencies{
		ExecutorProvider: missionTestProvider{found: false},
	}, testLogger())
	server := NewServer(cfg, nil, testLogger(),
		WithTokenizer(tk), WithOrders(orderSvc),
		WithCredentials(testCredentialService(t, "tg:468848033", true)),
		WithScheduledCloseStore(newMemScheduledCloses()))

	post := func(path string, body any) map[string]any {
		b, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(b))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+token)
		resp, _ := server.App().Test(req)
		defer resp.Body.Close()
		var out map[string]any
		json.NewDecoder(resp.Body).Decode(&out)
		return out
	}

	prepared := post("/api/mission/prepare", map[string]any{
		"symbol": "BTC", "capital": 100, "strategy": "ema", "duration": "15m", "leverage_use_pct": 50,
	})
	cid, _ := prepared["confirm_id"].(string)
	if cid == "" {
		t.Fatalf("prepare = %v, want confirmation id", prepared)
	}
	confirmed := post("/api/confirm", map[string]any{"id": cid})
	if output, _ := confirmed["output"].(string); !strings.Contains(output, "active per-user executor is required") {
		t.Fatalf("confirm output = %q, want required per-user executor failure", output)
	}
	if fallbackExec.calls != 0 {
		t.Fatalf("fallback executor calls = %d, want 0", fallbackExec.calls)
	}
}

func TestMissionPrepareANNYBasicDoesNotFallbackToEMA(t *testing.T) {
	stub := stubKlines(t)
	cfg := testConfigWith(t, missionArmRuntimeEnv(stub.URL))
	tk, _ := auth.NewTokenizer(bytes.Repeat([]byte("k"), auth.MinSecretSize), 0)
	token, _ := tk.Issue("tg:468848033", "u", "user")
	orderSvc := orders.NewService(true, time.Minute, testLogger())
	server := NewServer(cfg, nil, testLogger(), WithTokenizer(tk), WithOrders(orderSvc),
		WithCredentials(testCredentialService(t, "tg:468848033", true)))

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
		WithTokenizer(tk), WithOrders(orders.NewService(true, time.Minute, testLogger())),
		WithCredentials(testCredentialService(t, "tg:468848033", true)), WithArmedMissionStore(store))
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

func TestArmedMissionExtendWindowAndExpireStale(t *testing.T) {
	store := newMemArmedMissions()
	now := time.Now().UTC()
	shortExp := now.Add(15 * time.Minute)
	mission := ArmedMission{
		ID: "arm_extend", UserKey: "tg:7", UserID: 7, Symbol: "BTCUSDT", Strategy: annybasic.ID,
		Duration: "15m", DurationWindowSeconds: 900, Status: ArmedMissionStatusArmed, IdempotencyKey: "k1",
		ArmedAt: now, ExpiresAt: shortExp, PurgeAt: armedMissionPurgeAt(shortExp), CreatedAt: now, UpdatedAt: now,
	}
	if err := store.Save(context.Background(), mission); err != nil {
		t.Fatalf("save: %v", err)
	}
	// Re-arm with a longer (24h) window extends the existing mission.
	longExp := now.Add(24 * time.Hour)
	extended, ok, err := store.ExtendWindow(context.Background(), mission.ID, "24h", 86400, longExp, armedMissionPurgeAt(longExp), now.Add(time.Second))
	if err != nil || !ok {
		t.Fatalf("extend = ok:%v err:%v", ok, err)
	}
	if !extended.ExpiresAt.Equal(longExp) || extended.Duration != "24h" || extended.DurationWindowSeconds != 86400 {
		t.Fatalf("extended window = %+v, want 24h/longExp", extended)
	}
	if extended.PurgeAt == nil || !extended.PurgeAt.Equal(longExp.Add(armedMissionRetention)) {
		t.Fatalf("extended purge_at = %v, want longExp + retention", extended.PurgeAt)
	}

	// A stale armed orphan (window already passed) plus a live one: ExpireStale
	// sweeps only the stale orphan.
	stale := mission
	stale.ID = "arm_stale"
	stale.IdempotencyKey = "k2"
	stale.ExpiresAt = now.Add(-time.Minute)
	if err := store.Save(context.Background(), stale); err != nil {
		t.Fatalf("save stale: %v", err)
	}
	swept, err := store.ExpireStale(context.Background(), now)
	if err != nil || swept != 1 {
		t.Fatalf("ExpireStale swept=%d err=%v, want 1", swept, err)
	}
	got, _, _ := store.Get(context.Background(), stale.ID)
	if got.Status != ArmedMissionStatusExpired {
		t.Fatalf("stale orphan status = %q, want expired", got.Status)
	}
	live, _, _ := store.Get(context.Background(), mission.ID)
	if live.Status != ArmedMissionStatusArmed {
		t.Fatalf("live mission status = %q, want still armed", live.Status)
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

func TestArmedMissionWatcherAutoConfirmsOnceAndGated(t *testing.T) {
	stub := stubKlines(t)
	store := newMemArmedMissions()
	now := time.Now().UTC()
	mission := ArmedMission{
		ID: "arm_watch", UserKey: "tg:7", UserID: 7, Symbol: "BTCUSDT", Strategy: annybasic.ID,
		CapitalUSDT: "100", LeverageUsePct: 50, Duration: "15m", Status: ArmedMissionStatusArmed, IdempotencyKey: "k1",
		ArmedAt: now, ExpiresAt: now.Add(time.Hour), PurgeAt: armedMissionPurgeAt(now.Add(time.Hour)), CreatedAt: now, UpdatedAt: now,
	}
	if err := store.Save(context.Background(), mission); err != nil {
		t.Fatalf("save: %v", err)
	}
	exec := &missionTestExecutor{}
	server := NewServer(testConfigWith(t, map[string]string{"MARKETDATA_BASE_URL": stub.URL, "ACCESS_OPEN": "true"}), nil, testLogger(),
		WithOrders(missionOrderService(exec)), WithCredentials(testCredentialService(t, "tg:7", true)), WithArmedMissionStore(store))
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
		t.Fatalf("auto trigger = %+v done=%v calls=%d err=%v", triggered, done, calls, err)
	}
	if triggered.TriggeredConfirmationID == "" || exec.calls != 1 {
		t.Fatalf("Phase B should prepare+confirm once, confirmation=%q calls=%d", triggered.TriggeredConfirmationID, exec.calls)
	}
	if triggered.PurgeAt != nil {
		t.Fatalf("triggered mission purge_at = %v, want nil audit retention", triggered.PurgeAt)
	}
	if len(exec.confirmations) != 1 || exec.confirmations[0].IdempotencyKey != mission.IdempotencyKey {
		t.Fatalf("confirmation idempotency = %+v, want armed key %q", exec.confirmations, mission.IdempotencyKey)
	}
}

func TestArmedMissionWatcherUsesOneMinuteExecutionForLongWindow(t *testing.T) {
	var intervals []string
	var limits []string
	market := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/fapi/v1/klines" {
			http.NotFound(w, r)
			return
		}
		intervals = append(intervals, r.URL.Query().Get("interval"))
		limits = append(limits, r.URL.Query().Get("limit"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[[0,"100","102","99","101","1",60000,"0",0,"0","0","0"]]`))
	}))
	t.Cleanup(market.Close)

	store := newMemArmedMissions()
	now := time.Now().UTC()
	mission := ArmedMission{
		ID: "arm_watch_long", UserKey: "tg:7", UserID: 7, Symbol: "BTCUSDT", Strategy: annybasic.ID,
		CapitalUSDT: "100", LeverageUsePct: 50, Duration: armedMissionUnlimitedDuration, Status: ArmedMissionStatusArmed, IdempotencyKey: "k-long",
		ArmedAt: now, ExpiresAt: now.Add(time.Hour), PurgeAt: armedMissionPurgeAt(now.Add(time.Hour)), CreatedAt: now, UpdatedAt: now,
	}
	if err := store.Save(context.Background(), mission); err != nil {
		t.Fatalf("save: %v", err)
	}
	exec := &missionTestExecutor{}
	server := NewServer(testConfigWith(t, missionArmRuntimeEnv(market.URL)), nil, testLogger(),
		WithOrders(missionOrderService(exec)), WithCredentials(testCredentialService(t, "tg:7", true)), WithArmedMissionStore(store))
	calls := 0
	candleCount := 0
	lastClose := 0.0
	server.annyBasicDecider = func(_ context.Context, _ string, candles []marketdata.Candle) (annybasic.Decision, error) {
		calls++
		candleCount = len(candles)
		if len(candles) > 0 {
			lastClose = candles[len(candles)-1].Close
		}
		return annybasic.Decision{Side: annybasic.SideLong, Reason: "CDC and QQE aligned"}, nil
	}

	triggered, done, err := server.checkArmedMission(context.Background(), mission.ID, now.Add(time.Minute))
	if err != nil || !done || triggered.Status != ArmedMissionStatusTriggered || calls != 1 || exec.calls != 1 {
		t.Fatalf("auto trigger = %+v done=%v calls=%d exec=%d err=%v", triggered, done, calls, exec.calls, err)
	}
	if candleCount != 1 || lastClose != 101 {
		t.Fatalf("decider candles = count:%d close:%v, want one 1m candle closing 101", candleCount, lastClose)
	}
	if len(intervals) != 1 || intervals[0] != armedMissionExecutionInterval {
		t.Fatalf("market intervals = %v, want only %q for armed ANNY Basic execution", intervals, armedMissionExecutionInterval)
	}
	if len(limits) != 1 || limits[0] != "120" {
		t.Fatalf("market limits = %v, want 120 execution candles", limits)
	}
	if got := armedMissionPollInterval(armedMissionUnlimitedDuration); got != time.Minute {
		t.Fatalf("long-window armed poll interval = %v, want 1m execution cadence", got)
	}
}

func TestScheduledCloseClaimSingleWinnerAndRetention(t *testing.T) {
	store := newMemScheduledCloses()
	now := time.Now().UTC()
	close := ScheduledClose{
		ID: "close_claim", UserKey: "tg:7", UserID: 7, Symbol: "BTCUSDT",
		DueAt: now.Add(-time.Minute), Status: ScheduledCloseStatusPending, CreatedAt: now, UpdatedAt: now,
	}
	if err := store.Save(context.Background(), close); err != nil {
		t.Fatalf("save: %v", err)
	}
	first, claimed, err := store.ClaimDue(context.Background(), close.ID, now)
	if err != nil || !claimed || first.Status != ScheduledCloseStatusExecuting {
		t.Fatalf("first claim = %+v claimed=%v err=%v", first, claimed, err)
	}
	second, claimed, err := store.ClaimDue(context.Background(), close.ID, now)
	if err != nil || claimed || second.Status != ScheduledCloseStatusExecuting {
		t.Fatalf("second claim = %+v claimed=%v err=%v, want no double claim", second, claimed, err)
	}
	done, changed, err := store.MarkDone(context.Background(), close.ID, "confirm_1", "closed", now.Add(time.Second))
	if err != nil || !changed || done.Status != ScheduledCloseStatusDone || done.PurgeAt == nil {
		t.Fatalf("mark done = %+v changed=%v err=%v, want retained terminal row", done, changed, err)
	}
}

func TestScheduledCloseReclaimsStaleExecutingAfterRestart(t *testing.T) {
	store := newMemScheduledCloses()
	now := time.Now().UTC()
	stale := ScheduledClose{
		ID: "close_stale", UserKey: "tg:7", UserID: 7, Symbol: "BTCUSDT",
		DueAt:     now.Add(-time.Minute),
		Status:    ScheduledCloseStatusExecuting,
		CreatedAt: now.Add(-time.Hour),
		UpdatedAt: now.Add(-ScheduledCloseClaimTimeout - time.Second),
	}
	fresh := stale
	fresh.ID = "close_fresh"
	fresh.UpdatedAt = now.Add(-ScheduledCloseClaimTimeout + time.Second)
	if err := store.Save(context.Background(), stale); err != nil {
		t.Fatalf("save stale: %v", err)
	}
	if err := store.Save(context.Background(), fresh); err != nil {
		t.Fatalf("save fresh: %v", err)
	}
	due, err := store.ListDue(context.Background(), now, 10)
	if err != nil || len(due) != 1 || due[0].ID != stale.ID {
		t.Fatalf("due rows = %+v err=%v, want only stale executing", due, err)
	}
	claimed, ok, err := store.ClaimDue(context.Background(), stale.ID, now)
	if err != nil || !ok || !claimed.UpdatedAt.Equal(now) {
		t.Fatalf("reclaim = %+v ok=%v err=%v, want updated stale claim", claimed, ok, err)
	}
	if _, ok, err := store.ClaimDue(context.Background(), fresh.ID, now); err != nil || ok {
		t.Fatalf("fresh executing claim ok=%v err=%v, want no reclaim", ok, err)
	}
}

func TestScheduledCloseGateClosedSkipsNoOrder(t *testing.T) {
	store := newMemScheduledCloses()
	now := time.Now().UTC()
	if err := store.Save(context.Background(), ScheduledClose{
		ID: "close_gated", UserKey: "tg:7", UserID: 7, Symbol: "BTCUSDT",
		DueAt: now.Add(-time.Minute), Status: ScheduledCloseStatusPending, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("save: %v", err)
	}
	exec := &scheduledCloseExecutor{positions: []domain.Position{
		{Symbol: "BTCUSDT", Amount: decimal.MustParse("0.01")},
	}}
	server := NewServer(testConfigWith(t, map[string]string{"ACCESS_OPEN": "true"}), nil, testLogger(),
		WithOrders(missionOrderService(exec)), WithScheduledCloseStore(store),
		WithCredentials(testCredentialService(t, "tg:7", true)))

	handled, err := server.runDueScheduledCloses(context.Background(), now)
	if err != nil || handled != 1 {
		t.Fatalf("run due handled=%d err=%v, want one skipped row", handled, err)
	}
	got := store.rows["close_gated"]
	if got.Status != ScheduledCloseStatusSkipped || !strings.Contains(got.Reason, "gate closed") || exec.calls != 0 {
		t.Fatalf("scheduled close = %+v calls=%d, want skipped/no order", got, exec.calls)
	}
}

func TestScheduledCloseNoOpenPositionDone(t *testing.T) {
	store := newMemScheduledCloses()
	now := time.Now().UTC()
	if err := store.Save(context.Background(), ScheduledClose{
		ID: "close_empty", UserKey: "tg:7", UserID: 7, Symbol: "BTCUSDT",
		DueAt: now.Add(-time.Minute), Status: ScheduledCloseStatusPending, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("save: %v", err)
	}
	exec := &scheduledCloseExecutor{}
	server := NewServer(testConfigWith(t, missionArmRuntimeEnv("")), nil, testLogger(),
		WithOrders(missionOrderService(exec)), WithScheduledCloseStore(store),
		WithCredentials(testCredentialService(t, "tg:7", true)))

	handled, err := server.runDueScheduledCloses(context.Background(), now)
	if err != nil || handled != 1 {
		t.Fatalf("run due handled=%d err=%v, want one done row", handled, err)
	}
	got := store.rows["close_empty"]
	if got.Status != ScheduledCloseStatusDone || got.ConfirmationID != "" || got.Reason != "no matching open position" || exec.calls != 0 {
		t.Fatalf("scheduled close = %+v calls=%d, want done/no-op", got, exec.calls)
	}
}

func TestScheduledCloseClosesOpenPositionOnce(t *testing.T) {
	store := newMemScheduledCloses()
	now := time.Now().UTC()
	if err := store.Save(context.Background(), ScheduledClose{
		ID: "close_open", UserKey: "tg:7", UserID: 7, Symbol: "BTCUSDT",
		DueAt: now.Add(-time.Minute), Status: ScheduledCloseStatusPending, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("save: %v", err)
	}
	exec := &scheduledCloseExecutor{positions: []domain.Position{
		{Symbol: "BTCUSDT", Amount: decimal.MustParse("0.01")},
	}}
	server := NewServer(testConfigWith(t, missionArmRuntimeEnv("")), nil, testLogger(),
		WithOrders(missionOrderService(exec)), WithScheduledCloseStore(store),
		WithCredentials(testCredentialService(t, "tg:7", true)))

	handled, err := server.runDueScheduledCloses(context.Background(), now)
	if err != nil || handled != 1 {
		t.Fatalf("run due handled=%d err=%v, want one close", handled, err)
	}
	got := store.rows["close_open"]
	if got.Status != ScheduledCloseStatusDone || got.ConfirmationID == "" || got.Reason != "closed at plan deadline" || exec.calls != 1 {
		t.Fatalf("scheduled close = %+v calls=%d, want one confirmed close", got, exec.calls)
	}
	if len(exec.confirmations) != 1 || exec.confirmations[0].Intent.Type != domain.IntentClose ||
		exec.confirmations[0].Intent.Close == nil || !exec.confirmations[0].Intent.Close.All {
		t.Fatalf("confirmation = %+v, want close-all intent", exec.confirmations)
	}
	handled, err = server.runDueScheduledCloses(context.Background(), now.Add(time.Second))
	if err != nil || handled != 0 || exec.calls != 1 {
		t.Fatalf("second run handled=%d err=%v calls=%d, want no double close", handled, err, exec.calls)
	}
}

// A LONG mission's timed close must not flatten an unrelated SHORT the user opened
// on the same symbol after the mission position was already closed by TP/SL.
func TestScheduledCloseSkipsOppositeSidePosition(t *testing.T) {
	store := newMemScheduledCloses()
	now := time.Now().UTC()
	if err := store.Save(context.Background(), ScheduledClose{
		ID: "close_side", UserKey: "tg:7", UserID: 7, Symbol: "BTCUSDT", Side: "long",
		DueAt: now.Add(-time.Minute), Status: ScheduledCloseStatusPending, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("save: %v", err)
	}
	exec := &scheduledCloseExecutor{positions: []domain.Position{
		{Symbol: "BTCUSDT", Side: domain.PositionSideShort, Amount: decimal.MustParse("0.02")},
	}}
	server := NewServer(testConfigWith(t, missionArmRuntimeEnv("")), nil, testLogger(),
		WithOrders(missionOrderService(exec)), WithScheduledCloseStore(store),
		WithCredentials(testCredentialService(t, "tg:7", true)))

	handled, err := server.runDueScheduledCloses(context.Background(), now)
	if err != nil || handled != 1 {
		t.Fatalf("run due handled=%d err=%v, want one handled row", handled, err)
	}
	got := store.rows["close_side"]
	if got.Status != ScheduledCloseStatusDone || got.Reason != "no matching open position" || exec.calls != 0 {
		t.Fatalf("scheduled close = %+v calls=%d, want done no-op leaving the opposite-side position untouched", got, exec.calls)
	}
}

func TestArmedMissionCancelsScheduledCloseWhenEntryConfirmFails(t *testing.T) {
	armedStore := newMemArmedMissions()
	closeStore := newMemScheduledCloses()
	now := time.Now().UTC()
	mission := ArmedMission{
		ID: "arm_fail", UserKey: "tg:7", UserID: 7, Symbol: "BTCUSDT", Strategy: annybasic.ID,
		CapitalUSDT: "100", LeverageUsePct: 50, Duration: "15m", Status: ArmedMissionStatusArmed, IdempotencyKey: "k-fail",
		ArmedAt: now, ExpiresAt: now.Add(time.Hour), PurgeAt: armedMissionPurgeAt(now.Add(time.Hour)), CreatedAt: now, UpdatedAt: now,
	}
	if err := armedStore.Save(context.Background(), mission); err != nil {
		t.Fatalf("save armed mission: %v", err)
	}
	exec := &scheduledCloseExecutor{err: errors.New("exchange rejected")}
	server := NewServer(testConfigWith(t, missionArmRuntimeEnv("")), nil, testLogger(),
		WithOrders(missionOrderService(exec)), WithCredentials(testCredentialService(t, "tg:7", true)),
		WithArmedMissionStore(armedStore), WithScheduledCloseStore(closeStore))

	_, done, err := server.triggerArmedMission(context.Background(), mission, 100, annybasic.Decision{
		Side: annybasic.SideLong, MaxLeverage: 20, Reason: "CDC and QQE aligned",
	}, now.Add(time.Minute))
	if err == nil || !done || exec.calls != 1 {
		t.Fatalf("trigger done=%v err=%v calls=%d, want failed confirm after one attempt", done, err, exec.calls)
	}
	if len(closeStore.rows) != 1 {
		t.Fatalf("scheduled close rows = %+v, want one cancelled row", closeStore.rows)
	}
	for _, row := range closeStore.rows {
		if row.Status != ScheduledCloseStatusCancelled || !strings.Contains(row.Reason, "entry confirm failed") {
			t.Fatalf("scheduled close after failed entry = %+v, want cancelled", row)
		}
	}
}

func TestScheduleTimedMissionClosePersistsAwaitingThenActivates(t *testing.T) {
	store := newMemScheduledCloses()
	server := NewServer(testConfigWith(t, missionArmRuntimeEnv("")), nil, testLogger(), WithScheduledCloseStore(store))
	// At prepare the close is persisted AWAITING-ENTRY (poller-invisible), keyed by
	// the entry confirmation id, with no due_at yet.
	close, err := server.scheduleTimedMissionClose(timedMission{UserID: 7, Symbol: "BTC", Duration: 15 * time.Minute}, "conf-123")
	if err != nil {
		t.Fatalf("schedule timed close: %v", err)
	}
	if close.Status != ScheduledCloseStatusAwaitingEntry || close.EntryConfirmationID != "conf-123" ||
		close.WindowSeconds != 900 || !close.DueAt.IsZero() {
		t.Fatalf("scheduled close = %+v, want awaiting_entry/conf-123/900s/no-due", close)
	}
	// The poller must NOT treat an awaiting-entry row as due — even long after it was
	// created — because the entry it protects has not confirmed (crash-before-entry
	// safety guard). Poll at "now" so the stale-awaiting sweep (30m) doesn't apply.
	if handled, err := server.runDueScheduledCloses(context.Background(), time.Now().UTC()); err != nil || handled != 0 {
		t.Fatalf("awaiting close handled=%d err=%v, want untouched", handled, err)
	}
	if store.rows[close.ID].Status != ScheduledCloseStatusAwaitingEntry {
		t.Fatalf("awaiting close was activated/closed by the poller: %+v", store.rows[close.ID])
	}
	// After the entry confirms, activation arms the poller with due_at = now + window.
	before := time.Now().UTC()
	server.activateScheduledClose(context.Background(), "conf-123")
	got := store.rows[close.ID]
	if got.Status != ScheduledCloseStatusPending || got.DueAt.Before(before.Add(15*time.Minute)) {
		t.Fatalf("activated close = %+v, want pending with due_at ~= now+15m", got)
	}
}

func TestScheduledCloseReconcileFromConfirmationStatus(t *testing.T) {
	store := newMemScheduledCloses()
	now := time.Now().UTC()
	exec := &missionTestExecutor{}
	server := NewServer(testConfigWith(t, missionArmRuntimeEnv("")), nil, testLogger(),
		WithOrders(missionOrderService(exec)), WithScheduledCloseStore(store),
		WithCredentials(testCredentialService(t, "tg:7", true)))
	intent := func() domain.Intent {
		return domain.Intent{Type: domain.IntentClose, Close: &domain.CloseIntent{
			Symbol: "BTCUSDT", All: true, ResolvedPercent: decimal.NewFromInt(100),
		}}
	}
	awaiting := func(id, entryConf string, created time.Time) {
		_ = store.Save(context.Background(), ScheduledClose{ID: id, UserID: 7, Symbol: "BTCUSDT",
			EntryConfirmationID: entryConf, Status: ScheduledCloseStatusAwaitingEntry, WindowSeconds: 900,
			CreatedAt: created, UpdatedAt: created})
	}

	// executed entry → reconciler must ACTIVATE its close (recovers a crash between
	// confirm-success and activation).
	execConf, err := server.orders.Prepare(context.Background(), 7, intent())
	if err != nil {
		t.Fatalf("prepare exec: %v", err)
	}
	if _, err := server.orders.ConfirmWithRequiredUserExecutor(context.Background(), 7, execConf.ID); err != nil {
		t.Fatalf("confirm exec: %v", err)
	}
	awaiting("c-exec", execConf.ID, now)

	// cancelled entry → reconciler must CANCEL its close.
	cancelledConf, _ := server.orders.Prepare(context.Background(), 7, intent())
	_ = server.orders.Cancel(context.Background(), 7, cancelledConf.ID)
	awaiting("c-cancelled", cancelledConf.ID, now)

	// unknown confirmation, old → cancel; unknown, fresh → leave.
	awaiting("c-unknown-old", "missing-old", now.Add(-time.Hour))
	awaiting("c-unknown-fresh", "missing-fresh", now)

	server.reconcileAwaitingCloses(context.Background(), now)

	if got := store.rows["c-exec"].Status; got != ScheduledCloseStatusPending {
		t.Fatalf("executed-entry close = %q, want activated pending", got)
	}
	if got := store.rows["c-cancelled"].Status; got != ScheduledCloseStatusCancelled {
		t.Fatalf("cancelled-entry close = %q, want cancelled", got)
	}
	if got := store.rows["c-unknown-old"].Status; got != ScheduledCloseStatusCancelled {
		t.Fatalf("unknown-old close = %q, want cancelled", got)
	}
	if got := store.rows["c-unknown-fresh"].Status; got != ScheduledCloseStatusAwaitingEntry {
		t.Fatalf("unknown-fresh close = %q, want still awaiting", got)
	}
}

// A duplicate/concurrent Confirm must not drop the timed close of an entry that
// actually executes: the second Confirm errors, but the awaiting close (already
// activated by the first) must stay pending.
func TestMissionDuplicateConfirmKeepsScheduledClose(t *testing.T) {
	stub := stubKlines(t)
	closeStore := newMemScheduledCloses()
	tk, _ := auth.NewTokenizer(bytes.Repeat([]byte("k"), auth.MinSecretSize), 0)
	token, _ := tk.Issue("tg:468848033", "u", "user")
	server := NewServer(testConfigWith(t, missionArmRuntimeEnv(stub.URL)), nil, testLogger(),
		WithTokenizer(tk), WithOrders(missionOrderService(&missionTestExecutor{})),
		WithCredentials(testCredentialService(t, "tg:468848033", true)), WithScheduledCloseStore(closeStore))

	post := func(path string, body any) map[string]any {
		b, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(b))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+token)
		resp, _ := server.App().Test(req)
		defer resp.Body.Close()
		var out map[string]any
		json.NewDecoder(resp.Body).Decode(&out)
		return out
	}

	out := post("/api/mission/prepare", map[string]any{
		"symbol": "BTC", "capital": 100, "strategy": "ema", "duration": "15m", "leverage_use_pct": 50,
	})
	cid, _ := out["confirm_id"].(string)
	if cid == "" {
		t.Fatalf("prepare = %v, want confirm_id", out)
	}
	// First confirm executes the entry and activates the close.
	post("/api/confirm", map[string]any{"id": cid})
	// Second (duplicate) confirm errors — but must NOT cancel the close.
	post("/api/confirm", map[string]any{"id": cid})

	var close ScheduledClose
	for _, row := range closeStore.rows {
		close = row
	}
	if close.Status != ScheduledCloseStatusPending {
		t.Fatalf("scheduled close after duplicate confirm = %+v, want pending (not cancelled)", close)
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
