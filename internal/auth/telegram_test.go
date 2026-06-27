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
