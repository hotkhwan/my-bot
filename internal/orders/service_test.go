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

type recordingJournal struct {
	trades []journal.Trade
	byID   map[string]journal.Trade
}

func (r *recordingJournal) Record(_ context.Context, trade journal.Trade) error {
	if r.byID == nil {
		r.byID = map[string]journal.Trade{}
	}
	r.trades = append(r.trades, trade)
	r.byID[trade.ID] = trade
	return nil
}

func (r *recordingJournal) Close(_ context.Context, id string, update journal.CloseUpdate) (journal.Trade, bool, error) {
	if r.byID == nil {
		r.byID = map[string]journal.Trade{}
	}
	trade, ok := r.byID[id]
	if !ok || trade.Outcome != journal.OutcomeOpen {
		return journal.Trade{}, false, nil
	}
	trade.Exit = update.Exit
	trade.PnLUSDT = update.PnLUSDT
	trade.Outcome = update.Outcome
	trade.ClosedAt = update.ClosedAt
	if update.Mode != "" {
		trade.Mode = update.Mode
	}
	r.byID[id] = trade
	for i := range r.trades {
		if r.trades[i].ID == id {
			r.trades[i] = trade
		}
	}
	return trade, true, nil
}

func TestServiceJournalsOpenAndStandaloneClosedTrade(t *testing.T) {
	jrnl := &recordingJournal{}
	exec := stubExecutor{result: ExecutionResult{
		Mode:          "binance_testnet",
		ClientOrderID: "close-1",
		Quantity:      decimal.MustParse("0.001"),
		Symbol:        "BTCUSDT",
		Side:          "long",
		RealizedPnL:   decimal.MustParse("2.5"),
	}}
	service := NewServiceWithRepositories(5*time.Minute, exec, ServiceDependencies{Journal: jrnl}, testLogger())
	ctx := context.Background()

	openConf, err := service.Prepare(ctx, 12345, testOpenIntent())
	if err != nil {
		t.Fatalf("Prepare open: %v", err)
	}
	if _, err := service.Confirm(ctx, 12345, openConf.ID); err != nil {
		t.Fatalf("Confirm open: %v", err)
	}
	if len(jrnl.trades) != 1 {
		t.Fatalf("journaled %d trades after open, want 1", len(jrnl.trades))
	}
	open := jrnl.trades[0]
	if open.ID != openConf.ID || open.Outcome != journal.OutcomeOpen || open.Strategy != "anny_basic_v1.2" ||
		open.Reason != "CDC and QQE aligned" || open.CampaignID != "mission" {
		t.Fatalf("open journal row = %+v, want persisted mission context", open)
	}
	if open.Quantity.String() != "0.001" {
		t.Fatalf("open quantity = %s, want exchange execution quantity 0.001", open.Quantity)
	}

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

	if len(jrnl.trades) != 2 {
		t.Fatalf("journaled %d trades, want open + standalone close", len(jrnl.trades))
	}
	tr := jrnl.trades[1]
	if tr.Outcome != journal.OutcomeWin || tr.PnLUSDT.String() != "2.5" || tr.Symbol != "BTCUSDT" || tr.Side != "long" || tr.UserID != 12345 {
		t.Fatalf("journaled trade = %+v, want win 2.5 BTCUSDT long for user 12345", tr)
	}
}

func TestServiceSkipsDryRunOpenJournal(t *testing.T) {
	jrnl := &recordingJournal{}
	service := NewServiceWithRepositories(5*time.Minute, DryRunExecutor{DryRun: true}, ServiceDependencies{Journal: jrnl}, testLogger())
	ctx := context.Background()

	conf, err := service.Prepare(ctx, 12345, testOpenIntent())
	if err != nil {
		t.Fatalf("Prepare open: %v", err)
	}
	if _, err := service.Confirm(ctx, 12345, conf.ID); err != nil {
		t.Fatalf("Confirm open: %v", err)
	}
	if len(jrnl.trades) != 0 {
		t.Fatalf("journaled %d dry-run trades, want none", len(jrnl.trades))
	}
}

