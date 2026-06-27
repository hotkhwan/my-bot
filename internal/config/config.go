package config

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"bottrade/internal/auth"
)

const (
	LogLevelDebug = "debug"
	LogLevelInfo  = "info"
	LogLevelWarn  = "warn"
	LogLevelError = "error"

	TelegramModePolling = "polling"
	TelegramModeWebhook = "webhook"

	OrderSizingExplicit = "explicit"

	MarginModeIsolated = "isolated"
)

type LookupFunc func(string) (string, bool)

type Config struct {
	App         AppConfig
	Telegram    TelegramConfig
	HTTP        HTTPConfig
	Binance     BinanceConfig
	TradingView TradingViewConfig
	AI          AIConfig
	MongoDB     MongoDBConfig
	Stripe      StripeConfig
	S3          S3Config
	Auth        AuthConfig
}

type AppConfig struct {
	Env                string
	LogLevel           string
	DryRun             bool
	RealTradingEnabled bool
	OrderSizingMode    string
	DefaultMarginMode  string
	MaxLeverage        int
	ConfirmationTTL    time.Duration
	// Trailing stop policy, as percent-of-entry strings (e.g. "1" = 1%). Both
	// must be positive to enable the trailing-stop monitor.
	TrailActivatePct string
	TrailGapPct      string
}

type TelegramConfig struct {
	BotToken          string
	AdminUserID       int64
	AllowedUserIDs    []int64
	Mode              string
	PublicWebhookURL  string
	PollingBackoffMin time.Duration
	PollingBackoffMax time.Duration
}

type HTTPConfig struct {
	Addr    string
	Enabled bool
	// StatusToken gates the detailed /status endpoint. Empty disables /status
	// entirely so trading config is never exposed unauthenticated.
	StatusToken string
}

type BinanceConfig struct {
	APIKey               string
	APISecret            string
	Testnet              bool
	FuturesBaseURL       string
	RequestTimeout       time.Duration
	ExchangeInfoCacheTTL time.Duration
}

type MongoDBConfig struct {
	URI      string
	Database string
}

type TradingViewConfig struct {
	Enabled       bool
	WebhookSecret string
}

type AIConfig struct {
	Enabled              bool
	Provider             string
	APIKey               string
	BaseURL              string
	Model                string
	SystemPrompt         string
	RequestTimeout       time.Duration
	MinConfidencePercent int
	AutoTradeEnabled     bool
	// Ensemble (multi-AI panel). When Providers is non-empty the signal
	// processor uses the panel instead of the single Provider above.
	Providers        []AIProvider
	EnsemblePolicy   string // "majority" | "consensus"
	EnsembleMinVotes int
	// Market-data enrichment: free Binance Futures order-flow (funding, open
	// interest, long/short ratio, taker flow) injected into the AI prompt. Uses
	// the production market-data host even on testnet (public, read-only).
	MarketDataEnabled bool
	MarketDataBaseURL string
	MarketDataPeriod  string // sampling window for ratio endpoints, e.g. "5m"
	KlineInterval     string // candle interval for EMA/RSI/MACD, e.g. "1h"
	// Crypto Fear & Greed Index sentiment (free, key-less, market-wide).
	FearGreedEnabled bool
	FearGreedBaseURL string
}

// AIProvider is one member of the AI ensemble, parsed from AI_PROVIDERS (JSON).
type AIProvider struct {
	Name     string `json:"name"`
	Provider string `json:"provider"` // "anthropic" | "openai_compatible"
	APIKey   string `json:"api_key"`
	BaseURL  string `json:"base_url"`
	Model    string `json:"model"`
}

type StripeConfig struct {
	SecretKey     string
	WebhookSecret string
	PriceID       string
	Enabled       bool
}

type S3Config struct {
	Endpoint        string
	Region          string
	Bucket          string
	AccessKeyID     string
	SecretAccessKey string
	ForcePathStyle  bool
	Enabled         bool
}

// AuthConfig holds the master key used to encrypt per-user Binance credentials
// at rest. Enabled is true once a key is provided; multi-tenant credential
// storage stays off until then so the single-user flow is unaffected.
type AuthConfig struct {
	EncryptionKeyID string
	EncryptionKey   []byte
	Enabled         bool
}

