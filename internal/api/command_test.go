package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"bottrade/internal/auth"
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
	server := NewServer(testConfig(), nil, testLogger(), WithTokenizer(tk))

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
}
