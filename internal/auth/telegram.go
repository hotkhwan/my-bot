package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ErrTelegramAuth means the Telegram init data failed verification.
var ErrTelegramAuth = errors.New("auth: invalid telegram init data")

// TelegramUser is the user object inside Telegram Mini App init data.
type TelegramUser struct {
	ID        int64  `json:"id"`
	Username  string `json:"username"`
	FirstName string `json:"first_name"`
}

// VerifyTelegramInitData validates a Telegram Mini App `initData` string against
// the bot token (the WebAppData HMAC scheme) and returns the authenticated user.
// maxAge, when > 0, rejects init data older than that.
func VerifyTelegramInitData(initData, botToken string, maxAge time.Duration, now time.Time) (TelegramUser, error) {
	if strings.TrimSpace(botToken) == "" {
		return TelegramUser{}, errors.New("auth: telegram bot token is required")
	}
	values, err := url.ParseQuery(initData)
	if err != nil {
		return TelegramUser{}, ErrTelegramAuth
	}
	hash := values.Get("hash")
	if hash == "" {
		return TelegramUser{}, ErrTelegramAuth
	}

	// data_check_string: every field except hash, sorted by key, "k=v" joined \n.
	keys := make([]string, 0, len(values))
	for key := range values {
		if key == "hash" {
			continue
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	pairs := make([]string, 0, len(keys))
	for _, key := range keys {
		pairs = append(pairs, key+"="+values.Get(key))
	}
	dataCheck := strings.Join(pairs, "\n")

	secret := hmacSum([]byte("WebAppData"), []byte(botToken))
	computed := hex.EncodeToString(hmacSum(secret, []byte(dataCheck)))
	if subtle.ConstantTimeCompare([]byte(computed), []byte(hash)) != 1 {
		return TelegramUser{}, ErrTelegramAuth
	}

	if maxAge > 0 {
		authDate, _ := strconv.ParseInt(values.Get("auth_date"), 10, 64)
		if authDate == 0 || now.Sub(time.Unix(authDate, 0)) > maxAge {
			return TelegramUser{}, fmt.Errorf("%w: stale auth_date", ErrTelegramAuth)
		}
	}

	var user TelegramUser
	if err := json.Unmarshal([]byte(values.Get("user")), &user); err != nil || user.ID == 0 {
		return TelegramUser{}, ErrTelegramAuth
	}
	return user, nil
}

func hmacSum(key, msg []byte) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write(msg)
	return mac.Sum(nil)
}
