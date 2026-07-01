package api

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"strings"
	"sync"
	"time"

	"bottrade/internal/decimal"
	"bottrade/internal/domain"
	"bottrade/internal/orders"
)

type ScheduledCloseStatus string

const (
	// AwaitingEntry means the close is persisted but NOT yet active: the entry it
	// protects has not been confirmed. The poller ignores it, so a crash before the
	// entry confirms can never close an unrelated open position of the same symbol.
	ScheduledCloseStatusAwaitingEntry ScheduledCloseStatus = "awaiting_entry"
	ScheduledCloseStatusPending       ScheduledCloseStatus = "pending"
	ScheduledCloseStatusExecuting     ScheduledCloseStatus = "executing"
	ScheduledCloseStatusDone          ScheduledCloseStatus = "done"
	ScheduledCloseStatusCancelled     ScheduledCloseStatus = "cancelled"
	ScheduledCloseStatusSkipped       ScheduledCloseStatus = "skipped"
)

const scheduledClosePollInterval = 30 * time.Second

// scheduledCloseAwaitingTTL cancels an awaiting-entry close whose entry was never
// confirmed within this window (e.g. the user never clicked Confirm, or a crash
// left it stranded before activation). Comfortably longer than the order
// confirmation TTL so a slow-but-real confirmation still activates.
const scheduledCloseAwaitingTTL = 30 * time.Minute

// ScheduledCloseClaimTimeout lets another API instance recover a close job if
// the process dies after claiming it but before writing a terminal status.
const ScheduledCloseClaimTimeout = 5 * time.Minute

// ScheduledClose is the durable plan-end close job for a testnet Mission. It is
// protective plumbing: the poller closes an open position after the plan window
// survives API restarts.
type ScheduledClose struct {
	ID      string `json:"id" bson:"_id"`
	UserKey string `json:"-" bson:"user_key"`
	UserID  int64  `json:"-" bson:"user_id"`
	Symbol  string `json:"symbol" bson:"symbol"`
	// Side is the mission position's direction ("long"/"short"). The poller only
	// closes an open position that matches this side, so a timed close can never
	// flatten an unrelated opposite-side position on the same symbol. Empty on
	// legacy rows persisted before this field existed → falls back to symbol-only
	// match (old behaviour) so in-flight rows still drain safely.
	Side                string               `json:"side,omitempty" bson:"side,omitempty"`
	DueAt               time.Time            `json:"due_at" bson:"due_at"`
	WindowSeconds       int64                `json:"window_seconds" bson:"window_seconds"`
	EntryConfirmationID string               `json:"entry_confirmation_id,omitempty" bson:"entry_confirmation_id,omitempty"`
	Status              ScheduledCloseStatus `json:"status" bson:"status"`
	ConfirmationID      string               `json:"confirmation_id,omitempty" bson:"confirmation_id,omitempty"`
	Reason              string               `json:"reason,omitempty" bson:"reason,omitempty"`
	CreatedAt           time.Time            `json:"created_at" bson:"created_at"`
	UpdatedAt           time.Time            `json:"updated_at" bson:"updated_at"`
	PurgeAt             *time.Time           `json:"purge_at,omitempty" bson:"purge_at,omitempty"`
}

type ScheduledCloseStore interface {
	Save(ctx context.Context, close ScheduledClose) error
	ListDue(ctx context.Context, now time.Time, limit int) ([]ScheduledClose, error)
	ClaimDue(ctx context.Context, id string, now time.Time) (ScheduledClose, bool, error)
	MarkDone(ctx context.Context, id, confirmationID, reason string, now time.Time) (ScheduledClose, bool, error)
	MarkSkipped(ctx context.Context, id, confirmationID, reason string, now time.Time) (ScheduledClose, bool, error)
	MarkCancelled(ctx context.Context, id, reason string, now time.Time) (ScheduledClose, bool, error)
	// ActivateByEntryConfirmation flips an awaiting-entry close to pending (arming
	// the poller) once its entry has actually confirmed, setting DueAt = now +
	// window. Keyed by the entry confirmation id so the confirm path can recover it
	// from Mongo after a restart — it does not depend on any in-memory metadata.
	ActivateByEntryConfirmation(ctx context.Context, entryConfirmationID string, now time.Time) (ScheduledClose, bool, error)
	// CancelByEntryConfirmation cancels an awaiting-entry/pending close when its
	// entry was cancelled or failed.
	CancelByEntryConfirmation(ctx context.Context, entryConfirmationID, reason string, now time.Time) (ScheduledClose, bool, error)
	// ListAwaitingEntry returns awaiting-entry closes for the reconciler to resolve
	// against their entry confirmation's durable status.
	ListAwaitingEntry(ctx context.Context, limit int) ([]ScheduledClose, error)
}

