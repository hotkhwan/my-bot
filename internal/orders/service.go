package orders

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"bottrade/internal/audit"
	"bottrade/internal/decimal"
	"bottrade/internal/domain"
	"bottrade/internal/journal"
)

type ConfirmationStatus string

const (
	StatusPending   ConfirmationStatus = "pending"
	StatusExecuting ConfirmationStatus = "executing"
	StatusExecuted  ConfirmationStatus = "executed"
	StatusCancelled ConfirmationStatus = "cancelled"
	StatusExpired   ConfirmationStatus = "expired"
	StatusFailed    ConfirmationStatus = "failed"
)

var (
	ErrConfirmationNotFound  = errors.New("confirmation not found")
	ErrConfirmationForbidden = errors.New("confirmation belongs to another user")
	ErrConfirmationExpired   = errors.New("confirmation expired")
	ErrConfirmationCancelled = errors.New("confirmation cancelled")
	ErrConfirmationExecuted  = errors.New("confirmation already executed")
	ErrConfirmationFailed    = errors.New("confirmation failed")
	ErrConfirmationExecuting = errors.New("confirmation already executing")
)

type Confirmation struct {
	ID             string
	UserID         int64
	Intent         domain.Intent
	Status         ConfirmationStatus
	CorrelationID  string
	IntentHash     string
	IdempotencyKey string
	CreatedAt      time.Time
	ExpiresAt      time.Time
}

type ExecutionResult struct {
	Mode          string
	ClientOrderID string
	Message       string
	// Set on a close: the symbol/side that was closed and the realized PnL, so
	// the trade journal can record the round-trip outcome.
	Symbol      string
	Side        string
	RealizedPnL decimal.Decimal
}

type Executor interface {
	Execute(ctx context.Context, confirmation Confirmation) (ExecutionResult, error)
}

// ExecutorProvider resolves the executor for a specific user, so each user
// trades on their own Binance account. Optional; when nil (or when a user has no
// stored key) the service uses its default executor. The bool reports whether a
// per-user executor was found.
type ExecutorProvider interface {
	ExecutorFor(ctx context.Context, userKey string) (Executor, bool, error)
}

// TradeJournal records closed trades for win-rate / PnL statistics. Optional;
// when nil the order flow runs without journaling.
type TradeJournal interface {
	Record(ctx context.Context, trade journal.Trade) error
}

type ConfirmationStore interface {
	Put(ctx context.Context, confirmation Confirmation) error
	TakeForExecution(ctx context.Context, userID int64, id string, now time.Time) (Confirmation, ExecutionResult, bool, error)
	Complete(ctx context.Context, id string, result ExecutionResult) error
	Fail(ctx context.Context, id string, message string) error
	Cancel(ctx context.Context, userID int64, id string, now time.Time) error
}

type Service struct {
	ttl           time.Duration
	clock         func() time.Time
	store         ConfirmationStore
	intentStore   IntentStore
	auditRecorder audit.Recorder
	executor      Executor
	executorFor   ExecutorProvider
	journal       TradeJournal
	logger        *slog.Logger
}

type ServiceDependencies struct {
	ConfirmationStore ConfirmationStore
	IntentStore       IntentStore
	AuditRecorder     audit.Recorder
	Journal           TradeJournal
	ExecutorProvider  ExecutorProvider
}

func NewService(dryRun bool, ttl time.Duration, logger *slog.Logger) *Service {
	if ttl <= 0 {
		ttl = 5 * time.Minute
	}
	if logger == nil {
		logger = slog.Default()
	}

	return NewServiceWithStore(ttl, DryRunExecutor{DryRun: dryRun}, NewMemoryStore(), logger)
}

func NewServiceWithExecutor(ttl time.Duration, executor Executor, logger *slog.Logger) *Service {
	return NewServiceWithStore(ttl, executor, NewMemoryStore(), logger)
}

func NewServiceWithStore(ttl time.Duration, executor Executor, store ConfirmationStore, logger *slog.Logger) *Service {
	return NewServiceWithRepositories(ttl, executor, ServiceDependencies{ConfirmationStore: store}, logger)
}

