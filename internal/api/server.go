package api

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"bottrade/internal/auth"
	"bottrade/internal/config"
	"bottrade/internal/dashboard"
	"bottrade/internal/journal"
	"bottrade/internal/signals"
	"bottrade/internal/users"

	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/limiter"
)

const (
	// maxRequestBodyLimit caps request bodies on every route. Every endpoint
	// here takes small JSON or no body, so a tight limit blunts
	// memory-exhaustion abuse on the public port.
	maxRequestBodyLimit = 256 * 1024

	// Webhook rate limits: a per-client cap for fairness plus a global ceiling.
	// The per-client key is the client IP, but Fly-Client-IP is a
	// client-supplied header, so the global ceiling is the backstop that bounds
	// secret brute-forcing even if the per-client key is spoofed off the proxy.
	webhookRatePerIP  = 30
	webhookRateGlobal = 120
	webhookRateWindow = time.Minute
)

type Server struct {
	cfg         config.Config
	processor   *signals.Processor
	users       *users.Service
	report      *journal.Service
	tokenizer   *auth.Tokenizer
	credentials *auth.CredentialService
	logger      *slog.Logger
	app         *fiber.App
}

// Option customises a Server without breaking existing call sites.
type Option func(*Server)

// WithUsers enables the registration/login endpoints backed by svc.
func WithUsers(svc *users.Service) Option {
	return func(s *Server) { s.users = svc }
}

// WithTokenizer enables session JWTs: login returns a token and protected
// endpoints require it.
func WithTokenizer(t *auth.Tokenizer) Option {
	return func(s *Server) { s.tokenizer = t }
}

// WithReport enables the GET /api/report endpoint backed by the trade journal.
func WithReport(svc *journal.Service) Option {
	return func(s *Server) { s.report = svc }
}

// WithCredentials enables the /api/credentials endpoints (per-user Binance keys).
func WithCredentials(svc *auth.CredentialService) Option {
	return func(s *Server) { s.credentials = svc }
}

func NewServer(cfg config.Config, processor *signals.Processor, logger *slog.Logger, opts ...Option) *Server {
	if logger == nil {
		logger = slog.Default()
	}

	server := &Server{
		cfg:       cfg,
		processor: processor,
		logger:    logger,
		app: fiber.New(fiber.Config{
			AppName:   "tradebot",
			BodyLimit: maxRequestBodyLimit,
		}),
	}
	for _, opt := range opts {
		opt(server)
	}
	server.routes()
	return server
}

func (s *Server) App() *fiber.App {
	return s.app
}

func (s *Server) Run(ctx context.Context) error {
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := s.app.ShutdownWithContext(shutdownCtx); err != nil {
			s.logger.Error("http shutdown failed", "error", err)
		}
	}()

	s.logger.Info("http server started", "addr", s.cfg.HTTP.Addr)
	if err := s.app.Listen(s.cfg.HTTP.Addr); err != nil && !errors.Is(err, context.Canceled) {
		return fmt.Errorf("listen http: %w", err)
	}
	return nil
}

