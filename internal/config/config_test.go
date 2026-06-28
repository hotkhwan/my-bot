package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadFromLookupUsesSafeDefaults(t *testing.T) {
	cfg, err := LoadFromLookup(testLookup(nil))
	if err != nil {
		t.Fatalf("LoadFromLookup returned error: %v", err)
	}

	if cfg.App.DryRun != true {
		t.Fatalf("DryRun = %v, want true", cfg.App.DryRun)
	}
	if cfg.App.RealTradingEnabled != false {
		t.Fatalf("RealTradingEnabled = %v, want false", cfg.App.RealTradingEnabled)
	}
	if cfg.App.OrderSizingMode != OrderSizingExplicit {
		t.Fatalf("OrderSizingMode = %q, want %q", cfg.App.OrderSizingMode, OrderSizingExplicit)
	}
	if cfg.App.DefaultMarginMode != MarginModeIsolated {
		t.Fatalf("DefaultMarginMode = %q, want %q", cfg.App.DefaultMarginMode, MarginModeIsolated)
	}
	if cfg.App.ConfirmationTTL != 300*time.Second {
		t.Fatalf("ConfirmationTTL = %s, want 5m0s", cfg.App.ConfirmationTTL)
	}
	if cfg.Telegram.Mode != TelegramModePolling {
		t.Fatalf("Telegram mode = %q, want polling", cfg.Telegram.Mode)
	}
	if cfg.Telegram.AdminUserID != 12345 {
		t.Fatalf("AdminUserID = %d, want 12345", cfg.Telegram.AdminUserID)
	}
	if got, want := cfg.Telegram.AllowedUserIDs, []int64{12345, 67890}; !sameInt64s(got, want) {
		t.Fatalf("AllowedUserIDs = %v, want %v", got, want)
	}
	if cfg.Binance.Testnet != true {
		t.Fatalf("Binance Testnet = %v, want true", cfg.Binance.Testnet)
	}
}

func TestAnthropicBaseURLDefaultsToAnthropic(t *testing.T) {
	// AI_PROVIDER=anthropic without AI_BASE_URL must NOT keep the OpenAI default
	// (that makes requests hit api.openai.com/v1/messages → 404).
	cfg, err := LoadFromLookup(testLookup(map[string]string{
		"AI_PROVIDER": "anthropic", "AI_API_KEY": "sk-ant-x", "AI_MODEL": "claude-opus-4-8",
	}))
	if err != nil {
		t.Fatalf("LoadFromLookup: %v", err)
	}
	if cfg.AI.BaseURL != "https://api.anthropic.com/v1" {
		t.Fatalf("anthropic BaseURL = %q, want https://api.anthropic.com/v1", cfg.AI.BaseURL)
	}

	// An explicit AI_BASE_URL (e.g. a proxy) is still respected.
	cfg2, err := LoadFromLookup(testLookup(map[string]string{
		"AI_PROVIDER": "anthropic", "AI_API_KEY": "sk-ant-x", "AI_MODEL": "claude-opus-4-8",
		"AI_BASE_URL": "https://proxy.example/v1",
	}))
	if err != nil {
		t.Fatalf("LoadFromLookup: %v", err)
	}
	if cfg2.AI.BaseURL != "https://proxy.example/v1" {
		t.Fatalf("explicit BaseURL = %q, want the proxy", cfg2.AI.BaseURL)
	}
}

func TestFuturesBaseURLDefaultsByMode(t *testing.T) {
	// Testnet (default) → testnet host, so live orders don't error on a missing URL.
	cfg, err := LoadFromLookup(testLookup(nil))
	if err != nil {
		t.Fatalf("LoadFromLookup: %v", err)
	}
	if cfg.Binance.FuturesBaseURL != "https://demo-fapi.binance.com" {
		t.Fatalf("testnet FuturesBaseURL = %q, want demo host", cfg.Binance.FuturesBaseURL)
	}
	// An explicit override is respected.
	cfg2, _ := LoadFromLookup(testLookup(map[string]string{"BINANCE_FUTURES_BASE_URL": "https://x.example"}))
	if cfg2.Binance.FuturesBaseURL != "https://x.example" {
		t.Fatalf("override FuturesBaseURL = %q", cfg2.Binance.FuturesBaseURL)
	}
}