func NewServiceWithRepositories(ttl time.Duration, executor Executor, deps ServiceDependencies, logger *slog.Logger) *Service {
	if ttl <= 0 {
		ttl = 5 * time.Minute
	}
	if executor == nil {
		executor = DryRunExecutor{DryRun: true}
	}
	if logger == nil {
		logger = slog.Default()
	}
	if deps.ConfirmationStore == nil {
		deps.ConfirmationStore = NewMemoryStore()
	}
	if deps.IntentStore == nil {
		deps.IntentStore = NoopIntentStore{}
	}
	if deps.AuditRecorder == nil {
		deps.AuditRecorder = audit.NoopRecorder{}
	}

	return &Service{
		ttl:           ttl,
		clock:         time.Now,
		store:         deps.ConfirmationStore,
		intentStore:   deps.IntentStore,
		auditRecorder: deps.AuditRecorder,
		executor:      executor,
		executorFor:   deps.ExecutorProvider,
		journal:       deps.Journal,
		logger:        logger,
	}
}

// executorForUser returns the user's own executor when a provider is configured
// and the user has a stored key; otherwise the default executor. A provider
// error is logged and falls back to the default so a lookup failure never
// silently drops a trade.
func (s *Service) executorForUser(ctx context.Context, userID int64) Executor {
	if s.executorFor == nil {
		return s.executor
	}
	executor, ok, err := s.executorFor.ExecutorFor(ctx, TraderKey(userID))
	if err != nil {
		s.logger.Warn("per-user executor lookup failed; using default", "user_id", userID, "error", err)
		return s.executor
	}
	if ok {
		return executor
	}
	return s.executor
}

// TraderKey is the per-user credential/identity key for a Telegram user id,
// matching the "tg:<id>" subject minted by Telegram login.
func TraderKey(userID int64) string {
	return "tg:" + strconv.FormatInt(userID, 10)
}

