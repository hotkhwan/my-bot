package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"bottrade/internal/auth"
)

func TestAIKeyStoreAndHide(t *testing.T) {
	tk, _ := auth.NewTokenizer(bytes.Repeat([]byte("k"), auth.MinSecretSize), 0)
	token, _ := tk.Issue("tg:1", "u", "user")
	keyring, err := auth.NewKeyring(map[string][]byte{"v1": bytes.Repeat([]byte("e"), 32)}, "v1")
	if err != nil {
		t.Fatalf("keyring: %v", err)
	}
	server := NewServer(testConfig(), nil, testLogger(),
		WithTokenizer(tk), WithAISecrets(newMemAISecrets(), keyring))

	call := func(method, path, tok string, body any) (int, map[string]any) {
		var rdr *bytes.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			rdr = bytes.NewReader(b)
		} else {
			rdr = bytes.NewReader(nil)
		}
		req := httptest.NewRequest(method, path, rdr)
		req.Header.Set("Content-Type", "application/json")
		if tok != "" {
			req.Header.Set("Authorization", "Bearer "+tok)
		}
		resp, _ := server.App().Test(req)
		defer resp.Body.Close()
		var out map[string]any
		json.NewDecoder(resp.Body).Decode(&out)
		return resp.StatusCode, out
	}

	// Initially no key.
	if _, out := call(http.MethodGet, "/api/ai-key", token, nil); out["set"] != false {
		t.Fatalf("expected no key, got %v", out)
	}
	// Missing model → 400.
	if code, _ := call(http.MethodPost, "/api/ai-key", token, map[string]any{"provider": "anthropic", "api_key": "sk-secret"}); code != http.StatusBadRequest {
		t.Fatalf("missing model status = %d, want 400", code)
	}
	// Store a key.
	if code, _ := call(http.MethodPost, "/api/ai-key", token, map[string]any{"provider": "anthropic", "api_key": "sk-secret-123", "model": "claude-haiku-4-5"}); code != http.StatusCreated {
		t.Fatalf("store status = %d", code)
	}
	// GET reveals provider/model but never the key.
	_, got := call(http.MethodGet, "/api/ai-key", token, nil)
	if got["set"] != true || got["provider"] != "anthropic" || got["model"] != "claude-haiku-4-5" {
		t.Fatalf("get = %v", got)
	}
	for _, v := range got {
		if s, ok := v.(string); ok && s == "sk-secret-123" {
			t.Fatal("plaintext key leaked in GET response")
		}
	}
	// userAdvisor builds a real advisor from the stored key.
	if adv := server.userAdvisor(t.Context(), "tg:1"); adv == nil {
		t.Fatal("expected a per-user advisor from the stored key")
	}
	// Delete.
	if code, _ := call(http.MethodDelete, "/api/ai-key", token, nil); code != http.StatusOK {
		t.Fatalf("delete status = %d", code)
	}
	if _, out := call(http.MethodGet, "/api/ai-key", token, nil); out["set"] != false {
		t.Fatalf("expected no key after delete, got %v", out)
	}
}