type ValidationError struct {
	Problems []string
}

func (e ValidationError) Error() string {
	return "config validation failed: " + strings.Join(e.Problems, "; ")
}

func Load() (Config, error) {
	return LoadFromFile(discoverEnvFile(), os.LookupEnv)
}

func LoadFromFile(path string, lookup LookupFunc) (Config, error) {
	if lookup == nil {
		return Config{}, fmt.Errorf("config lookup function is required")
	}

	values, err := readEnvFile(path)
	if err != nil {
		return Config{}, err
	}

	return LoadFromLookup(overlayLookup(values, lookup))
}

func LoadFromLookup(lookup LookupFunc) (Config, error) {
	if lookup == nil {
		return Config{}, fmt.Errorf("config lookup function is required")
	}

	cfg := defaultConfig()
	reader := envReader{lookup: lookup}

	cfg.App.Env = reader.string("APP_ENV", cfg.App.Env)
	cfg.App.LogLevel = strings.ToLower(reader.string("LOG_LEVEL", cfg.App.LogLevel))
	cfg.App.DryRun = reader.bool("DRY_RUN", cfg.App.DryRun)
	cfg.App.RealTradingEnabled = reader.bool("REAL_TRADING_ENABLED", cfg.App.RealTradingEnabled)
	cfg.App.OrderSizingMode = strings.ToLower(reader.string("ORDER_SIZING_MODE", cfg.App.OrderSizingMode))
	cfg.App.DefaultMarginMode = strings.ToLower(reader.string("DEFAULT_MARGIN_MODE", cfg.App.DefaultMarginMode))
	cfg.App.MaxLeverage = reader.int("MAX_LEVERAGE", cfg.App.MaxLeverage)
	cfg.App.ConfirmationTTL = reader.seconds("CONFIRMATION_TTL_SECONDS", cfg.App.ConfirmationTTL)
	cfg.App.TrailActivatePct = reader.string("TRAIL_ACTIVATE_PCT", cfg.App.TrailActivatePct)
	cfg.App.TrailGapPct = reader.string("TRAIL_GAP_PCT", cfg.App.TrailGapPct)

	cfg.Telegram.BotToken = reader.string("TELEGRAM_BOT_TOKEN", cfg.Telegram.BotToken)
	cfg.Telegram.AdminUserID = reader.userID("TELEGRAM_ADMIN_USER_ID")
	if cfg.Telegram.AdminUserID == 0 {
		cfg.Telegram.AdminUserID = reader.userID("TELEGRAM_ALLOWED_USER_ID")
	}
	cfg.Telegram.AllowedUserIDs = reader.userIDs("TELEGRAM_ALLOWED_USER_IDS")
	if cfg.Telegram.AdminUserID == 0 && len(cfg.Telegram.AllowedUserIDs) > 0 {
		cfg.Telegram.AdminUserID = cfg.Telegram.AllowedUserIDs[0]
	}
	cfg.Telegram.Mode = strings.ToLower(reader.string("TELEGRAM_MODE", cfg.Telegram.Mode))
	cfg.Telegram.PublicWebhookURL = reader.string("PUBLIC_WEBHOOK_URL", cfg.Telegram.PublicWebhookURL)
	cfg.Telegram.PollingBackoffMin = reader.seconds("TELEGRAM_POLLING_BACKOFF_MIN_SECONDS", cfg.Telegram.PollingBackoffMin)
	cfg.Telegram.PollingBackoffMax = reader.seconds("TELEGRAM_POLLING_BACKOFF_MAX_SECONDS", cfg.Telegram.PollingBackoffMax)

	cfg.HTTP.Addr = reader.string("HTTP_ADDR", cfg.HTTP.Addr)
	cfg.HTTP.Enabled = reader.bool("HTTP_ENABLED", cfg.HTTP.Enabled)
	cfg.HTTP.StatusToken = reader.string("HTTP_STATUS_TOKEN", cfg.HTTP.StatusToken)

	cfg.Binance.APIKey = reader.string("BINANCE_API_KEY", cfg.Binance.APIKey)
	cfg.Binance.APISecret = reader.string("BINANCE_API_SECRET", cfg.Binance.APISecret)
	cfg.Binance.Testnet = reader.bool("BINANCE_TESTNET", cfg.Binance.Testnet)
	cfg.Binance.FuturesBaseURL = reader.string("BINANCE_FUTURES_BASE_URL", cfg.Binance.FuturesBaseURL)
	cfg.Binance.RequestTimeout = reader.seconds("BINANCE_REQUEST_TIMEOUT_SECONDS", cfg.Binance.RequestTimeout)
	cfg.Binance.ExchangeInfoCacheTTL = reader.seconds("EXCHANGE_INFO_CACHE_TTL_SECONDS", cfg.Binance.ExchangeInfoCacheTTL)

	cfg.TradingView.WebhookSecret = reader.string("TRADINGVIEW_WEBHOOK_SECRET", cfg.TradingView.WebhookSecret)
	cfg.TradingView.Enabled = reader.bool("TRADINGVIEW_ENABLED", anySet(cfg.TradingView.WebhookSecret))

	cfg.AI.Provider = strings.ToLower(reader.string("AI_PROVIDER", cfg.AI.Provider))
	cfg.AI.APIKey = reader.string("AI_API_KEY", cfg.AI.APIKey)
	cfg.AI.BaseURL = reader.string("AI_BASE_URL", cfg.AI.BaseURL)
	cfg.AI.Model = reader.string("AI_MODEL", cfg.AI.Model)
	cfg.AI.SystemPrompt = reader.string("AI_SYSTEM_PROMPT", cfg.AI.SystemPrompt)
	cfg.AI.RequestTimeout = reader.seconds("AI_REQUEST_TIMEOUT_SECONDS", cfg.AI.RequestTimeout)
	cfg.AI.MinConfidencePercent = reader.int("AI_MIN_CONFIDENCE_PERCENT", cfg.AI.MinConfidencePercent)
	cfg.AI.AutoTradeEnabled = reader.bool("AI_AUTOTRADE_ENABLED", cfg.AI.AutoTradeEnabled)
	cfg.AI.Enabled = reader.bool("AI_ENABLED", cfg.AI.Provider != "disabled" || anySet(cfg.AI.APIKey, cfg.AI.Model))
	cfg.AI.Providers = reader.aiProviders("AI_PROVIDERS")
	cfg.AI.EnsemblePolicy = strings.ToLower(reader.string("AI_ENSEMBLE_POLICY", cfg.AI.EnsemblePolicy))
	cfg.AI.EnsembleMinVotes = reader.int("AI_ENSEMBLE_MIN_VOTES", cfg.AI.EnsembleMinVotes)
	if len(cfg.AI.Providers) > 0 {
		cfg.AI.Enabled = true
	}
	cfg.AI.MarketDataBaseURL = reader.string("MARKETDATA_BASE_URL", cfg.AI.MarketDataBaseURL)
	cfg.AI.MarketDataPeriod = reader.string("MARKETDATA_PERIOD", cfg.AI.MarketDataPeriod)
	cfg.AI.KlineInterval = reader.string("MARKETDATA_KLINE_INTERVAL", cfg.AI.KlineInterval)
	cfg.AI.MarketDataEnabled = reader.bool("MARKETDATA_ENABLED", cfg.AI.Enabled)
	cfg.AI.FearGreedBaseURL = reader.string("FEAR_GREED_BASE_URL", cfg.AI.FearGreedBaseURL)
	cfg.AI.FearGreedEnabled = reader.bool("FEAR_GREED_ENABLED", cfg.AI.MarketDataEnabled)

	cfg.MongoDB.URI = reader.string("MONGODB_URI", cfg.MongoDB.URI)
	cfg.MongoDB.Database = reader.string("MONGODB_DATABASE", cfg.MongoDB.Database)

	cfg.Stripe.SecretKey = reader.string("STRIPE_SECRET_KEY", cfg.Stripe.SecretKey)
	cfg.Stripe.WebhookSecret = reader.string("STRIPE_WEBHOOK_SECRET", cfg.Stripe.WebhookSecret)
	cfg.Stripe.PriceID = reader.string("STRIPE_PRICE_ID", cfg.Stripe.PriceID)
	cfg.Stripe.Enabled = anySet(cfg.Stripe.SecretKey, cfg.Stripe.WebhookSecret, cfg.Stripe.PriceID)

	cfg.S3.Endpoint = reader.string("S3_ENDPOINT", cfg.S3.Endpoint)
	cfg.S3.Region = reader.string("S3_REGION", cfg.S3.Region)
	cfg.S3.Bucket = reader.string("S3_BUCKET", cfg.S3.Bucket)
	cfg.S3.AccessKeyID = reader.string("S3_ACCESS_KEY_ID", cfg.S3.AccessKeyID)
	cfg.S3.SecretAccessKey = reader.string("S3_SECRET_ACCESS_KEY", cfg.S3.SecretAccessKey)
	cfg.S3.ForcePathStyle = reader.bool("S3_FORCE_PATH_STYLE", cfg.S3.ForcePathStyle)
	cfg.S3.Enabled = anySet(cfg.S3.Endpoint, cfg.S3.Region, cfg.S3.Bucket, cfg.S3.AccessKeyID, cfg.S3.SecretAccessKey)

	cfg.Auth.EncryptionKeyID = reader.string("CREDENTIAL_ENCRYPTION_KEY_ID", cfg.Auth.EncryptionKeyID)
	cfg.Auth.EncryptionKey = reader.base64("CREDENTIAL_ENCRYPTION_KEY")
	cfg.Auth.Enabled = len(cfg.Auth.EncryptionKey) > 0

	validate(cfg, &reader.problems)

	if len(reader.problems) > 0 {
		return cfg, ValidationError{Problems: append([]string(nil), reader.problems...)}
	}

	return cfg, nil
}

