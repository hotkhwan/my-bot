package api

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"bottrade/internal/domain"
	"bottrade/internal/marketdata"
	"bottrade/internal/signals"
	"bottrade/internal/strategy/annybasic"

	"github.com/gofiber/fiber/v3"
)

type ArmedMissionStatus string

const (
	ArmedMissionStatusArmed     ArmedMissionStatus = "armed"
	ArmedMissionStatusTriggered ArmedMissionStatus = "triggered"
	ArmedMissionStatusExpired   ArmedMissionStatus = "expired"
	ArmedMissionStatusDisarmed  ArmedMissionStatus = "disarmed"
)

const (
	armedMissionRetention         = 90 * 24 * time.Hour
	armedMissionUnlimitedDuration = "24h"
	maxActiveArmedMissionsPerUser = 3
)

// ArmedMission is a bounded, persisted pre-authorization to wait for one ANNY
// Basic setup inside the plan window and enter once on testnet.
type ArmedMission struct {
	ID                      string             `json:"id" bson:"_id"`
	UserKey                 string             `json:"-" bson:"user_key"`
	UserID                  int64              `json:"-" bson:"user_id"`
	Symbol                  string             `json:"symbol" bson:"symbol"`
	Strategy                string             `json:"strategy" bson:"strategy"`
	Side                    string             `json:"side,omitempty" bson:"side,omitempty"`
	CapitalUSDT             string             `json:"capital_usdt" bson:"capital_usdt"`
	CapitalRiskPct          int                `json:"capital_risk_pct" bson:"capital_risk_pct"`
	LeverageUsePct          int                `json:"leverage_use_pct" bson:"leverage_use_pct"`
	Duration                string             `json:"duration" bson:"duration"`
	DurationWindowSeconds   int64              `json:"duration_window_seconds" bson:"duration_window_seconds"`
	UseAI                   bool               `json:"used_ai" bson:"used_ai"`
	Status                  ArmedMissionStatus `json:"status" bson:"status"`
	ArmReason               string             `json:"arm_reason,omitempty" bson:"arm_reason,omitempty"`
	TriggerReason           string             `json:"trigger_reason,omitempty" bson:"trigger_reason,omitempty"`
	IdempotencyKey          string             `json:"idempotency_key" bson:"idempotency_key"`
	TriggeredConfirmationID string             `json:"triggered_confirmation_id,omitempty" bson:"triggered_confirmation_id,omitempty"`
	ArmedAt                 time.Time          `json:"armed_at" bson:"armed_at"`
	TriggeredAt             *time.Time         `json:"triggered_at,omitempty" bson:"triggered_at,omitempty"`
	ExpiresAt               time.Time          `json:"expires_at" bson:"expires_at"`
	PurgeAt                 *time.Time         `json:"purge_at,omitempty" bson:"purge_at,omitempty"`
	CreatedAt               time.Time          `json:"created_at" bson:"created_at"`
	UpdatedAt               time.Time          `json:"updated_at" bson:"updated_at"`
}

type ArmedMissionStore interface {
	Save(ctx context.Context, mission ArmedMission) error
	Get(ctx context.Context, id string) (ArmedMission, bool, error)
	ListActive(ctx context.Context, now time.Time) ([]ArmedMission, error)
	ListUser(ctx context.Context, userKey string, limit int) ([]ArmedMission, error)
	Disarm(ctx context.Context, userKey, id string, now time.Time) (ArmedMission, bool, error)
	MarkExpired(ctx context.Context, id string, now time.Time) (ArmedMission, bool, error)
	MarkTriggered(ctx context.Context, id, side, reason, confirmationID string, now time.Time) (ArmedMission, bool, error)
	SetTriggeredConfirmation(ctx context.Context, id, confirmationID string, now time.Time) (ArmedMission, bool, error)
	// ExtendWindow lengthens a still-armed mission's wait window (e.g. re-arming the
	// same symbol with a longer Plan duration). Only updates when still armed.
	ExtendWindow(ctx context.Context, id, durationKey string, windowSeconds int64, expiresAt time.Time, purgeAt *time.Time, now time.Time) (ArmedMission, bool, error)
	// ExpireStale marks any still-"armed" rows whose window already passed as
	// expired — cleaning up orphans whose watcher died (e.g. a restart that did not
	// re-watch them). Returns how many were swept.
	ExpireStale(ctx context.Context, now time.Time) (int, error)
}