type memScheduledCloses struct {
	mu   sync.Mutex
	rows map[string]ScheduledClose
}

func newMemScheduledCloses() *memScheduledCloses {
	return &memScheduledCloses{rows: make(map[string]ScheduledClose)}
}

func (m *memScheduledCloses) Save(_ context.Context, close ScheduledClose) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rows[close.ID] = close
	return nil
}

func (m *memScheduledCloses) ListDue(_ context.Context, now time.Time, limit int) ([]ScheduledClose, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if limit <= 0 {
		limit = 100
	}
	out := make([]ScheduledClose, 0, len(m.rows))
	staleBefore := now.Add(-ScheduledCloseClaimTimeout)
	for _, row := range m.rows {
		if row.DueAt.After(now) {
			continue
		}
		if row.Status == ScheduledCloseStatusPending ||
			(row.Status == ScheduledCloseStatusExecuting && !row.UpdatedAt.After(staleBefore)) {
			out = append(out, row)
		}
	}
	sortScheduledCloses(out)
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (m *memScheduledCloses) ClaimDue(_ context.Context, id string, now time.Time) (ScheduledClose, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	row, ok := m.rows[id]
	stale := row.Status == ScheduledCloseStatusExecuting && !row.UpdatedAt.After(now.Add(-ScheduledCloseClaimTimeout))
	if !ok || row.DueAt.After(now) || (row.Status != ScheduledCloseStatusPending && !stale) {
		return row, false, nil
	}
	row.Status = ScheduledCloseStatusExecuting
	row.UpdatedAt = now
	m.rows[id] = row
	return row, true, nil
}

func (m *memScheduledCloses) MarkDone(_ context.Context, id, confirmationID, reason string, now time.Time) (ScheduledClose, bool, error) {
	return m.markTerminal(id, ScheduledCloseStatusDone, confirmationID, reason, now)
}

func (m *memScheduledCloses) MarkSkipped(_ context.Context, id, confirmationID, reason string, now time.Time) (ScheduledClose, bool, error) {
	return m.markTerminal(id, ScheduledCloseStatusSkipped, confirmationID, reason, now)
}

func (m *memScheduledCloses) MarkCancelled(_ context.Context, id, reason string, now time.Time) (ScheduledClose, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	row, ok := m.rows[id]
	if !ok || (row.Status != ScheduledCloseStatusPending && row.Status != ScheduledCloseStatusExecuting) {
		return row, false, nil
	}
	row.Status = ScheduledCloseStatusCancelled
	row.Reason = strings.TrimSpace(reason)
	row.UpdatedAt = now
	row.PurgeAt = scheduledClosePurgeAt(now)
	m.rows[id] = row
	return row, true, nil
}

func (m *memScheduledCloses) markTerminal(id string, status ScheduledCloseStatus, confirmationID, reason string, now time.Time) (ScheduledClose, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	row, ok := m.rows[id]
	if !ok || row.Status != ScheduledCloseStatusExecuting {
		return row, false, nil
	}
	row.Status = status
	row.ConfirmationID = confirmationID
	row.Reason = strings.TrimSpace(reason)
	row.UpdatedAt = now
	row.PurgeAt = scheduledClosePurgeAt(now)
	m.rows[id] = row
	return row, true, nil
}

func (m *memScheduledCloses) ActivateByEntryConfirmation(_ context.Context, entryConfirmationID string, now time.Time) (ScheduledClose, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, row := range m.rows {
		if row.EntryConfirmationID == entryConfirmationID && row.Status == ScheduledCloseStatusAwaitingEntry {
			row.Status = ScheduledCloseStatusPending
			row.DueAt = now.Add(time.Duration(row.WindowSeconds) * time.Second)
			row.UpdatedAt = now
			m.rows[id] = row
			return row, true, nil
		}
	}
	return ScheduledClose{}, false, nil
}

func (m *memScheduledCloses) CancelByEntryConfirmation(_ context.Context, entryConfirmationID, reason string, now time.Time) (ScheduledClose, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, row := range m.rows {
		if row.EntryConfirmationID != entryConfirmationID {
			continue
		}
		if row.Status == ScheduledCloseStatusAwaitingEntry || row.Status == ScheduledCloseStatusPending {
			row.Status = ScheduledCloseStatusCancelled
			row.Reason = strings.TrimSpace(reason)
			row.UpdatedAt = now
			row.PurgeAt = scheduledClosePurgeAt(now)
			m.rows[id] = row
			return row, true, nil
		}
	}
	return ScheduledClose{}, false, nil
}

func (m *memScheduledCloses) ListAwaitingEntry(_ context.Context, limit int) ([]ScheduledClose, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if limit <= 0 {
		limit = 100
	}
	out := make([]ScheduledClose, 0)
	for _, row := range m.rows {
		if row.Status == ScheduledCloseStatusAwaitingEntry {
			out = append(out, row)
		}
	}
	sortScheduledCloses(out)
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func sortScheduledCloses(rows []ScheduledClose) {
	for i := 1; i < len(rows); i++ {
		for j := i; j > 0 && rows[j].DueAt.Before(rows[j-1].DueAt); j-- {
			rows[j], rows[j-1] = rows[j-1], rows[j]
		}
	}
}

func newScheduledCloseID() (string, error) {
	var raw [18]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("create scheduled close id: %w", err)
	}
	return "close_" + base64.RawURLEncoding.EncodeToString(raw[:]), nil
}

func scheduledClosePurgeAt(now time.Time) *time.Time {
	if now.IsZero() {
		return nil
	}
	purgeAt := now.Add(armedMissionRetention)
	return &purgeAt
}

// scheduleTimedMissionClose persists an AWAITING-ENTRY close at prepare time, keyed
// by the entry confirmation id. It becomes active (poller-visible) only after the
// entry actually confirms (activateScheduledClose). Persisting at prepare — instead
// of an in-memory map — is what lets a confirm after an API restart still recover
// and arm the close.
func (s *Server) scheduleTimedMissionClose(m timedMission, entryConfirmationID string) (ScheduledClose, error) {
	if !s.armedMissionRuntimeAllowed() || m.Duration <= 0 || strings.TrimSpace(entryConfirmationID) == "" {
		return ScheduledClose{}, nil
	}
	if s.scheduledCloses == nil {
		return ScheduledClose{}, fmt.Errorf("scheduled close store is not configured")
	}
	now := time.Now().UTC()
	id, err := newScheduledCloseID()
	if err != nil {
		return ScheduledClose{}, err
	}
	close := ScheduledClose{
		ID:                  id,
		UserKey:             orders.TraderKey(m.UserID),
		UserID:              m.UserID,
		Symbol:              normalizeSymbol(m.Symbol),
		Side:                strings.ToLower(strings.TrimSpace(m.Side)),
		WindowSeconds:       int64(m.Duration / time.Second),
		EntryConfirmationID: entryConfirmationID,
		Status:              ScheduledCloseStatusAwaitingEntry,
		Reason:              "mission timed close",
		CreatedAt:           now,
		UpdatedAt:           now,
	}
	if err := s.scheduledCloses.Save(context.Background(), close); err != nil {
		return ScheduledClose{}, err
	}
	s.logger.Info("scheduled durable mission close (awaiting entry)", "id", close.ID, "user_id", close.UserID, "symbol", close.Symbol, "entry_confirmation_id", entryConfirmationID)
	return close, nil
}

// activateScheduledClose arms the poller for the close protecting a just-confirmed
// entry. Safe no-op if there is no awaiting close (e.g. gate off at schedule time).
func (s *Server) activateScheduledClose(ctx context.Context, entryConfirmationID string) {
	if s.scheduledCloses == nil || strings.TrimSpace(entryConfirmationID) == "" {
		return
	}
	updated, ok, err := s.scheduledCloses.ActivateByEntryConfirmation(ctx, entryConfirmationID, time.Now().UTC())
	if err != nil {
		s.logger.Warn("scheduled close activate failed", "entry_confirmation_id", entryConfirmationID, "error", err)
		return
	}
	if ok {
		s.logger.Info("scheduled close activated", "id", updated.ID, "symbol", updated.Symbol, "due_at", updated.DueAt)
	}
}

// cancelAwaitingScheduledClose drops the awaiting close for an entry that was
// cancelled or failed, so the poller never fires it.
func (s *Server) cancelAwaitingScheduledClose(ctx context.Context, entryConfirmationID, reason string) {
	if s.scheduledCloses == nil || strings.TrimSpace(entryConfirmationID) == "" {
		return
	}
	if _, _, err := s.scheduledCloses.CancelByEntryConfirmation(ctx, entryConfirmationID, reason, time.Now().UTC()); err != nil {
		s.logger.Warn("scheduled close cancel failed", "entry_confirmation_id", entryConfirmationID, "error", err)
	}
}

// reconcileAwaitingCloses resolves each awaiting-entry close against its entry
// confirmation's durable status: executed → activate (recovers a close whose
// activation was lost to a crash), failed/cancelled/expired → cancel, and
// pending/executing/unknown → leave until past the awaiting TTL, then cancel. This
// makes recovery status-driven rather than a blind time-based cancel, so a
// genuinely-executed entry never loses its close.
func (s *Server) reconcileAwaitingCloses(ctx context.Context, now time.Time) {
	if s.scheduledCloses == nil || s.orders == nil {
		return
	}
	rows, err := s.scheduledCloses.ListAwaitingEntry(ctx, 200)
	if err != nil {
		s.logger.Warn("scheduled close reconcile list failed", "error", err)
		return
	}
	staleBefore := now.Add(-scheduledCloseAwaitingTTL)
	for _, row := range rows {
		status, ok, err := s.orders.ConfirmationStatus(ctx, row.EntryConfirmationID)
		if err != nil {
			s.logger.Warn("scheduled close reconcile status failed", "id", row.ID, "error", err)
			continue
		}
		switch {
		case ok && status == orders.StatusExecuted:
			s.activateScheduledClose(ctx, row.EntryConfirmationID)
		case ok && (status == orders.StatusFailed || status == orders.StatusCancelled || status == orders.StatusExpired):
			s.cancelAwaitingScheduledClose(ctx, row.EntryConfirmationID, "entry "+string(status))
		default:
			// pending / executing (entry still in flight) or unknown/purged — cancel
			// only once the awaiting TTL has clearly passed.
			if row.CreatedAt.Before(staleBefore) {
				s.cancelAwaitingScheduledClose(ctx, row.EntryConfirmationID, "entry never confirmed")
			}
		}
	}
}

func (s *Server) startScheduledClosePoller(ctx context.Context) {
	if s.scheduledCloses == nil {
		return
	}
	go func() {
		run := func() {
			checkCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			if _, err := s.runDueScheduledCloses(checkCtx, time.Now().UTC()); err != nil {
				s.logger.Warn("scheduled close poll failed", "error", err)
			}
			cancel()
		}
		run()
		ticker := time.NewTicker(scheduledClosePollInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				run()
			}
		}
	}()
}