func TestLoadFromFileReadsDotEnv(t *testing.T) {
	envPath := writeTempEnv(t, `
APP_ENV=dev
TELEGRAM_BOT_TOKEN=file-token
TELEGRAM_ALLOWED_USER_IDS=111,222
MONGODB_URI=mongodb+srv://mongo.example.invalid/tradebot
MONGODB_DATABASE=tradebot
`)

	cfg, err := LoadFromFile(envPath, emptyLookup)
	if err != nil {
		t.Fatalf("LoadFromFile returned error: %v", err)
	}

	if cfg.App.Env != "dev" {
		t.Fatalf("App Env = %q, want dev", cfg.App.Env)
	}
	if got, want := cfg.Telegram.AllowedUserIDs, []int64{111, 222}; !sameInt64s(got, want) {
		t.Fatalf("AllowedUserIDs = %v, want %v", got, want)
	}
}

func TestLoadFromFileLetsProcessEnvOverrideDotEnv(t *testing.T) {
	envPath := writeTempEnv(t, `
LOG_LEVEL=debug
TELEGRAM_BOT_TOKEN=file-token
TELEGRAM_ALLOWED_USER_IDS=111
MONGODB_URI=mongodb+srv://mongo.example.invalid/tradebot
MONGODB_DATABASE=tradebot
`)

	cfg, err := LoadFromFile(envPath, func(key string) (string, bool) {
		if key == "LOG_LEVEL" {
			return "warn", true
		}
		return "", false
	})
	if err != nil {
		t.Fatalf("LoadFromFile returned error: %v", err)
	}

	if cfg.App.LogLevel != LogLevelWarn {
		t.Fatalf("LogLevel = %q, want %q", cfg.App.LogLevel, LogLevelWarn)
	}
}

func TestLoadFromFileAllowsMissingDotEnv(t *testing.T) {
	_, err := LoadFromFile(filepath.Join(t.TempDir(), ".env"), testLookup(nil))
	if err != nil {
		t.Fatalf("LoadFromFile returned error: %v", err)
	}
}

func TestLoadFromFileRejectsMalformedDotEnv(t *testing.T) {
	envPath := writeTempEnv(t, "not-a-key-value-line")

	_, err := LoadFromFile(envPath, emptyLookup)
	if err == nil {
		t.Fatal("LoadFromFile returned nil error, want parse error")
	}
	assertErrorContains(t, err, "line 1 must be KEY=VALUE")
}

func TestFirstExistingPathReturnsFirstExistingFile(t *testing.T) {
	dir := t.TempDir()
	first := filepath.Join(dir, "missing.env")
	second := filepath.Join(dir, ".env")
	if err := os.WriteFile(second, []byte("APP_ENV=test\n"), 0600); err != nil {
		t.Fatalf("write env file: %v", err)
	}

	got := firstExistingPath([]string{first, second})
	if got != second {
		t.Fatalf("firstExistingPath = %q, want %q", got, second)
	}
}

func TestFirstExistingPathFallsBackToDotEnv(t *testing.T) {
	got := firstExistingPath([]string{filepath.Join(t.TempDir(), "missing.env")})
	if got != ".env" {
		t.Fatalf("firstExistingPath = %q, want .env", got)
	}
}

func TestLoadFromLookupFallsBackForBlankStrings(t *testing.T) {
	cfg, err := LoadFromLookup(testLookup(map[string]string{
		"APP_ENV":   "   ",
		"HTTP_ADDR": "   ",
	}))
	if err != nil {
		t.Fatalf("LoadFromLookup returned error: %v", err)
	}

	if cfg.App.Env != "local" {
		t.Fatalf("App Env = %q, want local", cfg.App.Env)
	}
	if cfg.HTTP.Addr != ":8080" {
		t.Fatalf("HTTP Addr = %q, want :8080", cfg.HTTP.Addr)
	}
}

func TestLoadFromLookupUsesExplicitAdminUserID(t *testing.T) {
	cfg, err := LoadFromLookup(testLookup(map[string]string{
		"TELEGRAM_ADMIN_USER_ID": "777",
	}))
	if err != nil {
		t.Fatalf("LoadFromLookup returned error: %v", err)
	}

	if cfg.Telegram.AdminUserID != 777 {
		t.Fatalf("AdminUserID = %d, want 777", cfg.Telegram.AdminUserID)
	}
}

