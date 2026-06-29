// Command e2eserver boots the dashboard + API with in-memory stores and a stub
// Binance market-data backend, so Playwright can drive the real UI end-to-end
// without MongoDB, exchange keys, or network. It is a TEST harness only — never
// deployed — and places no real orders (the goal endpoints are paper-only).
package main

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"

	"time"

	"bottrade/internal/api"
	"bottrade/internal/auth"
	"bottrade/internal/config"
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
		"TELEGRAM_ALLOWED_USER_IDS": "12345",
		"MONGODB_URI":               "mongodb://localhost/none",
		"MONGODB_DATABASE":          "tradebot",
		"HTTP_ENABLED":              "true",
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

	// A dry-run order service (no exchange, no real orders) so the web console's
	// parser-error path and Confirm flow can be driven in E2E safely.
	orderSvc := orders.NewService(true, time.Minute, logger)

	server := api.NewServer(cfg, nil, logger,
		api.WithUsers(userSvc),
		api.WithTokenizer(tokenizer),
		api.WithOrders(orderSvc),
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