func TestServiceCorrelatedCloseUpdatesOpenJournalRow(t *testing.T) {
	jrnl := &recordingJournal{}
	exec := stubExecutor{result: ExecutionResult{
		Mode:          "binance_testnet",
		ClientOrderID: "close-1",
		Symbol:        "BTCUSDT",
		Side:          "long",
		ExitPrice:     decimal.MustParse("101"),
		RealizedPnL:   decimal.MustParse("-1.25"),
	}}
	service := NewServiceWithRepositories(5*time.Minute, exec, ServiceDependencies{Journal: jrnl}, testLogger())
	ctx := context.Background()

	openConf, err := service.Prepare(ctx, 12345, testOpenIntent())
	if err != nil {
		t.Fatalf("Prepare open: %v", err)
	}
	if _, err := service.Confirm(ctx, 12345, openConf.ID); err != nil {
		t.Fatalf("Confirm open: %v", err)
	}
	closeIntent := domain.Intent{Type: domain.IntentClose, Close: &domain.CloseIntent{
		Symbol: "BTCUSDT", All: true, ResolvedPercent: decimal.NewFromInt(100), EntryConfirmationID: openConf.ID,
	}}
	closeConf, err := service.Prepare(ctx, 12345, closeIntent)
	if err != nil {
		t.Fatalf("Prepare close: %v", err)
	}
	if _, err := service.Confirm(ctx, 12345, closeConf.ID); err != nil {
		t.Fatalf("Confirm close: %v", err)
	}
	if len(jrnl.trades) != 1 {
		t.Fatalf("journaled %d trades, want one updated round-trip row", len(jrnl.trades))
	}
	tr := jrnl.trades[0]
	if tr.ID != openConf.ID || tr.Outcome != journal.OutcomeLoss || tr.PnLUSDT.String() != "-1.25" || tr.Exit.String() != "101" {
		t.Fatalf("updated journal row = %+v, want correlated loss on entry id", tr)
	}
}

type stubProvider struct {
	executor Executor
	found    bool
}

func (p stubProvider) ExecutorFor(context.Context, string) (Executor, bool, error) {
	return p.executor, p.found, nil
}

func TestServiceUsesPerUserExecutorWithFallback(t *testing.T) {
	ctx := context.Background()
	perUser := stubExecutor{result: ExecutionResult{Mode: "per-user", ClientOrderID: "pu"}}

	withKey := NewServiceWithRepositories(5*time.Minute, DryRunExecutor{DryRun: true},
		ServiceDependencies{ExecutorProvider: stubProvider{executor: perUser, found: true}}, testLogger())
	conf, err := withKey.Prepare(ctx, 12345, testOpenIntent())
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	res, err := withKey.Confirm(ctx, 12345, conf.ID)
	if err != nil {
		t.Fatalf("confirm: %v", err)
	}
	if res.Mode != "per-user" {
		t.Fatalf("mode = %q, want per-user (the user's own executor)", res.Mode)
	}

	// A user with no stored key falls back to the default executor.
	fallback := NewServiceWithRepositories(5*time.Minute, DryRunExecutor{DryRun: true},
		ServiceDependencies{ExecutorProvider: stubProvider{found: false}}, testLogger())
	conf2, _ := fallback.Prepare(ctx, 12345, testOpenIntent())
	res2, err := fallback.Confirm(ctx, 12345, conf2.ID)
	if err != nil {
		t.Fatalf("confirm fallback: %v", err)
	}
	if res2.Mode != "dry_run" {
		t.Fatalf("mode = %q, want dry_run (fallback to default)", res2.Mode)
	}
}

func TestServicePrepareWithIdempotencyKeyRejectsDuplicate(t *testing.T) {
	service := NewServiceWithExecutor(5*time.Minute, DryRunExecutor{DryRun: true}, testLogger())
	ctx := context.Background()
	if _, err := service.PrepareWithIdempotencyKey(ctx, 12345, testOpenIntent(), "armed:one"); err != nil {
		t.Fatalf("first prepare: %v", err)
	}
	if _, err := service.PrepareWithIdempotencyKey(ctx, 12345, testOpenIntent(), "armed:one"); err == nil {
		t.Fatal("second prepare with same idempotency key succeeded, want duplicate rejection")
	}
}

func TestServiceConfirmWithRequiredUserExecutorRejectsFallback(t *testing.T) {
	ctx := context.Background()
	service := NewServiceWithRepositories(5*time.Minute, DryRunExecutor{DryRun: true},
		ServiceDependencies{ExecutorProvider: stubProvider{found: false}}, testLogger())
	conf, err := service.Prepare(ctx, 12345, testOpenIntent())
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if _, err := service.ConfirmWithRequiredUserExecutor(ctx, 12345, conf.ID); err == nil {
		t.Fatal("ConfirmWithRequiredUserExecutor succeeded without a per-user executor")
	}

	perUser := stubExecutor{result: ExecutionResult{Mode: "per-user", ClientOrderID: "pu"}}
	strict := NewServiceWithRepositories(5*time.Minute, DryRunExecutor{DryRun: true},
		ServiceDependencies{ExecutorProvider: stubProvider{executor: perUser, found: true}}, testLogger())
	conf, err = strict.Prepare(ctx, 12345, testOpenIntent())
	if err != nil {
		t.Fatalf("strict prepare: %v", err)
	}
	res, err := strict.ConfirmWithRequiredUserExecutor(ctx, 12345, conf.ID)
	if err != nil {
		t.Fatalf("strict confirm: %v", err)
	}
	if res.Mode != "per-user" {
		t.Fatalf("strict mode = %q, want per-user", res.Mode)
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
			Strategy:   "anny_basic_v1.2",
			Models:     []string{"rules"},
			Reason:     "CDC and QQE aligned",
			Confidence: 82,
			CampaignID: "mission",
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