func TestLoadFromLookupSupportsLegacySingularAllowedUserIDAsAdmin(t *testing.T) {
	cfg, err := LoadFromLookup(testLookup(map[string]string{
		"TELEGRAM_ADMIN_USER_ID":    "",
		"TELEGRAM_ALLOWED_USER_IDS": "",
		"TELEGRAM_ALLOWED_USER_ID":  "555",
	}))
	if err != nil {
		t.Fatalf("LoadFromLookup returned error: %v", err)
	}

	if cfg.Telegram.AdminUserID != 555 {
		t.Fatalf("AdminUserID = %d, want 555", cfg.Telegram.AdminUserID)
	}
}

func TestLoadFromLookupRejectsInvalidAdminUserID(t *testing.T) {
	_, err := LoadFromLookup(testLookup(map[string]string{
		"TELEGRAM_ADMIN_USER_ID":    "abc",
		"TELEGRAM_ALLOWED_USER_IDS": "",
	}))
	if err == nil {
		t.Fatal("LoadFromLookup returned nil error, want validation error")
	}

	assertErrorContains(t, err, "TELEGRAM_ADMIN_USER_ID must be a positive Telegram user id")
	assertErrorContains(t, err, "TELEGRAM_ADMIN_USER_ID or TELEGRAM_ALLOWED_USER_IDS must contain at least one valid user id")
}

func TestLoadFromLookupRequiresAllowedUsers(t *testing.T) {
	_, err := LoadFromLookup(testLookup(map[string]string{
		"TELEGRAM_ALLOWED_USER_IDS": "",
	}))
	if err == nil {
		t.Fatal("LoadFromLookup returned nil error, want validation error")
	}
	assertErrorContains(t, err, "TELEGRAM_ADMIN_USER_ID or TELEGRAM_ALLOWED_USER_IDS must contain at least one valid user id")
	assertErrorDoesNotContain(t, err, "TELEGRAM_ALLOWED_USER_IDS is required")
}

func TestLoadFromLookupProtectsRealTrading(t *testing.T) {
	_, err := LoadFromLookup(testLookup(map[string]string{
		"REAL_TRADING_ENABLED": "true",
	}))
	if err == nil {
		t.Fatal("LoadFromLookup returned nil error, want validation error")
	}

	assertErrorContains(t, err, "REAL_TRADING_ENABLED=true requires DRY_RUN=false")
	assertErrorContains(t, err, "REAL_TRADING_ENABLED=true requires BINANCE_TESTNET=false")
	assertErrorContains(t, err, "REAL_TRADING_ENABLED=true requires BINANCE_API_KEY and BINANCE_API_SECRET")
}

func TestLoadFromLookupRejectsRealTradingWhenOnlyTestnetGateFails(t *testing.T) {
	_, err := LoadFromLookup(testLookup(map[string]string{
		"DRY_RUN":              "false",
		"REAL_TRADING_ENABLED": "true",
		"BINANCE_TESTNET":      "true",
		"BINANCE_API_KEY":      "key",
		"BINANCE_API_SECRET":   "secret",
	}))
	if err == nil {
		t.Fatal("LoadFromLookup returned nil error, want validation error")
	}

	assertErrorContains(t, err, "REAL_TRADING_ENABLED=true requires BINANCE_TESTNET=false")
	assertErrorDoesNotContain(t, err, "REAL_TRADING_ENABLED=true requires DRY_RUN=false")
	assertErrorDoesNotContain(t, err, "REAL_TRADING_ENABLED=true requires BINANCE_API_KEY and BINANCE_API_SECRET")
	assertErrorDoesNotContain(t, err, "BINANCE_API_KEY and BINANCE_API_SECRET are required when DRY_RUN=false")
}

func TestLoadFromLookupRequiresBinanceCredentialsWhenDryRunDisabled(t *testing.T) {
	_, err := LoadFromLookup(testLookup(map[string]string{
		"DRY_RUN": "false",
	}))
	if err == nil {
		t.Fatal("LoadFromLookup returned nil error, want validation error")
	}

	assertErrorContains(t, err, "BINANCE_API_KEY and BINANCE_API_SECRET are required when DRY_RUN=false")
}

func TestLoadFromLookupAllowsConfiguredRealTradingOnlyWhenExplicitlyUnsafeFlagsAreOff(t *testing.T) {
	cfg, err := LoadFromLookup(testLookup(map[string]string{
		"DRY_RUN":              "false",
		"REAL_TRADING_ENABLED": "true",
		"BINANCE_TESTNET":      "false",
		"BINANCE_API_KEY":      "key",
		"BINANCE_API_SECRET":   "secret",
	}))
	if err != nil {
		t.Fatalf("LoadFromLookup returned error: %v", err)
	}

	if !cfg.App.RealTradingEnabled {
		t.Fatal("RealTradingEnabled = false, want true")
	}
	if cfg.App.DryRun {
		t.Fatal("DryRun = true, want false")
	}
	if cfg.Binance.Testnet {
		t.Fatal("Binance Testnet = true, want false")
	}
}

