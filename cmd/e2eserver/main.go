// Command e2eserver boots the dashboard + API with in-memory stores and a stub
// Binance market-data backend, so Playwright can drive the real UI end-to-end
// without MongoDB, exchange keys, or network. It is a TEST harness only — never
// deployed — and places no real orders (the goal endpoints are paper-only).
package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"

	"time"

	"bottrade/internal/api"
	"bottrade/internal/auth"
	"bottrade/internal/config"
	"bottrade/internal/decimal"
	"bottrade/internal/domain"
	"bottrade/internal/orders"
	"bottrade/internal/users"
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	stubURL, err := startKlineStub()
	if err != nil {
		logger.Error("kline stub", "error", err)
		os.Exit(1)
	}

	cfg, err := config.LoadFromLookup(lookup(map[string]string{
		"TELEGRAM_BOT_TOKEN":        "123:abc",
		"TELEGRAM_ADMIN_USER_ID":    "99999",
		"TELEGRAM_ALLOWED_USER_IDS": "12345",
		"MONGODB_URI":               "mongodb://localhost/none",
		"MONGODB_DATABASE":          "tradebot",
		"HTTP_ENABLED":              "true",
		"DRY_RUN":                   "false",
		"CAMPAIGN_LIVE_ENABLED":     "true",
		"CREDENTIAL_ENCRYPTION_KEY": "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=",
		"MARKETDATA_BASE_URL":       stubURL,
		"MAX_LEVERAGE":              "20",
		"ACCESS_OPEN":               "true", // harness runs unlocked; gating is unit-tested
	}))
	if err != nil {
		logger.Error("config", "error", err)
		os.Exit(1)
	}

	tokenizer, err := auth.NewTokenizer(bytes.Repeat([]byte("e2e-secret-"), 4), 0)
	if err != nil {
		logger.Error("tokenizer", "error", err)
		os.Exit(1)
	}
	userSvc, err := users.NewService(users.NewMemoryRepository())
	if err != nil {
		logger.Error("users", "error", err)
		os.Exit(1)
	}
	if _, err := userSvc.Register(context.Background(), "e2e_user", "password123"); err != nil {
		logger.Error("seed e2e user", "error", err)
		os.Exit(1)
	}

	keyring, err := auth.NewKeyring(map[string][]byte{"v1": []byte("0123456789abcdef0123456789abcdef")}, "v1")
	if err != nil {
		logger.Error("keyring", "error", err)
		os.Exit(1)
	}
	credSvc, err := auth.NewCredentialService(keyring, newE2ECredRepo())
	if err != nil {
		logger.Error("credentials", "error", err)
		os.Exit(1)
	}

	// The harness opens the campaign/testnet gate, but the executor stays in-process:
	// it never calls Binance and exists only so Playwright can verify arm/disarm UX.
	orderSvc := orders.NewServiceWithRepositories(time.Minute, orders.DryRunExecutor{DryRun: true}, orders.ServiceDependencies{
		ExecutorProvider: e2eExecutorProvider{credentials: credSvc},
	}, logger)

	server := api.NewServer(cfg, nil, logger,
		api.WithUsers(userSvc),
		api.WithTokenizer(tokenizer),
		api.WithOrders(orderSvc),
		api.WithCredentials(credSvc),
	)

	addr := envOr("E2E_ADDR", ":8099")
	fmt.Fprintf(os.Stderr, "e2eserver listening on %s (market stub %s)\n", addr, stubURL)
	if err := server.App().Listen(addr); err != nil {
		logger.Error("listen", "error", err)
		os.Exit(1)
	}
}

