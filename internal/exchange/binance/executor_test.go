package binance

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"bottrade/internal/decimal"
	"bottrade/internal/domain"
	"bottrade/internal/orders"
)

func TestExecutorOpenPlacesEntryStopAndTakeProfitOrders(t *testing.T) {
	server := newBinanceTestServer(t)
	defer server.Close()

	executor := NewExecutor(ExecutorConfig{
		APIKey:               "key",
		APISecret:            "secret",
		BaseURL:              server.URL,
		Testnet:              true,
		RequestTimeout:       time.Second,
		ExchangeInfoCacheTTL: time.Minute,
	}, testLogger())
	executor.now = func() time.Time { return time.UnixMilli(1710000000000) }

	result, err := executor.Execute(context.Background(), testOpenConfirmation())
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	if result.Mode != "binance_testnet" {
		t.Fatalf("Mode = %q, want binance_testnet", result.Mode)
	}
	if !strings.Contains(result.Message, "Entry order:") {
		t.Fatalf("Message = %q, want entry order summary", result.Message)
	}

	requests := server.Requests()
	wantPaths := []string{
		"GET /fapi/v1/exchangeInfo",
		"POST /fapi/v1/marginType",
		"POST /fapi/v1/leverage",
		"POST /fapi/v1/order",     // post-only maker entry
		"GET /fapi/v1/order",      // confirm it filled before placing protection
		"POST /fapi/v1/algoOrder", // stop loss (conditional)
		"POST /fapi/v1/algoOrder", // take profit (conditional)
	}
	if len(requests) != len(wantPaths) {
		t.Fatalf("requests = %v, want %v", requests, wantPaths)
	}
	for i, want := range wantPaths {
		if requests[i].MethodPath != want {
			t.Fatalf("request %d = %q, want %q", i, requests[i].MethodPath, want)
		}
	}

	entry := requests[3].Query
	if entry.Get("type") != "LIMIT" || entry.Get("timeInForce") != "GTX" || entry.Get("side") != "BUY" || entry.Get("quantity") != "0.001" {
		t.Fatalf("entry query = %s, want post-only LIMIT BUY quantity 0.001", entry.Encode())
	}
	if entry.Get("price") != "67500" {
		t.Fatalf("entry query = %s, want limit price 67500", entry.Encode())
	}
	if entry.Get("signature") == "" || entry.Get("timestamp") == "" {
		t.Fatalf("entry query = %s, want signed request", entry.Encode())
	}

	stop := requests[5].Query
	if stop.Get("algoType") != "CONDITIONAL" || stop.Get("type") != "STOP_MARKET" ||
		stop.Get("closePosition") != "true" || stop.Get("triggerPrice") == "" {
		t.Fatalf("stop query = %s, want conditional close-position STOP_MARKET with triggerPrice", stop.Encode())
	}
	if stop.Get("clientAlgoId") == "" || stop.Get("stopPrice") != "" {
		t.Fatalf("stop query = %s, want clientAlgoId and no legacy stopPrice", stop.Encode())
	}

	takeProfit := requests[6].Query
	if takeProfit.Get("algoType") != "CONDITIONAL" || takeProfit.Get("type") != "TAKE_PROFIT_MARKET" ||
		takeProfit.Get("closePosition") != "true" || takeProfit.Get("triggerPrice") == "" {
		t.Fatalf("take-profit query = %s, want conditional close-position TAKE_PROFIT_MARKET with triggerPrice", takeProfit.Encode())
	}
}

func TestExecutorOpenRollsBackWhenStopLossFails(t *testing.T) {
	server := newBinanceTestServer(t)
	server.failStopLoss = true
	defer server.Close()

	executor := NewExecutor(ExecutorConfig{
		APIKey:               "key",
		APISecret:            "secret",
		BaseURL:              server.URL,
		Testnet:              true,
		RequestTimeout:       time.Second,
		ExchangeInfoCacheTTL: time.Minute,
	}, testLogger())
	executor.now = func() time.Time { return time.UnixMilli(1710000000000) }

	_, err := executor.Execute(context.Background(), testOpenConfirmation())
	if err == nil {
		t.Fatal("expected stop loss placement to fail")
	}
	if !strings.Contains(err.Error(), "stop loss") {
		t.Fatalf("error = %v, want stop loss failure", err)
	}

	// The entry order was placed before the SL failed; rollback must cancel it
	// so no unprotected order/position is left behind.
	var canceledEntry, checkedPosition bool
	for _, rq := range server.Requests() {
		switch rq.MethodPath {
		case "DELETE /fapi/v1/order":
			canceledEntry = true
		case "GET /fapi/v3/positionRisk":
			checkedPosition = true
		}
	}
	if !canceledEntry {
		t.Fatal("rollback did not cancel the entry order")
	}
	if !checkedPosition {
		t.Fatal("rollback did not check for an open position to flatten")
	}
}