func (s *Service) Prepare(ctx context.Context, userID int64, intent domain.Intent) (Confirmation, error) {
	if !intent.IsExchangeChanging() {
		return Confirmation{}, fmt.Errorf("intent %q does not require confirmation", intent.Type)
	}

	id, err := newConfirmationID()
	if err != nil {
		return Confirmation{}, err
	}

	correlationID, err := newConfirmationID()
	if err != nil {
		return Confirmation{}, err
	}
	intentHash, err := IntentHash(intent)
	if err != nil {
		return Confirmation{}, err
	}

	now := s.clock()
	confirmation := Confirmation{
		ID:             id,
		UserID:         userID,
		Intent:         intent,
		Status:         StatusPending,
		CorrelationID:  correlationID,
		IntentHash:     intentHash,
		IdempotencyKey: "confirm:" + id,
		CreatedAt:      now,
		ExpiresAt:      now.Add(s.ttl),
	}
	intentRecord := IntentRecord{
		ID:             id,
		UserID:         userID,
		Intent:         intent,
		IntentHash:     intentHash,
		CorrelationID:  correlationID,
		ConfirmationID: id,
		Status:         IntentStatusAwaitingConfirmation,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if err := s.intentStore.PutIntentRecord(ctx, intentRecord); err != nil {
		return Confirmation{}, err
	}
	if err := s.store.Put(ctx, confirmation); err != nil {
		return Confirmation{}, err
	}
	if err := s.auditRecorder.Record(ctx, audit.Event{
		Type:          "confirmation_created",
		Source:        "orders",
		UserID:        userID,
		CorrelationID: correlationID,
		Metadata: map[string]string{
			"confirmation_id": shortID(id),
			"intent_hash":     intentHash,
			"intent_type":     string(intent.Type),
		},
		CreatedAt: now,
	}); err != nil {
		return Confirmation{}, err
	}
	s.logger.Info("confirmation created", "confirmation_id", shortID(id), "user_id", userID, "intent_type", intent.Type)

	return confirmation, nil
}

func (s *Service) Confirm(ctx context.Context, userID int64, id string) (ExecutionResult, error) {
	confirmation, result, done, err := s.store.TakeForExecution(ctx, userID, id, s.clock())
	if err != nil {
		return ExecutionResult{}, err
	}
	if done {
		return result, nil
	}

	s.updateIntentStatus(ctx, confirmation.ID, IntentStatusExecuting, "")
	result, err = s.executorForUser(ctx, userID).Execute(ctx, confirmation)
	if err != nil {
		_ = s.store.Fail(ctx, id, err.Error())
		s.updateIntentStatus(ctx, confirmation.ID, IntentStatusFailed, err.Error())
		s.recordAudit(ctx, audit.Event{
			Type:          "confirmation_failed",
			Source:        "orders",
			UserID:        userID,
			CorrelationID: confirmation.CorrelationID,
			Metadata: map[string]string{
				"confirmation_id": shortID(id),
				"intent_hash":     confirmation.IntentHash,
				"intent_type":     string(confirmation.Intent.Type),
				"error":           err.Error(),
			},
		})
		s.logger.Error("confirmation execution failed",
			"confirmation_id", shortID(id),
			"user_id", userID,
			"intent_type", confirmation.Intent.Type,
			"error", err.Error(),
		)
		return ExecutionResult{}, err
	}

	if err := s.store.Complete(ctx, id, result); err != nil {
		return ExecutionResult{}, err
	}
	s.updateIntentStatus(ctx, confirmation.ID, IntentStatusExecuted, "")
	s.recordAudit(ctx, audit.Event{
		Type:          "confirmation_executed",
		Source:        "orders",
		UserID:        userID,
		CorrelationID: confirmation.CorrelationID,
		Metadata: map[string]string{
			"confirmation_id": shortID(id),
			"intent_hash":     confirmation.IntentHash,
			"intent_type":     string(confirmation.Intent.Type),
			"mode":            result.Mode,
			"client_order_id": result.ClientOrderID,
		},
	})
	s.logger.Info("confirmation executed", "confirmation_id", shortID(id), "user_id", userID, "intent_type", confirmation.Intent.Type)
	s.recordClosedTrade(ctx, userID, confirmation, result)

	return result, nil
}

// recordClosedTrade journals a completed round-trip when a close executes, using
// the realized PnL the executor reported. Only closes are journaled, so each
// resolved trade is counted once with its win/loss outcome. Best-effort: a
// journal failure must not fail the trade that already executed.
func (s *Service) recordClosedTrade(ctx context.Context, userID int64, confirmation Confirmation, result ExecutionResult) {
	if s.journal == nil || confirmation.Intent.Type != domain.IntentClose {
		return
	}

	outcome := journal.OutcomeBreakeven
	switch {
	case result.RealizedPnL.IsPositive():
		outcome = journal.OutcomeWin
	case result.RealizedPnL.Cmp(decimal.Zero()) < 0:
		outcome = journal.OutcomeLoss
	}

	if err := s.journal.Record(ctx, journal.Trade{
		ID:             confirmation.ID,
		UserID:         userID,
		ConfirmationID: confirmation.ID,
		Symbol:         result.Symbol,
		Side:           result.Side,
		Mode:           result.Mode,
		PnLUSDT:        result.RealizedPnL,
		Outcome:        outcome,
		ClosedAt:       s.clock(),
	}); err != nil {
		s.logger.Warn("journal record failed", "confirmation_id", shortID(confirmation.ID), "error", err)
	}
}

func (s *Service) Cancel(ctx context.Context, userID int64, id string) error {
	err := s.store.Cancel(ctx, userID, id, s.clock())
	if err == nil {
		s.updateIntentStatus(ctx, id, IntentStatusCancelled, "")
		s.recordAudit(ctx, audit.Event{
			Type:   "confirmation_cancelled",
			Source: "orders",
			UserID: userID,
			Metadata: map[string]string{
				"confirmation_id": shortID(id),
			},
		})
	}
	if err == ErrConfirmationExpired {
		s.updateIntentStatus(ctx, id, IntentStatusExpired, err.Error())
	}
	return err
}

func (s *Service) TTL() time.Duration {
	return s.ttl
}

func (s *Service) updateIntentStatus(ctx context.Context, id string, status IntentStatus, errorMessage string) {
	if err := s.intentStore.UpdateIntentStatus(ctx, id, status, errorMessage, s.clock()); err != nil {
		s.logger.Warn("intent status update failed", "intent_id", shortID(id), "status", status, "error", err)
	}
}

func (s *Service) recordAudit(ctx context.Context, event audit.Event) {
	if event.CreatedAt.IsZero() {
		event.CreatedAt = s.clock()
	}
	if err := s.auditRecorder.Record(ctx, event); err != nil {
		s.logger.Warn("audit record failed", "event_type", event.Type, "error", err)
	}
}

type DryRunExecutor struct {
	DryRun bool
}

func (e DryRunExecutor) Execute(ctx context.Context, confirmation Confirmation) (ExecutionResult, error) {
	select {
	case <-ctx.Done():
		return ExecutionResult{}, ctx.Err()
	default:
	}

	if !e.DryRun {
		return ExecutionResult{}, fmt.Errorf("exchange executor is not connected yet; keep DRY_RUN=true until the Binance adapter is wired")
	}

	clientOrderID := clientOrderID(confirmation.ID)
	return ExecutionResult{
		Mode:          "dry_run",
		ClientOrderID: clientOrderID,
		Message:       "DRY-RUN accepted. No order was sent to Binance.\n\n" + Summary(confirmation.Intent),
	}, nil
}

type MemoryStore struct {
	mu    sync.Mutex
	items map[string]*confirmationRecord
}

type confirmationRecord struct {
	confirmation Confirmation
	result       ExecutionResult
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{items: make(map[string]*confirmationRecord)}
}

func (s *MemoryStore) Put(ctx context.Context, confirmation Confirmation) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.items[confirmation.ID] = &confirmationRecord{confirmation: confirmation}
	return nil
}