// startKlineStub serves an uptrending /fapi/v1/klines series so a paper goal run
// produces deterministic winning trades. Returns the stub's base URL.
func startKlineStub() (string, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	body := uptrendKlines(120)
	mux := http.NewServeMux()
	mux.HandleFunc("/fapi/v1/klines", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(body))
	})
	// ticker/24hr keeps favourites/quotes from erroring if exercised.
	mux.HandleFunc("/fapi/v1/ticker/24hr", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[{"symbol":"BTCUSDT","lastPrice":"60000","priceChangePercent":"1.2"}]`))
	})
	go func() { _ = http.Serve(ln, mux) }()
	return "http://" + ln.Addr().String(), nil
}

func uptrendKlines(n int) string {
	var b strings.Builder
	b.WriteString("[")
	price := 100.0
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		open := i * 3600000
		fmt.Fprintf(&b, `[%d,"%.4f","%.4f","%.4f","%.4f","1",%d,"0",0,"0","0","0"]`,
			open, price, price*1.012, price*0.9995, price, open+3600000)
		price *= 1.01
	}
	b.WriteString("]")
	return b.String()
}

func lookup(m map[string]string) func(string) (string, bool) {
	return func(key string) (string, bool) { v, ok := m[key]; return v, ok }
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

type e2eCredRepo struct {
	mu   sync.Mutex
	rows map[string][]auth.BinanceCredential
}

func newE2ECredRepo() *e2eCredRepo {
	return &e2eCredRepo{rows: make(map[string][]auth.BinanceCredential)}
}

func (r *e2eCredRepo) Save(_ context.Context, cred auth.BinanceCredential) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	list := r.rows[cred.UserID]
	replaced := false
	for i := range list {
		if list[i].Profile == cred.Profile {
			list[i] = cred
			replaced = true
			break
		}
	}
	if !replaced {
		list = append(list, cred)
	}
	r.rows[cred.UserID] = list
	return nil
}

func (r *e2eCredRepo) List(_ context.Context, userID string) ([]auth.BinanceCredential, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	src := r.rows[userID]
	out := make([]auth.BinanceCredential, len(src))
	copy(out, src)
	return out, nil
}

func (r *e2eCredRepo) FindActive(_ context.Context, userID string) (auth.BinanceCredential, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, row := range r.rows[userID] {
		if row.Active {
			return row, nil
		}
	}
	return auth.BinanceCredential{}, auth.ErrNoCredential
}

func (r *e2eCredRepo) Remove(_ context.Context, userID, profile string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	list := r.rows[userID]
	out := list[:0]
	for _, row := range list {
		if row.Profile != profile {
			out = append(out, row)
		}
	}
	if len(out) == 0 {
		delete(r.rows, userID)
		return nil
	}
	r.rows[userID] = out
	return nil
}

func (r *e2eCredRepo) SetActive(_ context.Context, userID, profile string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	list := r.rows[userID]
	for i := range list {
		list[i].Active = list[i].Profile == profile
	}
	r.rows[userID] = list
	return nil
}

type e2eExecutorProvider struct {
	credentials *auth.CredentialService
}

func (p e2eExecutorProvider) ExecutorFor(ctx context.Context, userKey string) (orders.Executor, bool, error) {
	if p.credentials == nil {
		return nil, false, nil
	}
	if _, err := p.credentials.Load(ctx, userKey); err != nil {
		if errors.Is(err, auth.ErrNoCredential) {
			return nil, false, nil
		}
		return nil, false, err
	}
	return e2eTestnetExecutor{}, true, nil
}

type e2eTestnetExecutor struct{}

func (e2eTestnetExecutor) Execute(ctx context.Context, confirmation orders.Confirmation) (orders.ExecutionResult, error) {
	select {
	case <-ctx.Done():
		return orders.ExecutionResult{}, ctx.Err()
	default:
	}
	result := orders.ExecutionResult{
		Mode:          "binance_testnet",
		ClientOrderID: "e2e_" + confirmation.ID,
		Message:       "E2E testnet accepted. No real order was sent.",
		Quantity:      decimal.NewFromInt(1),
	}
	if confirmation.Intent.Open != nil {
		result.Symbol = confirmation.Intent.Open.Symbol
		result.Side = string(confirmation.Intent.Open.Side)
	}
	if confirmation.Intent.Close != nil {
		result.Symbol = confirmation.Intent.Close.Symbol
		result.RealizedPnL = decimal.Zero()
	}
	return result, nil
}

func (e2eTestnetExecutor) Positions(context.Context) ([]domain.Position, error) {
	return nil, nil
}

func (e2eTestnetExecutor) RealizedTrade(context.Context, string, string, time.Time, decimal.Decimal) (orders.RealizedTrade, bool, error) {
	return orders.RealizedTrade{}, false, nil
}