type memArmedMissions struct {
	mu   sync.Mutex
	rows map[string]ArmedMission
}

func newMemArmedMissions() *memArmedMissions {
	return &memArmedMissions{rows: make(map[string]ArmedMission)}
}

func (m *memArmedMissions) Save(_ context.Context, mission ArmedMission) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rows[mission.ID] = mission
	return nil
}

func (m *memArmedMissions) Get(_ context.Context, id string) (ArmedMission, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	row, ok := m.rows[id]
	return row, ok, nil
}

func (m *memArmedMissions) ListActive(_ context.Context, now time.Time) ([]ArmedMission, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]ArmedMission, 0, len(m.rows))
	for _, row := range m.rows {
		if row.Status == ArmedMissionStatusArmed && now.Before(row.ExpiresAt) {
			out = append(out, row)
		}
	}
	return out, nil
}

func (m *memArmedMissions) ListUser(_ context.Context, userKey string, limit int) ([]ArmedMission, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if limit <= 0 {
		limit = 20
	}
	out := make([]ArmedMission, 0, len(m.rows))
	for _, row := range m.rows {
		if row.UserKey == userKey {
			out = append(out, row)
		}
	}
	sortArmedMissions(out)
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (m *memArmedMissions) Disarm(_ context.Context, userKey, id string, now time.Time) (ArmedMission, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	row, ok := m.rows[id]
	if !ok || row.UserKey != userKey {
		return ArmedMission{}, false, nil
	}
	if row.Status == ArmedMissionStatusArmed {
		row.Status = ArmedMissionStatusDisarmed
		row.UpdatedAt = now
		if row.PurgeAt == nil {
			row.PurgeAt = armedMissionPurgeAt(row.ExpiresAt)
		}
		m.rows[id] = row
	}
	return row, true, nil
}

func (m *memArmedMissions) MarkExpired(_ context.Context, id string, now time.Time) (ArmedMission, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	row, ok := m.rows[id]
	if !ok || row.Status != ArmedMissionStatusArmed {
		return row, false, nil
	}
	row.Status = ArmedMissionStatusExpired
	row.UpdatedAt = now
	if row.PurgeAt == nil {
		row.PurgeAt = armedMissionPurgeAt(row.ExpiresAt)
	}
	m.rows[id] = row
	return row, true, nil
}

func (m *memArmedMissions) MarkTriggered(_ context.Context, id, side, reason, confirmationID string, now time.Time) (ArmedMission, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	row, ok := m.rows[id]
	if !ok || row.Status != ArmedMissionStatusArmed || !now.Before(row.ExpiresAt) {
		return row, false, nil
	}
	row.Status = ArmedMissionStatusTriggered
	row.Side = side
	row.TriggerReason = reason
	row.TriggeredConfirmationID = confirmationID
	row.TriggeredAt = &now
	row.PurgeAt = nil
	row.UpdatedAt = now
	m.rows[id] = row
	return row, true, nil
}

func (m *memArmedMissions) SetTriggeredConfirmation(_ context.Context, id, confirmationID string, now time.Time) (ArmedMission, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	row, ok := m.rows[id]
	if !ok || row.Status != ArmedMissionStatusTriggered {
		return row, false, nil
	}
	if row.TriggeredConfirmationID != "" && row.TriggeredConfirmationID != confirmationID {
		return row, false, nil
	}
	row.TriggeredConfirmationID = confirmationID
	row.UpdatedAt = now
	m.rows[id] = row
	return row, true, nil
}

func (m *memArmedMissions) ExtendWindow(_ context.Context, id, durationKey string, windowSeconds int64, expiresAt time.Time, purgeAt *time.Time, now time.Time) (ArmedMission, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	row, ok := m.rows[id]
	if !ok || row.Status != ArmedMissionStatusArmed {
		return row, false, nil
	}
	row.Duration = durationKey
	row.DurationWindowSeconds = windowSeconds
	row.ExpiresAt = expiresAt
	row.PurgeAt = purgeAt
	row.UpdatedAt = now
	m.rows[id] = row
	return row, true, nil
}

func (m *memArmedMissions) ExpireStale(_ context.Context, now time.Time) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for id, row := range m.rows {
		if row.Status == ArmedMissionStatusArmed && !now.Before(row.ExpiresAt) {
			row.Status = ArmedMissionStatusExpired
			row.UpdatedAt = now
			m.rows[id] = row
			n++
		}
	}
	return n, nil
}