func (s *Server) runDueScheduledCloses(ctx context.Context, now time.Time) (int, error) {
	if s.scheduledCloses == nil {
		return 0, nil
	}
	// Reconcile awaiting-entry closes against their entry's durable status, so a
	// crash between confirm-success and activation is RECOVERED (not silently lost).
	s.reconcileAwaitingCloses(ctx, now)
	rows, err := s.scheduledCloses.ListDue(ctx, now, 100)
	if err != nil {
		return 0, err
	}
	handled := 0
	for _, row := range rows {
		if _, ok, err := s.handleScheduledClose(ctx, row.ID, now); err != nil {
			s.logger.Warn("scheduled close failed", "id", row.ID, "symbol", row.Symbol, "error", err)
		} else if ok {
			handled++
		}
	}
	return handled, nil
}

func (s *Server) handleScheduledClose(ctx context.Context, id string, now time.Time) (ScheduledClose, bool, error) {
	close, claimed, err := s.scheduledCloses.ClaimDue(ctx, id, now)
	if err != nil || !claimed {
		return close, false, err
	}
	if !s.armedMissionRuntimeAllowed() {
		updated, _, err := s.scheduledCloses.MarkSkipped(ctx, close.ID, "", "gate closed before timed close", now)
		return updated, true, err
	}
	if !s.hasActiveKeyForSubject(ctx, close.UserKey) {
		updated, _, err := s.scheduledCloses.MarkSkipped(ctx, close.ID, "", "active testnet key missing", now)
		return updated, true, err
	}
	if s.orders == nil {
		updated, _, err := s.scheduledCloses.MarkSkipped(ctx, close.ID, "", "orders service unavailable", now)
		return updated, true, err
	}
	positions, err := s.orders.PositionsWithRequiredUserExecutor(ctx, close.UserID)
	if err != nil {
		reason := "positions failed: " + err.Error()
		updated, _, markErr := s.scheduledCloses.MarkSkipped(ctx, close.ID, "", reason, now)
		if markErr != nil {
			return updated, true, markErr
		}
		return updated, true, err
	}
	if !scheduledCloseHasOpenPosition(positions, close.Symbol, close.Side) {
		updated, _, err := s.scheduledCloses.MarkDone(ctx, close.ID, "", "no matching open position", now)
		return updated, true, err
	}
	intent := domain.Intent{Type: domain.IntentClose, Close: &domain.CloseIntent{
		Symbol: close.Symbol, All: true, ResolvedPercent: decimal.NewFromInt(100),
		EntryConfirmationID: close.EntryConfirmationID,
	}}
	confirmation, err := s.orders.Prepare(ctx, close.UserID, intent)
	if err != nil {
		reason := "prepare close failed: " + err.Error()
		updated, _, markErr := s.scheduledCloses.MarkSkipped(ctx, close.ID, "", reason, now)
		if markErr != nil {
			return updated, true, markErr
		}
		return updated, true, err
	}
	if !s.armedMissionRuntimeAllowed() || !s.hasActiveKeyForSubject(ctx, close.UserKey) {
		_ = s.orders.Cancel(ctx, close.UserID, confirmation.ID)
		updated, _, err := s.scheduledCloses.MarkSkipped(ctx, close.ID, confirmation.ID, "gate closed before timed close confirm", time.Now().UTC())
		return updated, true, err
	}
	if _, err := s.orders.ConfirmWithRequiredUserExecutor(ctx, close.UserID, confirmation.ID); err != nil {
		reason := "confirm close failed: " + err.Error()
		updated, _, markErr := s.scheduledCloses.MarkSkipped(ctx, close.ID, confirmation.ID, reason, time.Now().UTC())
		if markErr != nil {
			return updated, true, markErr
		}
		return updated, true, err
	}
	updated, _, err := s.scheduledCloses.MarkDone(ctx, close.ID, confirmation.ID, "closed at plan deadline", time.Now().UTC())
	if err == nil {
		s.logger.Info("durable timed mission close executed", "id", close.ID, "user_id", close.UserID, "symbol", close.Symbol, "confirmation_id", confirmation.ID)
	}
	return updated, true, err
}

// scheduledCloseHasOpenPosition reports whether an open position exists that this
// timed close is entitled to flatten. When side is set (all non-legacy rows) it
// must match the position's direction, so a mission LONG never closes an unrelated
// SHORT the user opened on the same symbol after the mission position was gone.
// An empty side (legacy row) falls back to symbol-only matching.
func scheduledCloseHasOpenPosition(positions []domain.Position, symbol, side string) bool {
	for _, p := range positions {
		if !strings.EqualFold(p.Symbol, symbol) || p.Amount.IsZero() {
			continue
		}
		if side != "" && !strings.EqualFold(string(p.Side), side) {
			continue
		}
		return true
	}
	return false
}
