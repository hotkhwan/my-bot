package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"bottrade/internal/auth"
)

func TestFavouritesPersistPerUser(t *testing.T) {
	tk, _ := auth.NewTokenizer(bytes.Repeat([]byte("k"), auth.MinSecretSize), 0)
	a, _ := tk.Issue("tg:1", "a", "user")
	b, _ := tk.Issue("tg:2", "b", "user")
	server := NewServer(testConfigWith(t, nil), nil, testLogger(), WithTokenizer(tk))

	call := func(method, tok string, body any) map[string]any {
		var rdr *bytes.Reader
		if body != nil {
			raw, _ := json.Marshal(body)
			rdr = bytes.NewReader(raw)
		} else {
			rdr = bytes.NewReader(nil)
		}
		req := httptest.NewRequest(method, "/api/favourites", rdr)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+tok)
		resp, _ := server.App().Test(req)
		defer resp.Body.Close()
		var out map[string]any
		json.NewDecoder(resp.Body).Decode(&out)
		return out
	}
	syms := func(m map[string]any) []string {
		raw, _ := m["symbols"].([]any)
		out := make([]string, len(raw))
		for i, v := range raw {
			out[i], _ = v.(string)
		}
		return out
	}

	// Save for user A: sanitized (upper-cased, junk stripped, de-duped).
	got := syms(call(http.MethodPost, a, map[string]any{"symbols": []string{"btc", "ETH/USDT", "btc", ""}}))
	if len(got) != 2 || got[0] != "BTC" || got[1] != "ETHUSDT" {
		t.Fatalf("saved = %v, want [BTC ETHUSDT]", got)
	}
	// User A reads them back.
	if g := syms(call(http.MethodGet, a, nil)); len(g) != 2 || g[0] != "BTC" {
		t.Fatalf("A reload = %v", g)
	}
	// User B is isolated — sees nothing.
	if g := syms(call(http.MethodGet, b, nil)); len(g) != 0 {
		t.Fatalf("B should have no favourites, got %v", g)
	}
}