func sortArmedMissions(rows []ArmedMission) {
	for i := 1; i < len(rows); i++ {
		for j := i; j > 0 && rows[j].CreatedAt.After(rows[j-1].CreatedAt); j-- {
			rows[j], rows[j-1] = rows[j-1], rows[j]
		}
	}
}

func newArmedMissionID() (string, error) {
	var raw [18]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("create armed mission id: %w", err)
	}
	return "arm_" + base64.RawURLEncoding.EncodeToString(raw[:]), nil
}

func missionDurationKey(value string) string {
	key := strings.ToLower(strings.TrimSpace(value))
	if key == unlimitedDuration || key == "∞" {
		return armedMissionUnlimitedDuration
	}
	if _, ok := allowedDurations[key]; ok {
		return key
	}
	return "1h"
}

func missionSpecFor(value string) (string, durationSpec) {
	key := missionDurationKey(value)
	return key, allowedDurations[key]
}

func capitalString(n json.Number) string {
	v := strings.TrimSpace(n.String())
	if v == "" {
		return "100"
	}
	return v
}

func armedMissionPurgeAt(expiresAt time.Time) *time.Time {
	if expiresAt.IsZero() {
		return nil
	}
	purgeAt := expiresAt.Add(armedMissionRetention)
	return &purgeAt
}

func isUnlimitedMissionDuration(value string) bool {
	key := strings.ToLower(strings.TrimSpace(value))
	return key == unlimitedDuration || key == "∞"
}