func TestExecutorMoveStopLoss(t *testing.T) {
	server := newBinanceTestServer(t)
	defer server.Close()

	executor := NewExecutor(ExecutorConfig{
		APIKey:               "key",
		APISecret:            "secret",
		BaseURL:              server.URL,
		Testnet:              true,
		RequestTimeout:       time.Second,
		ExchangeInfoCacheTTL: time.Minute,
	}, testLogger())
	executor.now = func() time.Time { return time.UnixMilli(1710000000000) }

	if err := executor.MoveStopLoss(context.Background(), "BTCUSDT", domain.PositionSideLong, decimal.MustParse("61234.56"), "tb_x_sl", "tb_x_sl2"); err != nil {
		t.Fatalf("MoveStopLoss returned error: %v", err)
	}

	var canceled, placed url.Values
	for _, rq := range server.Requests() {
		switch rq.MethodPath {
		case "DELETE /fapi/v1/algoOrder":
			canceled = rq.Query
		case "POST /fapi/v1/algoOrder":
			placed = rq.Query
		}
	}
	if canceled.Get("clientAlgoId") != "tb_x_sl" {
		t.Fatalf("cancel = %s, want old client id tb_x_sl", canceled.Encode())
	}
	if placed.Get("type") != "STOP_MARKET" || placed.Get("algoType") != "CONDITIONAL" ||
		placed.Get("side") != "SELL" || placed.Get("clientAlgoId") != "tb_x_sl2" {
		t.Fatalf("new stop = %s, want SELL conditional STOP_MARKET with new id", placed.Encode())
	}
	if placed.Get("triggerPrice") != "61234.5" { // floored to the 0.10 tick size
		t.Fatalf("triggerPrice = %q, want 61234.5 (floored to tick)", placed.Get("triggerPrice"))
	}
}

func TestExecutorCurrentStop(t *testing.T) {
	server := newBinanceTestServer(t)
	defer server.Close()

	executor := NewExecutor(ExecutorConfig{
		APIKey:               "key",
		APISecret:            "secret",
		BaseURL:              server.URL,
		Testnet:              true,
		RequestTimeout:       time.Second,
		ExchangeInfoCacheTTL: time.Minute,
	}, testLogger())
	executor.now = func() time.Time { return time.UnixMilli(1710000000000) }

	stop, ok, err := executor.CurrentStop(context.Background(), "BTCUSDT")
	if err != nil {
		t.Fatalf("CurrentStop: %v", err)
	}
	// Must pick the STOP_MARKET leg, not the TAKE_PROFIT_MARKET one.
	if !ok || stop.ClientAlgoID != "tb_x_sl" || stop.Price.String() != "59000" {
		t.Fatalf("stop = {%+v ok:%v}, want tb_x_sl @ 59000", stop, ok)
	}
}

func TestExecutorClosePlacesReduceOnlyMarketOrder(t *testing.T) {
	server := newBinanceTestServer(t)
	defer server.Close()

	executor := NewExecutor(ExecutorConfig{
		APIKey:               "key",
		APISecret:            "secret",
		BaseURL:              server.URL,
		Testnet:              true,
		RequestTimeout:       time.Second,
		ExchangeInfoCacheTTL: time.Minute,
	}, testLogger())
	executor.now = func() time.Time { return time.UnixMilli(1710000000000) }

	result, err := executor.Execute(context.Background(), testCloseConfirmation())
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	if !strings.Contains(result.Message, "close submitted") {
		t.Fatalf("Message = %q, want close summary", result.Message)
	}
	// The close result must carry the realized PnL / symbol / side for the journal.
	if result.Symbol != "BTCUSDT" || result.Side != "long" || result.RealizedPnL.String() != "5.5" {
		t.Fatalf("close result = {sym:%q side:%q pnl:%s}, want BTCUSDT long 5.5", result.Symbol, result.Side, result.RealizedPnL.String())
	}

	requests := server.Requests()
	closeOrder := requests[len(requests)-1].Query
	if closeOrder.Get("type") != "MARKET" || closeOrder.Get("side") != "SELL" || closeOrder.Get("reduceOnly") != "true" {
		t.Fatalf("close query = %s, want reduce-only market sell", closeOrder.Encode())
	}
	if closeOrder.Get("quantity") != "0.005" {
		t.Fatalf("quantity = %q, want 0.005", closeOrder.Get("quantity"))
	}
}