func TestLoadFromLookupRequiresWebhookURLWhenWebhookMode(t *testing.T) {
	_, err := LoadFromLookup(testLookup(map[string]string{
		"TELEGRAM_MODE": "webhook",
	}))
	if err == nil {
		t.Fatal("LoadFromLookup returned nil error, want validation error")
	}
	assertErrorContains(t, err, "PUBLIC_WEBHOOK_URL must be a valid http or https URL when TELEGRAM_MODE=webhook")

	_, err = LoadFromLookup(testLookup(map[string]string{
		"TELEGRAM_MODE":             "webhook",
		"PUBLIC_WEBHOOK_URL":        "https://tradebot.example.com/telegram/webhook",
		"TELEGRAM_ALLOWED_USER_IDS": "12345",
		"HTTP_ENABLED":              "true",
	}))
	if err != nil {
		t.Fatalf("LoadFromLookup returned error: %v", err)
	}
}

func TestLoadFromLookupValidatesTradingViewWebhook(t *testing.T) {
	_, err := LoadFromLookup(testLookup(map[string]string{
		"TRADINGVIEW_ENABLED":        "true",
		"TRADINGVIEW_WEBHOOK_SECRET": "secret",
	}))
	if err == nil {
		t.Fatal("LoadFromLookup returned nil error, want validation error")
	}
	assertErrorContains(t, err, "HTTP_ENABLED=true is required when TRADINGVIEW_ENABLED=true")

	cfg, err := LoadFromLookup(testLookup(map[string]string{
		"HTTP_ENABLED":               "true",
		"TRADINGVIEW_WEBHOOK_SECRET": "secret",
	}))
	if err != nil {
		t.Fatalf("LoadFromLookup returned error: %v", err)
	}
	if !cfg.TradingView.Enabled {
		t.Fatal("TradingView.Enabled = false, want true when secret is set")
	}
}

func TestLoadFromLookupValidatesAIAdvisor(t *testing.T) {
	_, err := LoadFromLookup(testLookup(map[string]string{
		"AI_ENABLED":  "true",
		"AI_PROVIDER": "openai_compatible",
	}))
	if err == nil {
		t.Fatal("LoadFromLookup returned nil error, want validation error")
	}
	assertErrorContains(t, err, "AI_API_KEY is required")
	assertErrorContains(t, err, "AI_MODEL is required")

	cfg, err := LoadFromLookup(testLookup(map[string]string{
		"AI_PROVIDER": "openai_compatible",
		"AI_API_KEY":  "key",
		"AI_MODEL":    "model",
	}))
	if err != nil {
		t.Fatalf("LoadFromLookup returned error: %v", err)
	}
	if !cfg.AI.Enabled {
		t.Fatal("AI.Enabled = false, want true when provider/key/model are set")
	}
}

func TestLoadFromLookupRejectsFutureSizingAndCrossMarginInPhaseOne(t *testing.T) {
	_, err := LoadFromLookup(testLookup(map[string]string{
		"ORDER_SIZING_MODE":   "risk_percent",
		"DEFAULT_MARGIN_MODE": "cross",
	}))
	if err == nil {
		t.Fatal("LoadFromLookup returned nil error, want validation error")
	}

	assertErrorContains(t, err, "ORDER_SIZING_MODE must be explicit in Phase 1")
	assertErrorContains(t, err, "DEFAULT_MARGIN_MODE must be isolated in Phase 1")
}

func TestLoadFromLookupRejectsInvalidMaxLeverage(t *testing.T) {
	tests := []struct {
		name     string
		leverage string
	}{
		{name: "too low", leverage: "0"},
		{name: "too high", leverage: "126"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := LoadFromLookup(testLookup(map[string]string{
				"MAX_LEVERAGE": tt.leverage,
			}))
			if err == nil {
				t.Fatal("LoadFromLookup returned nil error, want validation error")
			}

			assertErrorContains(t, err, "MAX_LEVERAGE must be between 1 and 125")
		})
	}
}

