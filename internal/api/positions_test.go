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
	"bottrade/internal/decimal"
	"bottrade/internal/domain"
	"bottrade/internal/orders"
)

func TestPositionsAndClose(t *testing.T) {
	cfg := testConfigWith(t, map[string]string{"ACCESS_OPEN": "true"})
	tk, _ := auth.NewTokenizer(bytes.Repeat([]byte("k"), auth.MinSecretSize), 0)
	user, _ := tk.Issue("tg:9", "u", "user")
	server := NewServer(cfg, nil, testLogger(), WithTokenizer(tk),
		WithOrders(orders.NewService(true, time.Minute, testLogger())))

	// GET positions: the dry-run executor exposes none → empty list, no error.
	req := httptest.NewRequest(http.MethodGet, "/api/positions", nil)
	req.Header.Set("Authorization", "Bearer "+user)
	resp, _ := server.App().Test(req)
	var got map[string]any
	json.NewDecoder(resp.Body).Decode(&got)
	resp.Body.Close()
	if ps, _ := got["positions"].([]any); len(ps) != 0 {
		t.Fatalf("dry-run positions = %v, want empty", got["positions"])
	}

	// POST close: stages a confirmable 100% close (reuses Prepare → Confirm).
	body, _ := json.Marshal(map[string]any{"symbol": "BTC"})
	creq := httptest.NewRequest(http.MethodPost, "/api/positions/close", bytes.NewReader(body))
	creq.Header.Set("Content-Type", "application/json")
	creq.Header.Set("Authorization", "Bearer "+user)
	cresp, _ := server.App().Test(creq)
	var cout map[string]any
	json.NewDecoder(cresp.Body).Decode(&cout)
	cresp.Body.Close()
	if cout["confirm_id"] == nil || cout["confirm_id"] == "" {
		t.Fatalf("close should stage a confirm_id, got %v", cout)
	}
	if out, _ := cout["output"].(string); !strings.Contains(out, "Close BTCUSDT") {
		t.Fatalf("close output = %q", cout["output"])
	}
}

func TestPositionsIncludesNotionalAndMargin(t *testing.T) {
	cfg := testConfigWith(t, map[string]string{"ACCESS_OPEN": "true"})
	tk, _ := auth.NewTokenizer(bytes.Repeat([]byte("k"), auth.MinSecretSize), 0)
	user, _ := tk.Issue("tg:9", "u", "user")
	executor := positionExecutor{positions: []domain.Position{{
		Symbol:           "BTCUSDT",
		Side:             domain.PositionSideLong,
		Amount:           decimal.MustParse("0.01"),
		EntryPrice:       decimal.MustParse("60000"),
		MarkPrice:        decimal.MustParse("61000"),
		UnrealizedProfit: decimal.MustParse("10"),
		Leverage:         20,
	}}}
	server := NewServer(cfg, nil, testLogger(), WithTokenizer(tk),
		WithOrders(orders.NewServiceWithExecutor(time.Minute, executor, testLogger())))

	req := httptest.NewRequest(http.MethodGet, "/api/positions", nil)
	req.Header.Set("Authorization", "Bearer "+user)
	resp, _ := server.App().Test(req)
	defer resp.Body.Close()
	var got struct {
		Positions []map[string]any `json:"positions"`
	}
	json.NewDecoder(resp.Body).Decode(&got)
	if len(got.Positions) != 1 {
		t.Fatalf("positions = %d, want 1", len(got.Positions))
	}
	if got.Positions[0]["notional_usdt"] != "610" || got.Positions[0]["margin_usdt"] != "30.5" {
		t.Fatalf("position sizing fields = %v", got.Positions[0])
	}
}

type positionExecutor struct {
	orders.DryRunExecutor
	positions []domain.Position
}

func (e positionExecutor) Positions(context.Context) ([]domain.Position, error) {
	return e.positions, nil
}