func (s *Server) routes() {
	s.app.Get("/healthz", func(c fiber.Ctx) error {
		return c.JSON(fiber.Map{"status": "ok"})
	})
	// Readiness probe stays public but exposes no trading config — the detailed
	// flags moved to the token-gated /status below.
	s.app.Get("/readyz", func(c fiber.Ctx) error {
		return c.JSON(fiber.Map{"status": "ok"})
	})
	s.app.Get("/status", s.handleStatus)

	// Account registration / login. Rate-limited because they are public and
	// password-checking. Disabled (501) when no user service is wired.
	authLimiter := limiter.New(limiter.Config{
		Max:          webhookRatePerIP,
		Expiration:   webhookRateWindow,
		LimitReached: webhookRateLimited,
	})
	s.app.Post("/api/register", authLimiter, s.handleRegister)
	s.app.Post("/api/login", authLimiter, s.handleLogin)
	s.app.Post("/api/telegram-auth", authLimiter, s.handleTelegramAuth)
	s.app.Get("/api/report", s.handleReport)
	s.app.Post("/api/credentials", s.requireAuth, s.handleStoreCredential)
	s.app.Get("/api/credentials", s.requireAuth, s.handleGetCredential)
	s.app.Delete("/api/credentials", s.requireAuth, s.handleDeleteCredential)

	// Rate-limit the public webhook: it is the only internet-reachable path that
	// can drive the signal/order flow, so cap brute-forcing of the secret and
	// signal floods. A per-client limiter gives fairness; a global ceiling is
	// the security backstop, since the per-client key derives from the
	// client-supplied Fly-Client-IP header and could be rotated to dodge the
	// per-client cap.
	perIPLimiter := limiter.New(limiter.Config{
		Max:        webhookRatePerIP,
		Expiration: webhookRateWindow,
		KeyGenerator: func(c fiber.Ctx) string {
			if ip := strings.TrimSpace(c.Get("Fly-Client-IP")); ip != "" {
				return ip
			}
			return c.IP()
		},
		LimitReached: webhookRateLimited,
	})
	globalLimiter := limiter.New(limiter.Config{
		Max:          webhookRateGlobal,
		Expiration:   webhookRateWindow,
		KeyGenerator: func(fiber.Ctx) string { return "tradingview-webhook" },
		LimitReached: webhookRateLimited,
	})
	s.app.Post("/tradingview/webhook", globalLimiter, perIPLimiter, s.handleTradingViewWebhook)

	// Mount the embedded dashboard last: its "/*" catch-all must not shadow the
	// API routes above. A mount failure must not take down the API, so log and
	// continue with health checks and the webhook still served.
	if err := dashboard.Register(s.app); err != nil {
		s.logger.Error("dashboard mount failed", "error", err)
	}
}

// handleStatus serves operational/trading config flags, gated by a bearer
// token. When no token is configured the endpoint is disabled (404) so the
// flags are never exposed unauthenticated on the public port.
func (s *Server) handleStatus(c fiber.Ctx) error {
	token := s.cfg.HTTP.StatusToken
	if token == "" {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "status endpoint is disabled"})
	}

	provided := bearerToken(c)
	if subtle.ConstantTimeCompare([]byte(provided), []byte(token)) != 1 {
		s.logger.Warn("status endpoint rejected")
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "unauthorized"})
	}

	return c.JSON(fiber.Map{
		"status":      "ok",
		"tradingview": s.cfg.TradingView.Enabled,
		"ai":          s.cfg.AI.Enabled,
		"autotrade":   s.cfg.AI.AutoTradeEnabled,
		"dry_run":     s.cfg.App.DryRun,
	})
}

func webhookRateLimited(c fiber.Ctx) error {
	return c.Status(fiber.StatusTooManyRequests).JSON(fiber.Map{"error": "rate limit exceeded"})
}

type credentials struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func (s *Server) handleRegister(c fiber.Ctx) error {
	if s.users == nil {
		return c.Status(fiber.StatusNotImplemented).JSON(fiber.Map{"error": "registration is not enabled"})
	}
	var body credentials
	if err := json.Unmarshal(c.Body(), &body); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid JSON body"})
	}
	user, err := s.users.Register(c.Context(), body.Username, body.Password)
	if err != nil {
		if errors.Is(err, users.ErrUsernameTaken) {
			return c.Status(fiber.StatusConflict).JSON(fiber.Map{"error": "username already taken"})
		}
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
	}
	return c.Status(fiber.StatusCreated).JSON(fiber.Map{"id": user.ID, "username": user.Username, "role": user.Role})
}

func (s *Server) handleReport(c fiber.Ctx) error {
	if s.report == nil {
		return c.Status(fiber.StatusNotImplemented).JSON(fiber.Map{"error": "reporting is not enabled"})
	}
	filter := journal.Filter{ClosedOnly: true}
	if raw := strings.TrimSpace(c.Query("user")); raw != "" {
		if id, err := strconv.ParseInt(raw, 10, 64); err == nil {
			filter.UserID = id
		}
	}
	if mode := strings.TrimSpace(c.Query("mode")); mode != "" {
		filter.Mode = mode
	}
	if strategy := strings.TrimSpace(c.Query("strategy")); strategy != "" {
		filter.Strategy = strategy
	}
	if symbol := strings.TrimSpace(c.Query("symbol")); symbol != "" {
		filter.Symbol = symbol
	}
	report, err := s.report.Report(c.Context(), filter)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
	}
	return c.JSON(report)
}