func (s *Server) armANNYBasicMission(c fiber.Ctx, req goalRequest, userID int64, symbol, reason string) error {
	if s.armedMissions == nil {
		return c.JSON(fiber.Map{"output": "Armed missions are not enabled on this server."})
	}
	if !s.armedMissionRuntimeAllowed() {
		return c.JSON(fiber.Map{"output": "Arming is available only in testnet live-mission mode (CampaignLiveEnabled, Binance testnet, dry-run off, real trading off). No order was staged."})
	}
	now := time.Now().UTC()
	durationKey := missionDurationKey(req.Duration)
	window := planDuration(durationKey)
	userKey := claimsOf(c).Subject
	if !s.hasActiveKeyForSubject(c.Context(), userKey) {
		return c.JSON(fiber.Map{
			"output":   "🔑 No active testnet Binance key yet. Open Settings → add a testnet key profile (Futures on, Withdrawals off) → tap “Make active”, then arm the Mission.",
			"need_key": true,
		})
	}
	active, err := s.armedMissions.ListActive(c.Context(), now)
	if err != nil {
		s.logger.Warn("arm mission active scan failed", "user", userKey, "symbol", symbol, "error", err)
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "could not arm mission"})
	}
	activeForUser := 0
	for _, row := range active {
		if row.UserKey != userKey {
			continue
		}
		if row.Symbol == symbol && row.Strategy == annybasic.ID {
			// Re-arm of the same symbol: if the new plan asks for a longer window,
			// extend it (so switching 15m → ∞ takes effect without a manual disarm).
			newExpiry := now.Add(window)
			if newExpiry.After(row.ExpiresAt) {
				if extended, ok, err := s.armedMissions.ExtendWindow(c.Context(), row.ID, durationKey, int64(window.Seconds()), newExpiry, armedMissionPurgeAt(newExpiry), now); err == nil && ok {
					row = extended
				}
			}
			if runtimeCtx := s.runtimeContext(); runtimeCtx != nil {
				s.startArmedMissionWatcher(runtimeCtx, row)
			}
			output := fmt.Sprintf("Already armed — waiting for an ANNY Basic setup on %s. Expires at %s.",
				symbol, row.ExpiresAt.Format(time.RFC3339))
			return c.JSON(fiber.Map{"output": output, "armed": row})
		}
		activeForUser++
	}
	if activeForUser >= maxActiveArmedMissionsPerUser {
		return c.JSON(fiber.Map{"output": fmt.Sprintf("You already have %d armed missions waiting. Disarm one before arming another.", maxActiveArmedMissionsPerUser)})
	}
	id, err := newArmedMissionID()
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "could not arm mission"})
	}
	risk := req.CapitalRiskPct
	if risk == 0 {
		risk = req.Risk
	}
	if risk <= 0 {
		risk = 30
	}
	if risk > 100 {
		risk = 100
	}
	leverageUse := req.LeverageUsePct
	if leverageUse <= 0 {
		leverageUse = 25
	}
	if leverageUse > 100 {
		leverageUse = 100
	}
	expiresAt := now.Add(window)
	mission := ArmedMission{
		ID:                    id,
		UserKey:               userKey,
		UserID:                userID,
		Symbol:                symbol,
		Strategy:              annybasic.ID,
		CapitalUSDT:           capitalString(req.Capital),
		CapitalRiskPct:        risk,
		LeverageUsePct:        leverageUse,
		Duration:              durationKey,
		DurationWindowSeconds: int64(window.Seconds()),
		UseAI:                 req.UseAI,
		Status:                ArmedMissionStatusArmed,
		ArmReason:             strings.TrimSpace(reason),
		IdempotencyKey:        "armed:" + id,
		ArmedAt:               now,
		ExpiresAt:             expiresAt,
		PurgeAt:               armedMissionPurgeAt(expiresAt),
		CreatedAt:             now,
		UpdatedAt:             now,
	}
	if err := s.armedMissions.Save(c.Context(), mission); err != nil {
		s.logger.Warn("arm mission persist failed", "user", mission.UserKey, "symbol", symbol, "error", err)
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "could not arm mission"})
	}
	if runtimeCtx := s.runtimeContext(); runtimeCtx != nil {
		s.startArmedMissionWatcher(runtimeCtx, mission)
	}
	output := fmt.Sprintf("Armed — waiting for an ANNY Basic setup on %s. Last check: %s. Expires at %s. If a setup appears in this window, ANNY will auto-place one testnet entry on your active testnet key using capped size/leverage, with protective exits and a timed close. The window can expire with no order. Not financial advice.",
		symbol, fallback(reason, "no setup right now"), mission.ExpiresAt.Format(time.RFC3339))
	if isUnlimitedMissionDuration(req.Duration) {
		output += " Unlimited plans use a 24h maximum armed window."
	}
	return c.JSON(fiber.Map{"output": output, "armed": mission})
}

func fallback(value, def string) string {
	if strings.TrimSpace(value) == "" {
		return def
	}
	return strings.TrimSpace(value)
}

func (s *Server) handleArmedMissions(c fiber.Ctx) error {
	if s.armedMissions == nil {
		return c.JSON(fiber.Map{"armed": []ArmedMission{}})
	}
	rows, err := s.armedMissions.ListUser(c.Context(), claimsOf(c).Subject, 20)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "could not load armed missions"})
	}
	if rows == nil {
		rows = []ArmedMission{}
	}
	return c.JSON(fiber.Map{"armed": rows})
}

func (s *Server) handleDisarmMission(c fiber.Ctx) error {
	if s.armedMissions == nil {
		return c.Status(fiber.StatusNotImplemented).JSON(fiber.Map{"error": "armed missions are not enabled"})
	}
	var body struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(c.Body(), &body); err != nil || strings.TrimSpace(body.ID) == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "missing armed mission id"})
	}
	mission, ok, err := s.armedMissions.Disarm(c.Context(), claimsOf(c).Subject, strings.TrimSpace(body.ID), time.Now().UTC())
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "could not disarm mission"})
	}
	if !ok {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "armed mission not found"})
	}
	output := "Disarmed. No order will be placed."
	switch mission.Status {
	case ArmedMissionStatusTriggered:
		output = "Already triggered. This mission has passed the point of no return; check the Live monitor for the testnet entry."
	case ArmedMissionStatusExpired:
		output = "Already expired. No order was placed."
	case ArmedMissionStatusDisarmed:
		output = "Disarmed. No order will be placed."
	}
	return c.JSON(fiber.Map{"output": output, "armed": mission})
}

