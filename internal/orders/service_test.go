package orders

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"bottrade/internal/audit"
	"bottrade/internal/decimal"
	"bottrade/internal/domain"
	"bottrade/internal/journal"
)

type stubExecutor struct{ result ExecutionResult }

func (s stubExecutor) Execute(context.Context, Confirmation) (ExecutionResult, error) {
	return s.result, nil
}

type recordingJournal struct{ trades []journal.Trade }

func (r *recordingJournal) Record(_ context.Context, trade journal.Trade) error {
	r.trades = append(r.trades, trade)
	return nil
}

func TestServiceJournalsClosedTradeOnly(t *testing.T) {
	jrnl := &recordingJournal{}
	exec := stubExecutor{result: ExecutionResult{
		Mode:          "binance_testnet",
		ClientOrderID: "close-1",
		Symbol:        "BTCUSDT",
		Side:          "long",
		RealizedPnL:   decimal.MustParse("2.5"),
	}}
	service := NewServiceWithRepositories(5*time.Minute, exec, ServiceDependencies{Journal: jrnl}, testLogger())
	ctx := context.Background()

	closeIntent := domain.Intent{
		Type:  domain.IntentClose,
		Close: &domain.CloseIntent{Symbol: "BTCUSDT", All: true, ResolvedPercent: decimal.NewFromInt(100)},
	}
	conf, err := service.Prepare(ctx, 12345, closeIntent)
	if err != nil {
		t.Fatalf("Prepare close: %v", err)
	}
	if _, err := service.Confirm(ctx, 12345, conf.ID); err != nil {
		t.Fatalf("Confirm close: %v", err)
	}

	if len(jrnl.trades) != 1 {
		t.Fatalf("journaled %d trades, want 1 (the close)", len(jrnl.trades))
	}
	tr := jrnl.trades[0]
	if tr.Outcome != journal.OutcomeWin || tr.PnLUSDT.String() != "2.5" || tr.Symbol != "BTCUSDT" || tr.Side != "long" || tr.UserID != 12345 {
		t.Fatalf("journaled trade = %+v, want win 2.5 BTCUSDT long for user 12345", tr)
	}

	// An open must NOT be journaled (only resolved round-trips are counted).
	openConf, err := service.Prepare(ctx, 12345, testOpenIntent())
	if err != nil {
		t.Fatalf("Prepare open: %v", err)
	}
	if _, err := service.Confirm(ctx, 12345, openConf.ID); err != nil {
		t.Fatalf("Confirm open: %v", err)
	}
	if len(jrnl.trades) != 1 {
		t.Fatalf("journaled %d trades after an open, want still 1", len(jrnl.trades))
	}
}

func TestServiceConfirmExecutesDryRunOnce(t *testing.T) {
	service := NewServiceWithExecutor(5*time.Minute, DryRunExecutor{DryRun: true}, testLogger())
	intent := testOpenIntent()

	confirmation, err := service.Prepare(context.Background(), 12345, intent)
	if err != nil {
		t.Fatalf("Prepare returned error: %v", err)
	}

	result, err := service.Confirm(context.Background(), 12345, confirmation.ID)
	if err != nil {
		t.Fatalf("Confirm returned error: %v", err)
	}
	if result.Mode != "dry_run" {
		t.Fatalf("Mode = %q, want dry_run", result.Mode)
	}
	if result.ClientOrderID == "" {
		t.Fatal("ClientOrderID is empty")
	}

	again, err := service.Confirm(context.Background(), 12345, confirmation.ID)
	if err != nil {
		t.Fatalf("second Confirm returned error: %v", err)
	}
	if again.ClientOrderID != result.ClientOrderID {
		t.Fatalf("second ClientOrderID = %q, want %q", again.ClientOrderID, result.ClientOrderID)
	}
}

func TestServiceCancelIsIdempotent(t *testing.T) {
	service := NewServiceWithExecutor(5*time.Minute, DryRunExecutor{DryRun: true}, testLogger())
	confirmation, err := service.Prepare(context.Background(), 12345, testOpenIntent())
	if err != nil {
		t.Fatalf("Prepare returned error: %v", err)
	}

	if err := service.Cancel(context.Background(), 12345, confirmation.ID); err != nil {
		t.Fatalf("Cancel returned error: %v", err)
	}
	if err := service.Cancel(context.Background(), 12345, confirmation.ID); err != nil {
		t.Fatalf("second Cancel returned error: %v", err)
	}

	_, err = service.Confirm(context.Background(), 12345, confirmation.ID)
	if !errors.Is(err, ErrConfirmationCancelled) {
		t.Fatalf("Confirm error = %v, want ErrConfirmationCancelled", err)
	}
}

