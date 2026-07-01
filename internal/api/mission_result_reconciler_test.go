package api

import (
	"context"
	"testing"
	"time"

	"bottrade/internal/decimal"
	"bottrade/internal/domain"
	"bottrade/internal/journal"
	"bottrade/internal/orders"
)

type missionResultExecutor struct {
	positions    []domain.Position
	realized     orders.RealizedTrade
	found        bool
	requestedQty decimal.Decimal
}

func (e *missionResultExecutor) Execute(context.Context, orders.Confirmation) (orders.ExecutionResult, error) {
	return orders.ExecutionResult{Mode: "binance_testnet", ClientOrderID: "ok"}, nil
}

func (e *missionResultExecutor) Positions(context.Context) ([]domain.Position, error) {
	return e.positions, nil
}

func (e *missionResultExecutor) RealizedTrade(_ context.Context, _ string, _ string, _ time.Time, entryQty decimal.Decimal) (orders.RealizedTrade, bool, error) {
	e.requestedQty = entryQty
	return e.realized, e.found, nil
}

type missionResultPositionOnlyExecutor struct {
	positions []domain.Position
}

func (e *missionResultPositionOnlyExecutor) Execute(context.Context, orders.Confirmation) (orders.ExecutionResult, error) {
	return orders.ExecutionResult{Mode: "binance_testnet", ClientOrderID: "ok"}, nil
}

func (e *missionResultPositionOnlyExecutor) Positions(context.Context) ([]domain.Position, error) {
	return e.positions, nil
}

func missionResultServer(t *testing.T, exec orders.Executor, withKey bool) (*Server, *journal.Service) {
	t.Helper()
	repo := journal.NewMemoryRepository()
	jsvc, err := journal.NewService(repo)
	if err != nil {
		t.Fatalf("journal service: %v", err)
	}
	opts := []Option{
		WithOrders(missionOrderService(exec)),
		WithReport(jsvc),
	}
	if withKey {
		opts = append(opts, WithCredentials(testCredentialService(t, "tg:7", true)))
	}
	return NewServer(testConfigWith(t, missionArmRuntimeEnv("")), nil, testLogger(), opts...), jsvc
}

func seedOpenMissionResult(t *testing.T, svc *journal.Service, side string) journal.Trade {
	t.Helper()
	trade := journal.Trade{
		ID:             "entry-1",
		UserID:         7,
		CampaignID:     "mission",
		ConfirmationID: "entry-1",
		Symbol:         "BTCUSDT",
		Side:           side,
		Strategy:       "anny_basic_v1.2",
		Reason:         "CDC and QQE aligned",
		Leverage:       3,
		Mode:           "binance_testnet",
		Entry:          decimal.MustParse("100"),
		StopLoss:       decimal.MustParse("99"),
		TakeProfit:     decimal.MustParse("102"),
		Quantity:       decimal.MustParse("0.01"),
		Outcome:        journal.OutcomeOpen,
		OpenedAt:       time.Unix(1000, 0).UTC(),
	}
	if err := svc.Record(context.Background(), trade); err != nil {
		t.Fatalf("seed open trade: %v", err)
	}
	return trade
}

func TestMissionResultReconcilerLeavesMatchingOpenPosition(t *testing.T) {
	exec := &missionResultExecutor{positions: []domain.Position{{
		Symbol: "BTCUSDT", Side: domain.PositionSideLong, Amount: decimal.MustParse("0.01"),
	}}}
	server, jsvc := missionResultServer(t, exec, true)
	seedOpenMissionResult(t, jsvc, "long")

	closed, err := server.runMissionResultReconciler(context.Background(), time.Unix(2000, 0).UTC())
	if err != nil || closed != 0 {
		t.Fatalf("reconcile closed=%d err=%v, want no close", closed, err)
	}
	trades, _ := jsvc.List(context.Background(), journal.Filter{OpenOnly: true})
	if len(trades) != 1 {
		t.Fatalf("open trades = %d, want mission still open", len(trades))
	}
}

func TestMissionResultReconcilerClosesGoneSideMatchedPosition(t *testing.T) {
	exec := &missionResultExecutor{
		positions: []domain.Position{{
			Symbol: "BTCUSDT", Side: domain.PositionSideShort, Amount: decimal.MustParse("0.01"),
		}},
		realized: orders.RealizedTrade{
			Symbol: "BTCUSDT", Side: "long", ExitPrice: decimal.MustParse("102"),
			RealizedPnL: decimal.MustParse("1.75"), ClosedAt: time.Unix(2000, 0).UTC(),
		},
		found: true,
	}
	server, jsvc := missionResultServer(t, exec, true)
	seedOpenMissionResult(t, jsvc, "long")

	closed, err := server.runMissionResultReconciler(context.Background(), time.Unix(2100, 0).UTC())
	if err != nil || closed != 1 {
		t.Fatalf("reconcile closed=%d err=%v, want one close", closed, err)
	}
	trades, _ := jsvc.List(context.Background(), journal.Filter{ClosedOnly: true})
	if len(trades) != 1 || trades[0].Outcome != journal.OutcomeWin || trades[0].PnLUSDT.String() != "1.75" ||
		trades[0].Exit.String() != "102" || trades[0].Strategy != "anny_basic_v1.2" {
		t.Fatalf("closed trades = %+v, want complete winning mission row", trades)
	}
	if exec.requestedQty.String() != "0.01" {
		t.Fatalf("realized reader entry qty = %s, want journal quantity 0.01", exec.requestedQty)
	}
	again, err := server.runMissionResultReconciler(context.Background(), time.Unix(2200, 0).UTC())
	if err != nil || again != 0 {
		t.Fatalf("second reconcile closed=%d err=%v, want single-winner no-op", again, err)
	}
}