func (s *MemoryStore) TakeForExecution(ctx context.Context, userID int64, id string, now time.Time) (Confirmation, ExecutionResult, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	record, ok := s.items[id]
	if !ok {
		return Confirmation{}, ExecutionResult{}, false, ErrConfirmationNotFound
	}
	if record.confirmation.UserID != userID {
		return Confirmation{}, ExecutionResult{}, false, ErrConfirmationForbidden
	}
	if record.confirmation.Status == StatusExecuted {
		return record.confirmation, record.result, true, nil
	}
	if record.confirmation.Status == StatusCancelled {
		return Confirmation{}, ExecutionResult{}, false, ErrConfirmationCancelled
	}
	if record.confirmation.Status == StatusFailed {
		return Confirmation{}, ExecutionResult{}, false, ErrConfirmationFailed
	}
	if record.confirmation.Status == StatusExecuting {
		return Confirmation{}, ExecutionResult{}, false, ErrConfirmationExecuting
	}
	if !now.Before(record.confirmation.ExpiresAt) {
		record.confirmation.Status = StatusExpired
		return Confirmation{}, ExecutionResult{}, false, ErrConfirmationExpired
	}

	record.confirmation.Status = StatusExecuting
	return record.confirmation, ExecutionResult{}, false, nil
}

func (s *MemoryStore) Complete(ctx context.Context, id string, result ExecutionResult) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	record, ok := s.items[id]
	if !ok {
		return ErrConfirmationNotFound
	}
	record.confirmation.Status = StatusExecuted
	record.result = result
	return nil
}

func (s *MemoryStore) Fail(ctx context.Context, id string, message string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	record, ok := s.items[id]
	if !ok {
		return ErrConfirmationNotFound
	}
	record.confirmation.Status = StatusFailed
	return nil
}

func (s *MemoryStore) Cancel(ctx context.Context, userID int64, id string, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	record, ok := s.items[id]
	if !ok {
		return ErrConfirmationNotFound
	}
	if record.confirmation.UserID != userID {
		return ErrConfirmationForbidden
	}
	if record.confirmation.Status == StatusCancelled {
		return nil
	}
	if record.confirmation.Status == StatusExecuted {
		return ErrConfirmationExecuted
	}
	if !now.Before(record.confirmation.ExpiresAt) {
		record.confirmation.Status = StatusExpired
		return ErrConfirmationExpired
	}

	record.confirmation.Status = StatusCancelled
	return nil
}

func newConfirmationID() (string, error) {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "", fmt.Errorf("generate confirmation id: %w", err)
	}

	return base64.RawURLEncoding.EncodeToString(bytes[:]), nil
}

func clientOrderID(confirmationID string) string {
	id := shortID(confirmationID)
	return "tb_" + id
}

func shortID(id string) string {
	if len(id) <= 12 {
		return id
	}
	return id[:12]
}
