package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"bottrade/internal/auth"
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