func defaultConfig() Config {
	return Config{
		App: AppConfig{
			Env:                "local",
			LogLevel:           LogLevelInfo,
			DryRun:             true,
			RealTradingEnabled: false,
			OrderSizingMode:    OrderSizingExplicit,
			DefaultMarginMode:  MarginModeIsolated,
			MaxLeverage:        20,
			ConfirmationTTL:    300 * time.Second,
		},
		Telegram: TelegramConfig{
			Mode:              TelegramModePolling,
			PollingBackoffMin: time.Second,
			PollingBackoffMax: 30 * time.Second,
		},
		HTTP: HTTPConfig{
			Addr: ":8080",
		},
		Binance: BinanceConfig{
			Testnet:              true,
			RequestTimeout:       10 * time.Second,
			ExchangeInfoCacheTTL: 900 * time.Second,
		},
		MongoDB: MongoDBConfig{
			Database: "tradebot",
		},
		AI: AIConfig{
			Provider:             "disabled",
			BaseURL:              "https://api.openai.com/v1",
			RequestTimeout:       20 * time.Second,
			MinConfidencePercent: 70,
			MarketDataBaseURL:    "https://fapi.binance.com",
			MarketDataPeriod:     "5m",
			KlineInterval:        "1h",
			FearGreedBaseURL:     "https://api.alternative.me",
		},
		Auth: AuthConfig{
			EncryptionKeyID: "v1",
		},
	}
}

