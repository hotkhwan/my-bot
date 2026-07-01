package api

import (
	"context"
	"fmt"
	"time"

	"bottrade/internal/decimal"
	"bottrade/internal/journal"
	"bottrade/internal/orders"
)

const (
	missionResultPollInterval = 30 * time.Second
	missionResultMinCloseAge  = missionResultPollInterval
)

func (s *Server) startMissionResultReconciler(ctx context.Context) {
	if s.report == nil || s.orders == nil {
		return
	}
	go func() {
		run := func() {
			checkCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			if _, err := s.runMissionResultReconciler(checkCtx, time.Now().UTC()); err != nil {
				s.logger.Warn("mission result reconcile failed", "error", err)
			}
			cancel()
		}
		run()
		ticker := time.NewTicker(missionResultPollInterval)
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

func (s *Server) runMissionResultReconciler(ctx context.Context, now time.Time) (int, error) {
	if s.report == nil || s.orders == nil || !s.armedMissionRuntimeAllowed() {
		return 0, nil
	}
	openTrades, err := s.report.List(ctx, journal.Filter{OpenOnly: true})
	if err != nil {
		return 0, err
	}
	closed := 0
	for _, trade := range openTrades {
		if trade.UserID <= 0 {
			continue
		}
		userKey := orders.TraderKey(trade.UserID)
		if !s.hasActiveKeyForSubject(ctx, userKey) {
			continue
		}
		ok, err := s.reconcileOpenMissionResult(ctx, trade, now)
		if err != nil {
			s.logger.Warn("open mission result reconcile skipped", "trade_id", trade.ID, "user_id", trade.UserID, "symbol", trade.Symbol, "error", err)
			continue
		}
		if ok {
			closed++
		}
	}
	return closed, nil
}

func (s *Server) reconcileOpenMissionResult(ctx context.Context, trade journal.Trade, now time.Time) (bool, error) {
	if !trade.OpenedAt.IsZero() && now.Before(trade.OpenedAt.Add(missionResultMinCloseAge)) {
		return false, nil
	}
	if !trade.Quantity.IsPositive() {
		return false, fmt.Errorf("entry quantity is required")
	}
	positions, err := s.orders.PositionsWithRequiredUserExecutor(ctx, trade.UserID)
	if err != nil {
		return false, fmt.Errorf("positions: %w", err)
	}
	if scheduledCloseHasOpenPosition(positions, trade.Symbol, trade.Side) {
		return false, nil
	}
	result, ok, err := s.orders.RealizedTradeWithRequiredUserExecutor(ctx, trade.UserID, trade.Symbol, trade.Side, trade.OpenedAt, trade.Quantity)
	if err != nil {
		return false, fmt.Errorf("realized result: %w", err)
	}
	if !ok {
		return false, nil
	}
	outcome := journal.OutcomeBreakeven
	switch {
	case result.RealizedPnL.IsPositive():
		outcome = journal.OutcomeWin
	case result.RealizedPnL.Cmp(decimal.Zero()) < 0:
		outcome = journal.OutcomeLoss
	}
	closedAt := result.ClosedAt
	if closedAt.IsZero() {
		closedAt = now
	}
	_, updated, err := s.report.Close(ctx, trade.ID, journal.CloseUpdate{
		Exit:     result.ExitPrice,
		PnLUSDT:  result.RealizedPnL,
		Outcome:  outcome,
		ClosedAt: closedAt,
		Mode:     trade.Mode,
	})
	if err != nil {
		return false, err
	}
	if updated {
		s.logger.Info("mission result reconciled", "trade_id", trade.ID, "user_id", trade.UserID, "symbol", trade.Symbol, "side", trade.Side, "outcome", outcome)
	}
	return updated, nil
}