func TestMissionResultReconcilerWaitsForMinimumAge(t *testing.T) {
	exec := &missionResultExecutor{
		realized: orders.RealizedTrade{Symbol: "BTCUSDT", Side: "long", RealizedPnL: decimal.MustParse("-0.02")},
		found:    true,
	}
	server, jsvc := missionResultServer(t, exec, true)
	trade := seedOpenMissionResult(t, jsvc, "long")

	closed, err := server.runMissionResultReconciler(context.Background(), trade.OpenedAt.Add(10*time.Second))
	if err != nil || closed != 0 {
		t.Fatalf("young reconcile closed=%d err=%v, want no-op", closed, err)
	}
	trades, _ := jsvc.List(context.Background(), journal.Filter{OpenOnly: true})
	if len(trades) != 1 {
		t.Fatalf("open trades = %d, want young mission still open", len(trades))
	}
	if exec.requestedQty.IsPositive() {
		t.Fatalf("realized reader was called before min age with qty %s", exec.requestedQty)
	}
}

func TestMissionResultReconcilerRequiresRealizedReader(t *testing.T) {
	exec := &missionResultPositionOnlyExecutor{}
	server, jsvc := missionResultServer(t, exec, true)
	seedOpenMissionResult(t, jsvc, "long")

	closed, err := server.runMissionResultReconciler(context.Background(), time.Unix(2000, 0).UTC())
	if err != nil || closed != 0 {
		t.Fatalf("missing-reader reconcile closed=%d err=%v, want skip without fatal run error", closed, err)
	}
	trades, _ := jsvc.List(context.Background(), journal.Filter{OpenOnly: true})
	if len(trades) != 1 {
		t.Fatalf("open trades = %d, want row left open when executor cannot read realized result", len(trades))
	}
}

func TestMissionResultReconcilerDoesNotOverwriteAlreadyClosedRow(t *testing.T) {
	exec := &missionResultExecutor{
		realized: orders.RealizedTrade{Symbol: "BTCUSDT", Side: "long", RealizedPnL: decimal.MustParse("-3")},
		found:    true,
	}
	server, jsvc := missionResultServer(t, exec, true)
	seedOpenMissionResult(t, jsvc, "long")
	if _, ok, err := jsvc.Close(context.Background(), "entry-1", journal.CloseUpdate{
		Exit:     decimal.MustParse("101"),
		PnLUSDT:  decimal.MustParse("1"),
		Outcome:  journal.OutcomeWin,
		ClosedAt: time.Unix(1500, 0).UTC(),
		Mode:     "binance_testnet",
	}); err != nil || !ok {
		t.Fatalf("pre-close ok=%v err=%v, want timed close winner", ok, err)
	}

	closed, err := server.runMissionResultReconciler(context.Background(), time.Unix(2000, 0).UTC())
	if err != nil || closed != 0 {
		t.Fatalf("post-close reconcile closed=%d err=%v, want no-op", closed, err)
	}
	trades, _ := jsvc.List(context.Background(), journal.Filter{ClosedOnly: true})
	if len(trades) != 1 || trades[0].Outcome != journal.OutcomeWin || trades[0].PnLUSDT.String() != "1" {
		t.Fatalf("closed trades = %+v, want original timed-close result preserved", trades)
	}
}

func TestMissionResultReconcilerGateOrKeyClosedDoesNothing(t *testing.T) {
	exec := &missionResultExecutor{
		realized: orders.RealizedTrade{Symbol: "BTCUSDT", Side: "long", RealizedPnL: decimal.MustParse("1")},
		found:    true,
	}
	noKey, jsvc := missionResultServer(t, exec, false)
	seedOpenMissionResult(t, jsvc, "long")
	if closed, err := noKey.runMissionResultReconciler(context.Background(), time.Now().UTC()); err != nil || closed != 0 {
		t.Fatalf("no-key reconcile closed=%d err=%v, want no-op", closed, err)
	}
	if trades, _ := jsvc.List(context.Background(), journal.Filter{OpenOnly: true}); len(trades) != 1 {
		t.Fatalf("no-key open trades = %d, want still open", len(trades))
	}

	gateClosed := NewServer(testConfig(), nil, testLogger(),
		WithOrders(missionOrderService(exec)), WithReport(jsvc), WithCredentials(testCredentialService(t, "tg:7", true)))
	if closed, err := gateClosed.runMissionResultReconciler(context.Background(), time.Now().UTC()); err != nil || closed != 0 {
		t.Fatalf("gate-closed reconcile closed=%d err=%v, want no-op", closed, err)
	}
}