func (s *Server) startArmedMissionWatchers(ctx context.Context) int {
	if s.armedMissions == nil {
		return 0
	}
	now := time.Now().UTC()
	// Sweep orphans first: rows still "armed" past their window whose watcher died
	// (e.g. before this boot) would otherwise never be marked expired.
	if swept, err := s.armedMissions.ExpireStale(ctx, now); err != nil {
		s.logger.Warn("armed mission expire-stale failed", "error", err)
	} else if swept > 0 {
		s.logger.Info("armed missions expired on boot (stale orphans)", "count", swept)
	}
	rows, err := s.armedMissions.ListActive(ctx, now)
	if err != nil {
		s.logger.Warn("armed mission rehydrate failed", "error", err)
		return 0
	}
	for _, row := range rows {
		s.startArmedMissionWatcher(ctx, row)
	}
	return len(rows)
}

func (s *Server) startArmedMissionWatcher(ctx context.Context, mission ArmedMission) {
	if mission.ID == "" || mission.Status != ArmedMissionStatusArmed || !time.Now().UTC().Before(mission.ExpiresAt) {
		return
	}
	if _, loaded := s.armedWatchers.LoadOrStore(mission.ID, struct{}{}); loaded {
		return
	}
	go func() {
		defer s.armedWatchers.Delete(mission.ID)
		ticker := time.NewTicker(armedMissionPollInterval(mission.Duration))
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				checkCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
				_, done, err := s.checkArmedMission(checkCtx, mission.ID, time.Now().UTC())
				cancel()
				if err != nil {
					s.logger.Warn("armed mission check failed", "id", mission.ID, "symbol", mission.Symbol, "error", err)
				}
				if done {
					return
				}
			}
		}
	}()
}

func armedMissionPollInterval(duration string) time.Duration {
	_, spec := missionSpecFor(duration)
	switch spec.ExecutionInterval {
	case "1m":
		return time.Minute
	case "5m":
		return 5 * time.Minute
	case "15m":
		return 15 * time.Minute
	case "1h":
		return time.Hour
	default:
		return time.Minute
	}
}

func (s *Server) checkArmedMission(ctx context.Context, id string, now time.Time) (ArmedMission, bool, error) {
	mission, ok, err := s.armedMissions.Get(ctx, id)
	if err != nil || !ok {
		return mission, true, err
	}
	if mission.Status != ArmedMissionStatusArmed {
		return mission, true, nil
	}
	if !now.Before(mission.ExpiresAt) {
		expired, _, err := s.armedMissions.MarkExpired(ctx, id, now)
		return expired, true, err
	}
	if !s.armedMissionRuntimeAllowed() || !s.armedMissionTriggerAllowed(ctx, mission) {
		return mission, false, nil
	}
	_, spec := missionSpecFor(mission.Duration)
	candles, err := s.market.Candles(ctx, mission.Symbol, spec.ExecutionInterval, 120)
	if err != nil || len(candles) == 0 {
		if err == nil {
			err = fmt.Errorf("no market candles")
		}
		return mission, false, err
	}
	decision, err := s.annyBasicLiveDecision(ctx, mission.Symbol, candles)
	if err != nil {
		return mission, false, err
	}
	if decision.Stop || decision.Side == annybasic.SideNone {
		return mission, false, nil
	}
	return s.triggerArmedMission(ctx, mission, candles[len(candles)-1].Close, decision, now)
}