func TestExecutorPositionsReturnsOpenPositions(t *testing.T) {
	server := newBinanceTestServer(t)
	defer server.Close()

	executor := NewExecutor(ExecutorConfig{
		APIKey:               "key",
		APISecret:            "secret",
		BaseURL:              server.URL,
		Testnet:              true,
		RequestTimeout:       time.Second,
		ExchangeInfoCacheTTL: time.Minute,
	}, testLogger())
	executor.now = func() time.Time { return time.UnixMilli(1710000000000) }

	positions, err := executor.Positions(context.Background())
	if err != nil {
		t.Fatalf("Positions returned error: %v", err)
	}
	if len(positions) != 1 {
		t.Fatalf("positions = %d, want 1", len(positions))
	}

	position := positions[0]
	if position.Symbol != "BTCUSDT" || position.Side != domain.PositionSideLong {
		t.Fatalf("position = %#v, want BTCUSDT long", position)
	}
	if !position.Amount.Equal(decimal.MustParse("0.01")) {
		t.Fatalf("amount = %s, want 0.01", position.Amount.String())
	}
	if !position.MarkPrice.Equal(decimal.MustParse("68000.5")) {
		t.Fatalf("mark price = %s, want 68000.5", position.MarkPrice.String())
	}
	if !position.UnrealizedProfit.Equal(decimal.MustParse("5.50")) {
		t.Fatalf("unrealized profit = %s, want 5.50", position.UnrealizedProfit.String())
	}
	if position.Leverage != 3 || position.MarginType != "isolated" {
		t.Fatalf("leverage/margin = %dx %s, want 3x isolated", position.Leverage, position.MarginType)
	}
}

func TestExecutorRefusesNonTestnetWhenRealTradingDisabled(t *testing.T) {
	executor := NewExecutor(ExecutorConfig{
		APIKey:    "key",
		APISecret: "secret",
		BaseURL:   "https://example.invalid",
		Testnet:   false,
	}, testLogger())

	_, err := executor.Execute(context.Background(), testOpenConfirmation())
	if err == nil {
		t.Fatal("Execute returned nil error, want safety error")
	}
	if !strings.Contains(err.Error(), "BINANCE_TESTNET=false") {
		t.Fatalf("error = %q, want safety error", err.Error())
	}
}

type binanceTestServer struct {
	*httptest.Server
	mu           sync.Mutex
	requests     []recordedRequest
	orderID      int64
	failStopLoss bool // when true, the STOP_MARKET algo order is rejected
}

type recordedRequest struct {
	MethodPath string
	Query      url.Values
}

