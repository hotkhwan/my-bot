package orders

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
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
	// Set on an open: the base quantity that actually reached the exchange.
	// The result reconciler uses it to bound TP/SL exit fills to this mission.
	Quantity decimal.Decimal
	// Set on a close: the symbol/side that was closed and the realized PnL, so
	// the trade journal can record the round-trip outcome.
	Symbol      string
	Side        string
	ExitPrice   decimal.Decimal
	RealizedPnL decimal.Decimal
}

type RealizedTrade struct {
	Symbol      string
	Side        string
	ExitPrice   decimal.Decimal
	RealizedPnL decimal.Decimal
	ClosedAt    time.Time
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
	Close(ctx context.Context, id string, update journal.CloseUpdate) (journal.Trade, bool, error)
}

type ConfirmationStore interface {
	Put(ctx context.Context, confirmation Confirmation) error
	Get(ctx context.Context, id string) (Confirmation, bool, error)
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

// PositionReader is the optional slice of an Executor that can read open
// positions (the live Binance executor satisfies it; a dry-run one need not).
type PositionReader interface {
	Positions(ctx context.Context) ([]domain.Position, error)
}

// RealizedTradeReader is the optional slice of an Executor that can read
// exchange-realized PnL for a closed position.
type RealizedTradeReader interface {
	RealizedTrade(ctx context.Context, symbol, side string, since time.Time, entryQty decimal.Decimal) (RealizedTrade, bool, error)
}

// Positions returns the user's open positions on their own account, or nil when
// the resolved executor cannot read them (e.g. dry-run).
func (s *Service) Positions(ctx context.Context, userID int64) ([]domain.Position, error) {
	pr, ok := s.executorForUser(ctx, userID).(PositionReader)
	if !ok {
		return nil, nil
	}
	return pr.Positions(ctx)
}

// PositionsWithRequiredUserExecutor reads positions only from the user's
// resolved executor. Automated mission management uses this to avoid silently
// falling back to a shared/default account.
func (s *Service) PositionsWithRequiredUserExecutor(ctx context.Context, userID int64) ([]domain.Position, error) {
	executor, err := s.requiredExecutorForUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	pr, ok := executor.(PositionReader)
	if !ok {
		return nil, nil
	}
	return pr.Positions(ctx)
}

func (s *Service) RealizedTradeWithRequiredUserExecutor(ctx context.Context, userID int64, symbol, side string, since time.Time, entryQty decimal.Decimal) (RealizedTrade, bool, error) {
	executor, err := s.requiredExecutorForUser(ctx, userID)
	if err != nil {
		return RealizedTrade{}, false, err
	}
	reader, ok := executor.(RealizedTradeReader)
	if !ok {
		return RealizedTrade{}, false, fmt.Errorf("per-user executor cannot read realized trades")
	}
	return reader.RealizedTrade(ctx, symbol, side, since, entryQty)
}

func (s *Service) Prepare(ctx context.Context, userID int64, intent domain.Intent) (Confirmation, error) {
	return s.prepare(ctx, userID, intent, "")
}

// PrepareWithIdempotencyKey is for pre-authorized automation where another
// durable record owns the single-entry claim. Normal user-confirmed flows should
// call Prepare so their confirmation id remains the idempotency identity.
func (s *Service) PrepareWithIdempotencyKey(ctx context.Context, userID int64, intent domain.Intent, idempotencyKey string) (Confirmation, error) {
	return s.prepare(ctx, userID, intent, idempotencyKey)
}

func (s *Service) prepare(ctx context.Context, userID int64, intent domain.Intent, idempotencyKey string) (Confirmation, error) {
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
	idempotencyKey = strings.TrimSpace(idempotencyKey)
	if idempotencyKey == "" {
		idempotencyKey = "confirm:" + id
	}

	now := s.clock()
	confirmation := Confirmation{
		ID:             id,
		UserID:         userID,
		Intent:         intent,
		Status:         StatusPending,
		CorrelationID:  correlationID,
		IntentHash:     intentHash,
		IdempotencyKey: idempotencyKey,
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

// ConfirmationStatus reports a confirmation's durable status, so callers can
// reconcile dependent work (e.g. a scheduled close) against whether the entry
// actually executed. Returns ok=false when the confirmation is unknown/purged.
func (s *Service) ConfirmationStatus(ctx context.Context, id string) (ConfirmationStatus, bool, error) {
	confirmation, ok, err := s.store.Get(ctx, id)
	if err != nil || !ok {
		return "", false, err
	}
	return confirmation.Status, true, nil
}

func (s *Service) Confirm(ctx context.Context, userID int64, id string) (ExecutionResult, error) {
	return s.confirm(ctx, userID, id, false)
}

// ConfirmWithRequiredUserExecutor confirms a prepared order only through the
// user's resolved executor. It is used for pre-authorized automation where
// falling back to a shared/default executor would violate the user's key scope.
func (s *Service) ConfirmWithRequiredUserExecutor(ctx context.Context, userID int64, id string) (ExecutionResult, error) {
	return s.confirm(ctx, userID, id, true)
}

func (s *Service) confirm(ctx context.Context, userID int64, id string, requireUserExecutor bool) (ExecutionResult, error) {
	confirmation, result, done, err := s.store.TakeForExecution(ctx, userID, id, s.clock())
	if err != nil {
		return ExecutionResult{}, err
	}
	if done {
		return result, nil
	}

	s.updateIntentStatus(ctx, confirmation.ID, IntentStatusExecuting, "")
	var executor Executor
	if requireUserExecutor {
		executor, err = s.requiredExecutorForUser(ctx, userID)
		if err != nil {
			s.failExecutingConfirmation(ctx, id, confirmation, err)
			return ExecutionResult{}, err
		}
	} else {
		executor = s.executorForUser(ctx, userID)
	}
	result, err = executor.Execute(ctx, confirmation)
	if err != nil {
		s.failExecutingConfirmation(ctx, id, confirmation, err)
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
	s.recordOpenTrade(ctx, userID, confirmation, result)
	s.recordClosedTrade(ctx, userID, confirmation, result)

	return result, nil
}

func (s *Service) requiredExecutorForUser(ctx context.Context, userID int64) (Executor, error) {
	if s.executorFor == nil {
		return nil, fmt.Errorf("per-user executor is required")
	}
	executor, ok, err := s.executorFor.ExecutorFor(ctx, TraderKey(userID))
	if err != nil {
		return nil, fmt.Errorf("per-user executor lookup: %w", err)
	}
	if !ok || executor == nil {
		return nil, fmt.Errorf("active per-user executor is required")
	}
	return executor, nil
}

func (s *Service) failExecutingConfirmation(ctx context.Context, id string, confirmation Confirmation, err error) {
	_ = s.store.Fail(ctx, id, err.Error())
	s.updateIntentStatus(ctx, confirmation.ID, IntentStatusFailed, err.Error())
	s.recordAudit(ctx, audit.Event{
		Type:          "confirmation_failed",
		Source:        "orders",
		UserID:        confirmation.UserID,
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
		"user_id", confirmation.UserID,
		"intent_type", confirmation.Intent.Type,
		"error", err.Error(),
	)
}

// recordOpenTrade journals the decision context at entry confirmation. The row
// is keyed by the entry confirmation id so exchange-side TP/SL exits can update
// the same record later.
func (s *Service) recordOpenTrade(ctx context.Context, userID int64, confirmation Confirmation, result ExecutionResult) {
	if s.journal == nil || confirmation.Intent.Type != domain.IntentOpen || confirmation.Intent.Open == nil {
		return
	}
	if strings.EqualFold(strings.TrimSpace(result.Mode), "dry_run") {
		return
	}
	open := confirmation.Intent.Open
	tp := decimal.Zero()
	if len(open.TakeProfits) > 0 {
		tp = open.TakeProfits[0]
	}
	trade := journal.Trade{
		ID:             confirmation.ID,
		UserID:         userID,
		CampaignID:     strings.TrimSpace(open.CampaignID),
		ConfirmationID: confirmation.ID,
		Symbol:         open.Symbol,
		Side:           string(open.Side),
		Strategy:       strings.TrimSpace(open.Strategy),
		Models:         append([]string(nil), open.Models...),
		Reason:         strings.TrimSpace(open.Reason),
		Confidence:     open.Confidence,
		Leverage:       open.Leverage,
		Mode:           result.Mode,
		Entry:          open.Entry,
		StopLoss:       open.StopLoss,
		TakeProfit:     tp,
		Outcome:        journal.OutcomeOpen,
		OpenedAt:       s.clock(),
	}
	switch open.Size.Kind {
	case domain.SizeUSDT:
		trade.SizeUSDT = open.Size.Amount
	case domain.SizeQty:
		trade.Quantity = open.Size.Amount
	}
	if result.Quantity.IsPositive() {
		trade.Quantity = result.Quantity
	}
	if err := s.journal.Record(ctx, trade); err != nil {
		s.logger.Warn("journal open record failed", "confirmation_id", shortID(confirmation.ID), "error", err)
	}
}

// recordClosedTrade journals a completed round-trip when a close executes, using
// the realized PnL the executor reported. When the close carries an entry
// confirmation id it atomically closes that open row; otherwise it falls back to
// a standalone close row for manual legacy closes.
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

	entryConfirmationID := ""
	if confirmation.Intent.Close != nil {
		entryConfirmationID = strings.TrimSpace(confirmation.Intent.Close.EntryConfirmationID)
	}
	if entryConfirmationID != "" {
		_, ok, err := s.journal.Close(ctx, entryConfirmationID, journal.CloseUpdate{
			Exit:     result.ExitPrice,
			PnLUSDT:  result.RealizedPnL,
			Outcome:  outcome,
			ClosedAt: s.clock(),
			Mode:     result.Mode,
		})
		if err != nil {
			s.logger.Warn("journal close update failed", "entry_confirmation_id", shortID(entryConfirmationID), "confirmation_id", shortID(confirmation.ID), "error", err)
		} else if !ok {
			s.logger.Warn("journal close update skipped", "entry_confirmation_id", shortID(entryConfirmationID), "confirmation_id", shortID(confirmation.ID), "reason", "open row not found")
		}
		return
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

	if confirmation.IdempotencyKey != "" {
		for _, record := range s.items {
			if record.confirmation.IdempotencyKey == confirmation.IdempotencyKey {
				return fmt.Errorf("confirmation idempotency key already exists")
			}
		}
	}
	s.items[confirmation.ID] = &confirmationRecord{confirmation: confirmation}
	return nil
}

func (s *MemoryStore) Get(_ context.Context, id string) (Confirmation, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.items[id]
	if !ok {
		return Confirmation{}, false, nil
	}
	return record.confirmation, true, nil
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