func (s *Server) triggerArmedMission(ctx context.Context, mission ArmedMission, entry float64, decision annybasic.Decision, now time.Time) (ArmedMission, bool, error) {
	side := string(decision.Side)
	reason := annybasic.ID + " v" + annybasic.Version + " · " + decision.Reason
	claimed, changed, err := s.armedMissions.MarkTriggered(ctx, mission.ID, side, reason, "", now)
	if err != nil || !changed {
		return claimed, true, err
	}
	s.usage.Incr(mission.UserKey, "mission")
	intent, err := s.intentForArmedMission(claimed, side, entry, decision.MaxLeverage, reason)
	if err != nil {
		return claimed, true, err
	}
	confirmation, err := s.orders.PrepareWithIdempotencyKey(ctx, mission.UserID, intent, mission.IdempotencyKey)
	if err != nil {
		return claimed, true, err
	}
	updated, ok, err := s.armedMissions.SetTriggeredConfirmation(ctx, mission.ID, confirmation.ID, time.Now().UTC())
	if err != nil || !ok {
		_ = s.orders.Cancel(ctx, mission.UserID, confirmation.ID)
		if err == nil {
			err = fmt.Errorf("armed mission confirmation could not be recorded")
		}
		return updated, true, err
	}
	if !s.armedMissionRuntimeAllowed() || !s.armedMissionTriggerAllowed(ctx, updated) {
		_ = s.orders.Cancel(ctx, mission.UserID, confirmation.ID)
		return updated, true, fmt.Errorf("armed mission gate closed before confirm")
	}
	// Persist the plan-end close as AWAITING-ENTRY (poller-invisible) before confirm;
	// it is armed only after the entry actually executes, so a crash before confirm
	// can never close an unrelated position.
	if _, err := s.scheduleTimedMissionClose(timedMission{
		UserID: mission.UserID, Symbol: mission.Symbol, Duration: planDuration(mission.Duration),
	}, confirmation.ID); err != nil {
		_ = s.orders.Cancel(ctx, mission.UserID, confirmation.ID)
		return updated, true, err
	}
	if !s.armedMissionRuntimeAllowed() || !s.armedMissionTriggerAllowed(ctx, updated) {
		_ = s.orders.Cancel(ctx, mission.UserID, confirmation.ID)
		s.cancelAwaitingScheduledClose(ctx, confirmation.ID, "armed mission gate closed before entry confirm")
		return updated, true, fmt.Errorf("armed mission gate closed before confirm")
	}
	if _, err := s.orders.ConfirmWithRequiredUserExecutor(ctx, mission.UserID, confirmation.ID); err != nil {
		s.cancelAwaitingScheduledClose(ctx, confirmation.ID, "entry confirm failed: "+err.Error())
		return updated, true, err
	}
	// Entry executed → arm the plan-end close.
	s.activateScheduledClose(ctx, confirmation.ID)
	s.logger.Info("armed mission testnet entry confirmed",
		"id", mission.ID, "confirmation_id", confirmation.ID, "user", mission.UserKey, "symbol", mission.Symbol, "side", side, "reason", decision.Reason)
	return updated, true, nil
}

func (s *Server) intentForArmedMission(mission ArmedMission, side string, entry float64, modelLeverageCap int, reason string) (domain.Intent, error) {
	sl, tp := missionBracket(side, entry)
	size := missionSize(json.Number(mission.CapitalUSDT))
	leverage := missionLeverageFor(mission.LeverageUsePct, minPositive(s.cfg.App.MaxLeverage, modelLeverageCap))
	decision := signals.Decision{
		Action:     signals.ActionOpen,
		Symbol:     mission.Symbol,
		Side:       side,
		Leverage:   leverage,
		Entry:      trimPrice(entry),
		StopLoss:   trimPrice(sl),
		TakeProfit: trimPrice(tp),
		SizeUSDT:   fmt.Sprintf("%.2f", size),
		Reason:     reason,
	}
	return signals.DecisionToIntent(decision, missionMaxLeverage)
}

func (s *Server) armedMissionRuntimeAllowed() bool {
	return s.cfg.App.CampaignLiveEnabled && s.cfg.Binance.Testnet &&
		!s.cfg.App.RealTradingEnabled && !s.cfg.App.DryRun
}

func (s *Server) armedMissionTriggerAllowed(ctx context.Context, mission ArmedMission) bool {
	if s.orders == nil {
		return false
	}
	if !s.hasActiveKeyForSubject(ctx, mission.UserKey) {
		return false
	}
	allowed, _ := s.allow(ctx, mission.UserKey, "mission")
	return allowed
}

func (s *Server) hasActiveKeyForSubject(ctx context.Context, subject string) bool {
	if s.credentials == nil {
		return false
	}
	profiles, err := s.credentials.Profiles(ctx, subject)
	if err != nil {
		return false
	}
	for _, p := range profiles {
		if p.Active && p.Testnet {
			return true
		}
	}
	return false
}

type annyBasicDecisionFunc func(context.Context, string, []marketdata.Candle) (annybasic.Decision, error)