func newBinanceTestServer(t *testing.T) *binanceTestServer {
	t.Helper()

	server := &binanceTestServer{orderID: 1000}
	server.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		server.mu.Lock()
		server.requests = append(server.requests, recordedRequest{
			MethodPath: r.Method + " " + r.URL.Path,
			Query:      r.URL.Query(),
		})
		server.mu.Unlock()

		if r.URL.Path != "/fapi/v1/exchangeInfo" && r.Header.Get("X-MBX-APIKEY") != "key" {
			t.Fatalf("missing api key header for %s", r.URL.Path)
		}

		switch r.URL.Path {
		case "/fapi/v1/exchangeInfo":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"symbols":[{"symbol":"BTCUSDT","filters":[{"filterType":"PRICE_FILTER","tickSize":"0.10"},{"filterType":"LOT_SIZE","minQty":"0.001","stepSize":"0.001"},{"filterType":"MIN_NOTIONAL","notional":"5"}]}]}`))
		case "/fapi/v1/marginType":
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"code":-4046,"msg":"No need to change margin type."}`))
		case "/fapi/v1/leverage":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"leverage":3,"symbol":"BTCUSDT"}`))
		case "/fapi/v1/order":
			if r.Method == http.MethodDelete {
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"clientOrderId":"` + r.URL.Query().Get("origClientOrderId") + `","orderId":1,"symbol":"BTCUSDT","status":"CANCELED"}`))
				return
			}
			if r.Method == http.MethodGet {
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"clientOrderId":"` + r.URL.Query().Get("origClientOrderId") + `","orderId":1001,"symbol":"BTCUSDT","status":"FILLED","type":"LIMIT","executedQty":"0.001"}`))
				return
			}
			server.mu.Lock()
			server.orderID++
			orderID := server.orderID
			server.mu.Unlock()
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"clientOrderId":"` + r.URL.Query().Get("newClientOrderId") + `","orderId":` + strconvInt64(orderID) + `,"symbol":"BTCUSDT","status":"NEW","type":"` + r.URL.Query().Get("type") + `"}`))
		case "/fapi/v1/algoOrder":
			if r.Method == http.MethodDelete {
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"algoId":1,"clientAlgoId":"` + r.URL.Query().Get("clientAlgoId") + `","code":"200","msg":"success"}`))
				return
			}
			if server.failStopLoss && r.URL.Query().Get("type") == "STOP_MARKET" {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"code":-2021,"msg":"Order would immediately trigger."}`))
				return
			}
			server.mu.Lock()
			server.orderID++
			algoID := server.orderID
			server.mu.Unlock()
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"clientAlgoId":"` + r.URL.Query().Get("clientAlgoId") + `","algoId":` + strconvInt64(algoID) + `,"symbol":"BTCUSDT","algoStatus":"NEW","type":"` + r.URL.Query().Get("type") + `"}`))
		case "/fapi/v1/openAlgoOrders":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`[{"clientAlgoId":"tb_x_sl","orderType":"STOP_MARKET","symbol":"BTCUSDT","triggerPrice":"59000.0"},{"clientAlgoId":"tb_x_tp","orderType":"TAKE_PROFIT_MARKET","symbol":"BTCUSDT","triggerPrice":"66000.0"}]`))
		case "/fapi/v3/positionRisk":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`[{"symbol":"BTCUSDT","positionAmt":"0.010","entryPrice":"67500.0","markPrice":"68000.50","unRealizedProfit":"5.50","leverage":"3","marginType":"isolated","positionSide":"BOTH"},{"symbol":"ETHUSDT","positionAmt":"0","entryPrice":"0","markPrice":"0","unRealizedProfit":"0","leverage":"2","marginType":"isolated","positionSide":"BOTH"}]`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))

	return server
}

func (s *binanceTestServer) Requests() []recordedRequest {
	s.mu.Lock()
	defer s.mu.Unlock()

	return append([]recordedRequest(nil), s.requests...)
}

func testOpenConfirmation() orders.Confirmation {
	return orders.Confirmation{
		ID:     "abc123def456ghi789",
		UserID: 12345,
		Intent: domain.Intent{
			Type: domain.IntentOpen,
			Open: &domain.OpenIntent{
				Symbol:   "BTCUSDT",
				Side:     domain.SideLong,
				Leverage: 3,
				Entry:    decimal.MustParse("67500"),
				StopLoss: decimal.MustParse("65000"),
				TakeProfits: []decimal.Decimal{
					decimal.MustParse("72000"),
				},
				Size: domain.OrderSize{
					Kind:   domain.SizeUSDT,
					Amount: decimal.MustParse("100"),
				},
			},
		},
	}
}

func testCloseConfirmation() orders.Confirmation {
	return orders.Confirmation{
		ID:     "abc123def456ghi789",
		UserID: 12345,
		Intent: domain.Intent{
			Type: domain.IntentClose,
			Close: &domain.CloseIntent{
				Symbol:          "BTCUSDT",
				Percent:         decimal.MustParse("50"),
				HasPercent:      true,
				ResolvedPercent: decimal.MustParse("50"),
			},
		},
	}
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func strconvInt64(value int64) string {
	return strconv.FormatInt(value, 10)
}