func TestLoadFromLookupRejectsInvalidConfirmationTTL(t *testing.T) {
	_, err := LoadFromLookup(testLookup(map[string]string{
		"CONFIRMATION_TTL_SECONDS": "0",
	}))
	if err == nil {
		t.Fatal("LoadFromLookup returned nil error, want validation error")
	}

	assertErrorContains(t, err, "CONFIRMATION_TTL_SECONDS must be greater than 0")
}

func TestLoadFromLookupRejectsInvalidMongoURI(t *testing.T) {
	_, err := LoadFromLookup(testLookup(map[string]string{
		"MONGODB_URI": "redis://cache.example.invalid/0",
	}))
	if err == nil {
		t.Fatal("LoadFromLookup returned nil error, want validation error")
	}

	assertErrorContains(t, err, "MONGODB_URI must start with mongodb:// or mongodb+srv:// and include a host")
}

func TestLoadFromLookupRejectsInvalidBoolAndIntValues(t *testing.T) {
	_, err := LoadFromLookup(testLookup(map[string]string{
		"DRY_RUN":      "maybe",
		"MAX_LEVERAGE": "abc",
	}))
	if err == nil {
		t.Fatal("LoadFromLookup returned nil error, want validation error")
	}

	assertErrorContains(t, err, "DRY_RUN must be a boolean")
	assertErrorContains(t, err, "MAX_LEVERAGE must be an integer")
}

func TestLoadFromLookupRejectsInvalidPollingBackoff(t *testing.T) {
	_, err := LoadFromLookup(testLookup(map[string]string{
		"TELEGRAM_POLLING_BACKOFF_MIN_SECONDS": "30",
		"TELEGRAM_POLLING_BACKOFF_MAX_SECONDS": "1",
	}))
	if err == nil {
		t.Fatal("LoadFromLookup returned nil error, want validation error")
	}

	assertErrorContains(t, err, "TELEGRAM_POLLING_BACKOFF_MIN_SECONDS must be less than or equal to TELEGRAM_POLLING_BACKOFF_MAX_SECONDS")
}

func TestLoadFromLookupValidatesOptionalS3AsACompleteBlock(t *testing.T) {
	_, err := LoadFromLookup(testLookup(map[string]string{
		"S3_BUCKET": "reports",
	}))
	if err == nil {
		t.Fatal("LoadFromLookup returned nil error, want validation error")
	}

	assertErrorContains(t, err, "S3_REGION is required")
	assertErrorContains(t, err, "S3_ACCESS_KEY_ID is required")
	assertErrorContains(t, err, "S3_SECRET_ACCESS_KEY is required")
}

func TestLoadFromLookupValidatesOptionalStripeAsACompleteBlock(t *testing.T) {
	_, err := LoadFromLookup(testLookup(map[string]string{
		"STRIPE_PRICE_ID": "price_test",
	}))
	if err == nil {
		t.Fatal("LoadFromLookup returned nil error, want validation error")
	}

	assertErrorContains(t, err, "STRIPE_SECRET_KEY is required")
	assertErrorContains(t, err, "STRIPE_WEBHOOK_SECRET is required")
}

func testLookup(overrides map[string]string) LookupFunc {
	values := map[string]string{
		"TELEGRAM_BOT_TOKEN":        "123:abc",
		"TELEGRAM_ALLOWED_USER_IDS": "12345,67890,12345",
		"MONGODB_URI":               "mongodb+srv://mongo.example.invalid/tradebot",
		"MONGODB_DATABASE":          "tradebot",
	}

	for key, value := range overrides {
		values[key] = value
	}

	return func(key string) (string, bool) {
		value, ok := values[key]
		return value, ok
	}
}

func emptyLookup(string) (string, bool) {
	return "", false
}

func writeTempEnv(t *testing.T, content string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(path, []byte(strings.TrimSpace(content)+"\n"), 0600); err != nil {
		t.Fatalf("write temp env: %v", err)
	}

	return path
}

func assertErrorContains(t *testing.T, err error, want string) {
	t.Helper()
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("error = %q, want it to contain %q", err.Error(), want)
	}
}

func assertErrorDoesNotContain(t *testing.T, err error, unwanted string) {
	t.Helper()
	if strings.Contains(err.Error(), unwanted) {
		t.Fatalf("error = %q, want it not to contain %q", err.Error(), unwanted)
	}
}

func sameInt64s(a []int64, b []int64) bool {
	if len(a) != len(b) {
		return false
	}

	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}

	return true
}
