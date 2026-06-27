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

func TestCommandEndpoint(t *testing.T) {
	tk, err := auth.NewTokenizer(bytes.Repeat([]byte("k"), auth.MinSecretSize), 0)
	if err != nil {
		t.Fatalf("NewTokenizer: %v", err)
	}
	token, err := tk.Issue("tg:468848033", "hotkhwan", "user")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	orderService := orders.NewService(true, time.Minute, testLogger()) // dry-run
	server := NewServer(testConfig(), nil, testLogger(), WithTokenizer(tk), WithOrders(orderService))

	run := func(command, bearer string) (int, string) {
		payload, _ := json.Marshal(map[string]string{"command": command})
		req := httptest.NewRequest(http.MethodPost, "/api/command", bytes.NewReader(payload))
		req.Header.Set("Content-Type", "application/json")
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		resp, err := server.App().Test(req)
		if err != nil {
			t.Fatalf("Test: %v", err)
		}
		defer resp.Body.Close()
		var out struct {
			Output string `json:"output"`
		}
		json.NewDecoder(resp.Body).Decode(&out)
		return resp.StatusCode, out.Output
	}

	// Unauthenticated → 401.
	if code, _ := run("help", ""); code != http.StatusUnauthorized {
		t.Fatalf("no-auth status = %d, want 401", code)
	}

	// help lists the console commands.
	if code, out := run("help", token); code != http.StatusOK || !strings.Contains(out, "Web console") {
		t.Fatalf("help = %d %q", code, out)
	}

	// goal runs offline (parse + simulate) — the redesigned profit + risk% form.
	code, out := run("goal profit 10 risk 50", token)
	if code != http.StatusOK || !strings.Contains(out, "Goal: make 10 USDT") || !strings.Contains(out, "Risk: up to 50%") {
		t.Fatalf("goal = %d %q", code, out)
	}

	// An unknown command falls back to help.
	if _, out := run("frobnicate", token); !strings.Contains(out, "Unknown command") {
		t.Fatalf("unknown command output = %q", out)
	}

	// A trade command prepares and returns a confirm_id; /api/confirm executes
	// it (dry-run).
	payload, _ := json.Marshal(map[string]string{"command": "long BTC 3x entry 67500 sl 65000 tp 72000 size 100usdt"})
	req := httptest.NewRequest(http.MethodPost, "/api/command", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, _ := server.App().Test(req)
	var prep struct {
		Output    string `json:"output"`
		ConfirmID string `json:"confirm_id"`
	}
	json.NewDecoder(resp.Body).Decode(&prep)
	resp.Body.Close()
	if prep.ConfirmID == "" || !strings.Contains(prep.Output, "Review") {
		t.Fatalf("trade prepare = %+v, want a confirm_id and review", prep)
	}

	cbody, _ := json.Marshal(map[string]any{"id": prep.ConfirmID})
	creq := httptest.NewRequest(http.MethodPost, "/api/confirm", bytes.NewReader(cbody))
	creq.Header.Set("Content-Type", "application/json")
	creq.Header.Set("Authorization", "Bearer "+token)
	cresp, _ := server.App().Test(creq)
	var conf struct {
		Output string `json:"output"`
	}
	json.NewDecoder(cresp.Body).Decode(&conf)
	cresp.Body.Close()
	if !strings.Contains(conf.Output, "DRY-RUN") {
		t.Fatalf("confirm output = %q, want a dry-run result", conf.Output)
	}
}
