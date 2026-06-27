package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/url"
	"sort"
	"strings"
	"testing"
	"time"
)

// signInitData builds a valid Mini App initData string for a bot token.
func signInitData(botToken string, fields map[string]string) string {
	keys := make([]string, 0, len(fields))
	for k := range fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	pairs := make([]string, 0, len(keys))
	for _, k := range keys {
		pairs = append(pairs, k+"="+fields[k])
	}
	secret := hmacSum([]byte("WebAppData"), []byte(botToken))
	hash := hex.EncodeToString(hmacSum(secret, []byte(strings.Join(pairs, "\n"))))

	q := url.Values{}
	for k, v := range fields {
		q.Set(k, v)
	}
	q.Set("hash", hash)
	return q.Encode()
}

// signLoginWidget builds a valid Login Widget field set (SHA256-secret scheme).
func signLoginWidget(botToken string, fields map[string]string) map[string]string {
	keys := make([]string, 0, len(fields))
	for k := range fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	pairs := make([]string, 0, len(keys))
	for _, k := range keys {
		pairs = append(pairs, k+"="+fields[k])
	}
	secret := sha256.Sum256([]byte(botToken))
	hash := hex.EncodeToString(hmacSum(secret[:], []byte(strings.Join(pairs, "\n"))))

	out := make(map[string]string, len(fields)+1)
	for k, v := range fields {
		out[k] = v
	}
	out["hash"] = hash
	return out
}

func TestVerifyTelegramLoginWidget(t *testing.T) {
	const token = "123456:bot-token"
	now := time.Unix(1_700_000_100, 0)
	fields := signLoginWidget(token, map[string]string{
		"id":         "987654321",
		"username":   "alice",
		"first_name": "Alice",
		"auth_date":  "1700000050",
	})

	user, err := VerifyTelegramLoginWidget(fields, token, time.Hour, now)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if user.ID != 987654321 || user.Username != "alice" || user.FirstName != "Alice" {
		t.Fatalf("user = %+v", user)
	}
}

func TestVerifyTelegramLoginWidgetRejectsTamperAndStale(t *testing.T) {
	const token = "123456:bot-token"
	now := time.Unix(1_700_000_100, 0)
	fields := signLoginWidget(token, map[string]string{"id": "1", "auth_date": "1700000050"})

	// Tampered id invalidates the hash.
	tampered := map[string]string{}
	for k, v := range fields {
		tampered[k] = v
	}
	tampered["id"] = "2"
	if _, err := VerifyTelegramLoginWidget(tampered, token, time.Hour, now); !errors.Is(err, ErrTelegramAuth) {
		t.Fatalf("tamper err = %v, want ErrTelegramAuth", err)
	}

	// Stale auth_date is rejected.
	stale := signLoginWidget(token, map[string]string{"id": "1", "auth_date": "1700000050"})
	if _, err := VerifyTelegramLoginWidget(stale, token, 10*time.Second, now); !errors.Is(err, ErrTelegramAuth) {
		t.Fatalf("stale err = %v, want ErrTelegramAuth", err)
	}

	// Wrong bot token fails.
	if _, err := VerifyTelegramLoginWidget(fields, "999:other", time.Hour, now); !errors.Is(err, ErrTelegramAuth) {
		t.Fatalf("wrong-token err = %v, want ErrTelegramAuth", err)
	}
}

func TestVerifyTelegramInitData(t *testing.T) {
	const token = "123456:bot-token"
	now := time.Unix(1_700_000_100, 0)
	initData := signInitData(token, map[string]string{
		"user":      `{"id":987654321,"username":"alice","first_name":"A"}`,
		"auth_date": "1700000050",
	})

	user, err := VerifyTelegramInitData(initData, token, time.Hour, now)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if user.ID != 987654321 || user.Username != "alice" {
		t.Fatalf("user = %+v", user)
	}
}

func TestVerifyTelegramInitDataRejectsTamperAndStale(t *testing.T) {
	const token = "123456:bot-token"
	now := time.Unix(1_700_000_100, 0)
	good := signInitData(token, map[string]string{
		"user":      `{"id":1,"username":"a"}`,
		"auth_date": "1700000050",
	})

	// Wrong bot token -> hash mismatch.
	if _, err := VerifyTelegramInitData(good, "other:token", time.Hour, now); !errors.Is(err, ErrTelegramAuth) {
		t.Fatalf("wrong token err = %v", err)
	}

	// Tampered user field invalidates the hash.
	tampered := strings.Replace(good, "username", "usern…me", 1)
	if _, err := VerifyTelegramInitData(tampered, token, time.Hour, now); !errors.Is(err, ErrTelegramAuth) {
		t.Fatalf("tampered err = %v", err)
	}

	// Stale auth_date.
	stale := signInitData(token, map[string]string{
		"user":      `{"id":1,"username":"a"}`,
		"auth_date": "1700000000",
	})
	if _, err := VerifyTelegramInitData(stale, token, 30*time.Second, now); !errors.Is(err, ErrTelegramAuth) {
		t.Fatalf("stale err = %v", err)
	}
}

// guard against accidental signature drift.
func TestHmacSumMatchesStdlib(t *testing.T) {
	mac := hmac.New(sha256.New, []byte("k"))
	mac.Write([]byte("m"))
	if hex.EncodeToString(hmacSum([]byte("k"), []byte("m"))) != hex.EncodeToString(mac.Sum(nil)) {
		t.Fatal("hmacSum diverged from stdlib")
	}
}