func validate(cfg Config, problems *[]string) {
	requireNonEmpty(problems, "TELEGRAM_BOT_TOKEN", cfg.Telegram.BotToken)
	requireNonEmpty(problems, "MONGODB_URI", cfg.MongoDB.URI)
	requireNonEmpty(problems, "MONGODB_DATABASE", cfg.MongoDB.Database)
	requireNonEmpty(problems, "HTTP_ADDR", cfg.HTTP.Addr)

	if cfg.Telegram.AdminUserID == 0 && len(cfg.Telegram.AllowedUserIDs) == 0 {
		addProblem(problems, "TELEGRAM_ADMIN_USER_ID or TELEGRAM_ALLOWED_USER_IDS must contain at least one valid user id")
	}

	if !oneOf(cfg.App.LogLevel, LogLevelDebug, LogLevelInfo, LogLevelWarn, LogLevelError) {
		addProblem(problems, "LOG_LEVEL must be one of debug, info, warn, error")
	}

	if cfg.App.OrderSizingMode != OrderSizingExplicit {
		addProblem(problems, "ORDER_SIZING_MODE must be explicit in Phase 1")
	}

	if cfg.App.DefaultMarginMode != MarginModeIsolated {
		addProblem(problems, "DEFAULT_MARGIN_MODE must be isolated in Phase 1")
	}

	if cfg.App.MaxLeverage < 1 || cfg.App.MaxLeverage > 125 {
		addProblem(problems, "MAX_LEVERAGE must be between 1 and 125")
	}

	if cfg.App.ConfirmationTTL <= 0 {
		addProblem(problems, "CONFIRMATION_TTL_SECONDS must be greater than 0")
	}

	if !oneOf(cfg.Telegram.Mode, TelegramModePolling, TelegramModeWebhook) {
		addProblem(problems, "TELEGRAM_MODE must be polling or webhook")
	}

	if cfg.Telegram.Mode == TelegramModeWebhook && !validHTTPURL(cfg.Telegram.PublicWebhookURL) {
		addProblem(problems, "PUBLIC_WEBHOOK_URL must be a valid http or https URL when TELEGRAM_MODE=webhook")
	}
	if cfg.Telegram.Mode == TelegramModeWebhook {
		if !cfg.HTTP.Enabled {
			addProblem(problems, "HTTP_ENABLED=true is required when TELEGRAM_MODE=webhook")
		}
	}

	if cfg.Telegram.PollingBackoffMin <= 0 {
		addProblem(problems, "TELEGRAM_POLLING_BACKOFF_MIN_SECONDS must be greater than 0")
	}

	if cfg.Telegram.PollingBackoffMax <= 0 {
		addProblem(problems, "TELEGRAM_POLLING_BACKOFF_MAX_SECONDS must be greater than 0")
	}

	if cfg.Telegram.PollingBackoffMin > cfg.Telegram.PollingBackoffMax {
		addProblem(problems, "TELEGRAM_POLLING_BACKOFF_MIN_SECONDS must be less than or equal to TELEGRAM_POLLING_BACKOFF_MAX_SECONDS")
	}

	if cfg.Binance.RequestTimeout <= 0 {
		addProblem(problems, "BINANCE_REQUEST_TIMEOUT_SECONDS must be greater than 0")
	}

	if cfg.Binance.ExchangeInfoCacheTTL <= 0 {
		addProblem(problems, "EXCHANGE_INFO_CACHE_TTL_SECONDS must be greater than 0")
	}

	if !cfg.App.DryRun && (cfg.Binance.APIKey == "" || cfg.Binance.APISecret == "") {
		addProblem(problems, "BINANCE_API_KEY and BINANCE_API_SECRET are required when DRY_RUN=false")
	}

	if cfg.App.RealTradingEnabled {
		if cfg.App.DryRun {
			addProblem(problems, "REAL_TRADING_ENABLED=true requires DRY_RUN=false")
		}
		if cfg.Binance.Testnet {
			addProblem(problems, "REAL_TRADING_ENABLED=true requires BINANCE_TESTNET=false")
		}
		if cfg.Binance.APIKey == "" || cfg.Binance.APISecret == "" {
			addProblem(problems, "REAL_TRADING_ENABLED=true requires BINANCE_API_KEY and BINANCE_API_SECRET")
		}
	}

	if cfg.MongoDB.URI != "" && !validMongoURI(cfg.MongoDB.URI) {
		addProblem(problems, "MONGODB_URI must start with mongodb:// or mongodb+srv:// and include a host")
	}

	if cfg.TradingView.Enabled {
		requireNonEmpty(problems, "TRADINGVIEW_WEBHOOK_SECRET", cfg.TradingView.WebhookSecret)
		if !cfg.HTTP.Enabled {
			addProblem(problems, "HTTP_ENABLED=true is required when TRADINGVIEW_ENABLED=true")
		}
	}

	if cfg.AI.Enabled {
		if len(cfg.AI.Providers) > 0 {
			// Ensemble mode: validate each panel member instead of the single
			// AI_PROVIDER block.
			for i, provider := range cfg.AI.Providers {
				if provider.Provider != "anthropic" && provider.Provider != "openai_compatible" {
					addProblem(problems, fmt.Sprintf("AI_PROVIDERS[%d].provider must be anthropic or openai_compatible", i))
				}
				requireNonEmpty(problems, fmt.Sprintf("AI_PROVIDERS[%d].api_key", i), provider.APIKey)
				requireNonEmpty(problems, fmt.Sprintf("AI_PROVIDERS[%d].model", i), provider.Model)
			}
			if !oneOf(cfg.AI.EnsemblePolicy, "", "majority", "consensus") {
				addProblem(problems, "AI_ENSEMBLE_POLICY must be majority or consensus")
			}
		} else {
			if cfg.AI.Provider != "openai_compatible" && cfg.AI.Provider != "anthropic" {
				addProblem(problems, "AI_PROVIDER must be disabled, openai_compatible, or anthropic")
			}
			requireNonEmpty(problems, "AI_API_KEY", cfg.AI.APIKey)
			requireNonEmpty(problems, "AI_MODEL", cfg.AI.Model)
			if !validHTTPURL(cfg.AI.BaseURL) {
				addProblem(problems, "AI_BASE_URL must be a valid http or https URL")
			}
		}
		if cfg.AI.RequestTimeout <= 0 {
			addProblem(problems, "AI_REQUEST_TIMEOUT_SECONDS must be greater than 0")
		}
		if cfg.AI.MinConfidencePercent < 0 || cfg.AI.MinConfidencePercent > 100 {
			addProblem(problems, "AI_MIN_CONFIDENCE_PERCENT must be between 0 and 100")
		}
	}
	if cfg.AI.AutoTradeEnabled && !cfg.AI.Enabled {
		addProblem(problems, "AI_AUTOTRADE_ENABLED=true requires AI_ENABLED=true")
	}
	if cfg.AI.AutoTradeEnabled && !cfg.App.DryRun && !cfg.Binance.Testnet {
		addProblem(problems, "AI_AUTOTRADE_ENABLED=true requires DRY_RUN=true or BINANCE_TESTNET=true in this phase")
	}

	if cfg.Stripe.Enabled {
		requireNonEmpty(problems, "STRIPE_SECRET_KEY", cfg.Stripe.SecretKey)
		requireNonEmpty(problems, "STRIPE_WEBHOOK_SECRET", cfg.Stripe.WebhookSecret)
		requireNonEmpty(problems, "STRIPE_PRICE_ID", cfg.Stripe.PriceID)
	}

	if cfg.S3.Enabled {
		requireNonEmpty(problems, "S3_REGION", cfg.S3.Region)
		requireNonEmpty(problems, "S3_BUCKET", cfg.S3.Bucket)
		requireNonEmpty(problems, "S3_ACCESS_KEY_ID", cfg.S3.AccessKeyID)
		requireNonEmpty(problems, "S3_SECRET_ACCESS_KEY", cfg.S3.SecretAccessKey)
	}

	if cfg.Auth.Enabled {
		if len(cfg.Auth.EncryptionKey) != auth.KeySize {
			addProblem(problems, fmt.Sprintf("CREDENTIAL_ENCRYPTION_KEY must decode (base64) to exactly %d bytes for AES-256", auth.KeySize))
		}
		requireNonEmpty(problems, "CREDENTIAL_ENCRYPTION_KEY_ID", cfg.Auth.EncryptionKeyID)
	}
}