type credentialBody struct {
	APIKey    string `json:"api_key"`
	APISecret string `json:"api_secret"`
	Testnet   bool   `json:"testnet"`
}

func (s *Server) handleStoreCredential(c fiber.Ctx) error {
	if s.credentials == nil {
		return c.Status(fiber.StatusNotImplemented).JSON(fiber.Map{"error": "credentials are not enabled (set CREDENTIAL_ENCRYPTION_KEY)"})
	}
	var body credentialBody
	if err := json.Unmarshal(c.Body(), &body); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid JSON body"})
	}
	err := s.credentials.Store(c.Context(), claimsOf(c).Subject, auth.BinanceKeys{
		APIKey:    body.APIKey,
		APISecret: body.APISecret,
		Testnet:   body.Testnet,
	})
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
	}
	return c.Status(fiber.StatusCreated).JSON(fiber.Map{"configured": true, "testnet": body.Testnet})
}

func (s *Server) handleGetCredential(c fiber.Ctx) error {
	if s.credentials == nil {
		return c.Status(fiber.StatusNotImplemented).JSON(fiber.Map{"error": "credentials are not enabled"})
	}
	keys, err := s.credentials.Load(c.Context(), claimsOf(c).Subject)
	if errors.Is(err, auth.ErrNoCredential) {
		return c.JSON(fiber.Map{"configured": false})
	}
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "could not read credential"})
	}
	// Never return the secret — only that it is set, plus the masked key tail.
	return c.JSON(fiber.Map{"configured": true, "testnet": keys.Testnet, "api_key_tail": maskTail(keys.APIKey)})
}

func (s *Server) handleDeleteCredential(c fiber.Ctx) error {
	if s.credentials == nil {
		return c.Status(fiber.StatusNotImplemented).JSON(fiber.Map{"error": "credentials are not enabled"})
	}
	if err := s.credentials.Delete(c.Context(), claimsOf(c).Subject); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "could not delete credential"})
	}
	return c.JSON(fiber.Map{"configured": false})
}

func maskTail(s string) string {
	if len(s) <= 4 {
		return "****"
	}
	return "…" + s[len(s)-4:]
}

// handleTelegramAuth verifies Telegram Mini App init data and issues a session
// token. The user identity is "tg:<telegram id>" — the same key used for
// per-user credentials and the journal.
func (s *Server) handleTelegramAuth(c fiber.Ctx) error {
	if s.tokenizer == nil {
		return c.Status(fiber.StatusNotImplemented).JSON(fiber.Map{"error": "auth is not enabled"})
	}
	var body struct {
		InitData string `json:"init_data"`
	}
	if err := json.Unmarshal(c.Body(), &body); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid JSON body"})
	}
	user, err := auth.VerifyTelegramInitData(body.InitData, s.cfg.Telegram.BotToken, 24*time.Hour, time.Now())
	if err != nil {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "invalid telegram login"})
	}
	username := user.Username
	if username == "" {
		username = user.FirstName
	}
	return c.JSON(s.loginResponse("tg:"+strconv.FormatInt(user.ID, 10), username, "user"))
}

func (s *Server) handleLogin(c fiber.Ctx) error {
	if s.users == nil {
		return c.Status(fiber.StatusNotImplemented).JSON(fiber.Map{"error": "login is not enabled"})
	}
	var body credentials
	if err := json.Unmarshal(c.Body(), &body); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid JSON body"})
	}
	user, err := s.users.Authenticate(c.Context(), body.Username, body.Password)
	if err != nil {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "invalid username or password"})
	}
	return c.JSON(s.loginResponse(user.ID, user.Username, string(user.Role)))
}

// loginResponse returns the user fields plus a session token when a tokenizer
// is configured. Both the password and Telegram login paths use it.
func (s *Server) loginResponse(id, username, role string) fiber.Map {
	out := fiber.Map{"id": id, "username": username, "role": role}
	if s.tokenizer != nil {
		if token, err := s.tokenizer.Issue(id, username, role); err == nil {
			out["token"] = token
		} else {
			s.logger.Error("issue session token failed", "error", err)
		}
	}
	return out
}