func TestServiceCancelAfterExecutionIsRejected(t *testing.T) {
	service := NewServiceWithExecutor(5*time.Minute, DryRunExecutor{DryRun: true}, testLogger())
	confirmation, err := service.Prepare(context.Background(), 12345, testOpenIntent())
	if err != nil {
		t.Fatalf("Prepare returned error: %v", err)
	}

	if _, err := service.Confirm(context.Background(), 12345, confirmation.ID); err != nil {
		t.Fatalf("Confirm returned error: %v", err)
	}

	err = service.Cancel(context.Background(), 12345, confirmation.ID)
	if !errors.Is(err, ErrConfirmationExecuted) {
		t.Fatalf("Cancel error = %v, want ErrConfirmationExecuted", err)
	}
}

func TestServicePreparePersistsIntentAndAudit(t *testing.T) {
	intentStore := &recordingIntentStore{}
	auditRecorder := &recordingAuditRecorder{}
	service := NewServiceWithRepositories(5*time.Minute, DryRunExecutor{DryRun: true}, ServiceDependencies{
		ConfirmationStore: NewMemoryStore(),
		IntentStore:       intentStore,
		AuditRecorder:     auditRecorder,
	}, testLogger())

	confirmation, err := service.Prepare(context.Background(), 12345, testOpenIntent())
	if err != nil {
		t.Fatalf("Prepare returned error: %v", err)
	}
	if confirmation.IntentHash == "" || confirmation.CorrelationID == "" || confirmation.IdempotencyKey == "" {
		t.Fatalf("confirmation metadata is incomplete: %#v", confirmation)
	}
	if len(intentStore.records) != 1 {
		t.Fatalf("intent records = %d, want 1", len(intentStore.records))
	}
	record := intentStore.records[0]
	if record.ConfirmationID != confirmation.ID || record.IntentHash != confirmation.IntentHash {
		t.Fatalf("intent record = %#v, want confirmation linkage", record)
	}
	if len(auditRecorder.events) != 1 || auditRecorder.events[0].Type != "confirmation_created" {
		t.Fatalf("audit events = %#v, want confirmation_created", auditRecorder.events)
	}
}

func TestServiceRejectsWrongUser(t *testing.T) {
	service := NewServiceWithExecutor(5*time.Minute, DryRunExecutor{DryRun: true}, testLogger())
	confirmation, err := service.Prepare(context.Background(), 12345, testOpenIntent())
	if err != nil {
		t.Fatalf("Prepare returned error: %v", err)
	}

	_, err = service.Confirm(context.Background(), 67890, confirmation.ID)
	if !errors.Is(err, ErrConfirmationForbidden) {
		t.Fatalf("Confirm error = %v, want ErrConfirmationForbidden", err)
	}
}

func TestServiceRejectsExpiredConfirmation(t *testing.T) {
	service := NewServiceWithExecutor(time.Second, DryRunExecutor{DryRun: true}, testLogger())
	now := time.Date(2026, 5, 14, 9, 0, 0, 0, time.UTC)
	service.clock = func() time.Time { return now }

	confirmation, err := service.Prepare(context.Background(), 12345, testOpenIntent())
	if err != nil {
		t.Fatalf("Prepare returned error: %v", err)
	}

	now = now.Add(2 * time.Second)
	_, err = service.Confirm(context.Background(), 12345, confirmation.ID)
	if !errors.Is(err, ErrConfirmationExpired) {
		t.Fatalf("Confirm error = %v, want ErrConfirmationExpired", err)
	}
}

func testOpenIntent() domain.Intent {
	return domain.Intent{
		Type: domain.IntentOpen,
		Open: &domain.OpenIntent{
			Symbol:   "BTCUSDT",
			Side:     domain.SideLong,
			Leverage: 3,
			Entry:    decimal.MustParse("67500"),
			StopLoss: decimal.MustParse("65000"),
			TakeProfits: []decimal.Decimal{
				decimal.MustParse("72000"),
			},
			Size: domain.OrderSize{
				Kind:   domain.SizeUSDT,
				Amount: decimal.MustParse("100"),
			},
		},
	}
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

type recordingIntentStore struct {
	records []IntentRecord
	updates []IntentStatus
}

func (s *recordingIntentStore) PutIntentRecord(ctx context.Context, record IntentRecord) error {
	s.records = append(s.records, record)
	return nil
}

func (s *recordingIntentStore) UpdateIntentStatus(ctx context.Context, id string, status IntentStatus, errorMessage string, updatedAt time.Time) error {
	s.updates = append(s.updates, status)
	return nil
}

type recordingAuditRecorder struct {
	events []audit.Event
}

func (r *recordingAuditRecorder) Record(ctx context.Context, event audit.Event) error {
	r.events = append(r.events, event)
	return nil
}