type envReader struct {
	lookup   LookupFunc
	problems []string
}

func (r *envReader) string(name string, fallback string) string {
	raw, ok := r.lookup(name)
	if !ok || strings.TrimSpace(raw) == "" {
		return fallback
	}

	return strings.TrimSpace(raw)
}

func (r *envReader) bool(name string, fallback bool) bool {
	raw, ok := r.lookup(name)
	if !ok || strings.TrimSpace(raw) == "" {
		return fallback
	}

	parsed, err := strconv.ParseBool(strings.TrimSpace(raw))
	if err != nil {
		r.add("%s must be a boolean", name)
		return fallback
	}

	return parsed
}

func (r *envReader) int(name string, fallback int) int {
	raw, ok := r.lookup(name)
	if !ok || strings.TrimSpace(raw) == "" {
		return fallback
	}

	parsed, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		r.add("%s must be an integer", name)
		return fallback
	}

	return parsed
}

func (r *envReader) seconds(name string, fallback time.Duration) time.Duration {
	raw, ok := r.lookup(name)
	if !ok || strings.TrimSpace(raw) == "" {
		return fallback
	}

	parsed, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		r.add("%s must be an integer number of seconds", name)
		return fallback
	}

	return time.Duration(parsed) * time.Second
}

func (r *envReader) aiProviders(name string) []AIProvider {
	raw, ok := r.lookup(name)
	if !ok || strings.TrimSpace(raw) == "" {
		return nil
	}
	var providers []AIProvider
	if err := json.Unmarshal([]byte(raw), &providers); err != nil {
		r.add("%s must be a JSON array of {name,provider,api_key,base_url,model}", name)
		return nil
	}
	return providers
}