// requireAuth is middleware that rejects requests without a valid session token
// and stores the verified claims for the handler.
func (s *Server) requireAuth(c fiber.Ctx) error {
	if s.tokenizer == nil {
		return c.Status(fiber.StatusNotImplemented).JSON(fiber.Map{"error": "auth is not enabled"})
	}
	claims, err := s.tokenizer.Verify(bearerToken(c))
	if err != nil {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "unauthorized"})
	}
	c.Locals("claims", claims)
	return c.Next()
}

// claims returns the authenticated user's claims set by requireAuth.
func claimsOf(c fiber.Ctx) auth.Claims {
	if v, ok := c.Locals("claims").(auth.Claims); ok {
		return v
	}
	return auth.Claims{}
}

func bearerToken(c fiber.Ctx) string {
	header := strings.TrimSpace(c.Get(fiber.HeaderAuthorization))
	const prefix = "Bearer "
	if len(header) > len(prefix) && strings.EqualFold(header[:len(prefix)], prefix) {
		return strings.TrimSpace(header[len(prefix):])
	}
	return ""
}

func (s *Server) handleTradingViewWebhook(c fiber.Ctx) error {
	if !s.cfg.TradingView.Enabled {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "tradingview webhook is disabled"})
	}
	if s.processor == nil {
		return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{"error": "signal processor is not connected"})
	}

	var payload tradingViewPayload
	if err := json.Unmarshal(c.Body(), &payload); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "invalid JSON webhook payload",
			"hint":  `TradingView alert body should be valid JSON such as {"secret":"...","symbol":"BTCUSDT","price":"67500"}`,
		})
	}

	secret := strings.TrimSpace(c.Get("X-TradeBot-Webhook-Secret"))
	if secret == "" {
		secret = strings.TrimSpace(c.Get("X-TradingView-Secret"))
	}
	if secret == "" {
		secret = strings.TrimSpace(payload.Secret)
	}
	// Constant-time compare: the webhook is on a public port, so a byte-by-byte
	// `!=` would leak the secret through response timing.
	if subtle.ConstantTimeCompare([]byte(secret), []byte(s.cfg.TradingView.WebhookSecret)) != 1 {
		s.logger.Warn("tradingview webhook rejected")
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "unauthorized webhook"})
	}

	signal := payload.Signal()
	result, err := s.processor.Process(c.Context(), signal)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
	}

	return c.JSON(result)
}

type tradingViewPayload struct {
	Secret     string         `json:"secret"`
	Source     string         `json:"source"`
	Symbol     string         `json:"symbol"`
	Ticker     string         `json:"ticker"`
	Interval   string         `json:"interval"`
	Timeframe  string         `json:"timeframe"`
	Price      string         `json:"price"`
	Close      string         `json:"close"`
	Strategy   string         `json:"strategy"`
	ActionHint string         `json:"action_hint"`
	Action     string         `json:"action"`
	SideHint   string         `json:"side_hint"`
	Message    string         `json:"message"`
	Indicators map[string]any `json:"indicators"`
}

func (p tradingViewPayload) Signal() signals.MarketSignal {
	symbol := firstNonEmpty(p.Symbol, p.Ticker)
	interval := firstNonEmpty(p.Interval, p.Timeframe)
	price := firstNonEmpty(p.Price, p.Close)
	actionHint := firstNonEmpty(p.ActionHint, p.Action)

	return signals.MarketSignal{
		Source:     p.Source,
		Symbol:     symbol,
		Interval:   interval,
		Price:      price,
		Strategy:   p.Strategy,
		ActionHint: actionHint,
		SideHint:   p.SideHint,
		Message:    p.Message,
		Indicators: stringifyMap(p.Indicators),
		ReceivedAt: time.Now(),
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func stringifyMap(values map[string]any) map[string]string {
	if len(values) == 0 {
		return map[string]string{}
	}

	result := make(map[string]string, len(values))
	for key, value := range values {
		result[key] = fmt.Sprint(value)
	}
	return result
}