func (r *envReader) base64(name string) []byte {
	raw, ok := r.lookup(name)
	if !ok || strings.TrimSpace(raw) == "" {
		return nil
	}

	decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(raw))
	if err != nil {
		r.add("%s must be valid base64", name)
		return nil
	}

	return decoded
}

func (r *envReader) userID(name string) int64 {
	raw, ok := r.lookup(name)
	if !ok || strings.TrimSpace(raw) == "" {
		return 0
	}

	id, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil || id <= 0 {
		r.add("%s must be a positive Telegram user id", name)
		return 0
	}

	return id
}

func (r *envReader) userIDs(name string) []int64 {
	raw, ok := r.lookup(name)
	if !ok || strings.TrimSpace(raw) == "" {
		return nil
	}

	parts := strings.Split(raw, ",")
	ids := make([]int64, 0, len(parts))
	seen := make(map[int64]struct{}, len(parts))

	for _, part := range parts {
		value := strings.TrimSpace(part)
		if value == "" {
			r.add("%s contains an empty user id", name)
			continue
		}

		id, err := strconv.ParseInt(value, 10, 64)
		if err != nil || id <= 0 {
			r.add("%s contains an invalid user id", name)
			continue
		}

		if _, ok := seen[id]; ok {
			continue
		}

		seen[id] = struct{}{}
		ids = append(ids, id)
	}

	return ids
}

func (r *envReader) add(format string, args ...any) {
	addProblem(&r.problems, fmt.Sprintf(format, args...))
}

func requireNonEmpty(problems *[]string, field string, value string) {
	if strings.TrimSpace(value) == "" {
		addProblem(problems, field+" is required")
	}
}

func addProblem(problems *[]string, problem string) {
	*problems = append(*problems, problem)
}

func oneOf(value string, allowed ...string) bool {
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}

	return false
}

func anySet(values ...string) bool {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return true
		}
	}

	return false
}

func validHTTPURL(value string) bool {
	parsed, err := url.Parse(value)
	if err != nil {
		return false
	}

	return parsed.Host != "" && (parsed.Scheme == "http" || parsed.Scheme == "https")
}

func validMongoURI(value string) bool {
	parsed, err := url.Parse(value)
	if err != nil {
		return false
	}

	return parsed.Host != "" && (parsed.Scheme == "mongodb" || parsed.Scheme == "mongodb+srv")
}

func discoverEnvFile() string {
	candidates := make([]string, 0, 4)
	if wd, err := os.Getwd(); err == nil {
		candidates = append(candidates, filepath.Join(wd, ".env"))
	}
	if exe, err := os.Executable(); err == nil {
		exeDir := filepath.Dir(exe)
		candidates = append(candidates, filepath.Join(exeDir, ".env"))
		candidates = append(candidates, filepath.Join(filepath.Dir(exeDir), ".env"))
	}

	return firstExistingPath(candidates)
}

func firstExistingPath(candidates []string) string {
	seen := make(map[string]struct{}, len(candidates))
	for _, candidate := range candidates {
		if strings.TrimSpace(candidate) == "" {
			continue
		}

		cleaned := filepath.Clean(candidate)
		if _, ok := seen[cleaned]; ok {
			continue
		}
		seen[cleaned] = struct{}{}

		info, err := os.Stat(cleaned)
		if err == nil && !info.IsDir() {
			return cleaned
		}
	}

	return ".env"
}
