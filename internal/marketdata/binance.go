package marketdata

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"bottrade/internal/decimal"
)

// DefaultBinanceBaseURL is Binance Futures' production market-data host. It is
// used even when orders execute on testnet: these endpoints are public,
// read-only, and the testnet host does not serve the /futures/data ratio
// endpoints.
const DefaultBinanceBaseURL = "https://fapi.binance.com"

// BinanceProvider reads Binance Futures' free public market-data endpoints. No
// API key is required. It is safe for concurrent use.
type BinanceProvider struct {
	baseURL string
	client  *http.Client

	mu         sync.Mutex
	symbols    []string
	symbolsAt  time.Time
	symbolsTTL time.Duration
}

// NewBinanceProvider builds a provider. An empty baseURL defaults to production;
// a nil client gets a 10s-timeout client.
func NewBinanceProvider(baseURL string, client *http.Client) *BinanceProvider {
	if strings.TrimSpace(baseURL) == "" {
		baseURL = DefaultBinanceBaseURL
	}
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	return &BinanceProvider{baseURL: strings.TrimRight(baseURL, "/"), client: client, symbolsTTL: time.Hour}
}

// Symbols returns the tradable USDT-margined perpetual symbols on Binance
// Futures, cached for an hour. The list is sorted alphabetically.
func (p *BinanceProvider) Symbols(ctx context.Context) ([]string, error) {
	p.mu.Lock()
	if len(p.symbols) > 0 && time.Since(p.symbolsAt) < p.symbolsTTL {
		out := append([]string(nil), p.symbols...)
		p.mu.Unlock()
		return out, nil
	}
	p.mu.Unlock()

	var body struct {
		Symbols []struct {
			Symbol       string `json:"symbol"`
			Status       string `json:"status"`
			ContractType string `json:"contractType"`
			QuoteAsset   string `json:"quoteAsset"`
		} `json:"symbols"`
	}
	if err := p.get(ctx, "/fapi/v1/exchangeInfo", nil, &body); err != nil {
		return nil, err
	}
	symbols := make([]string, 0, len(body.Symbols))
	for _, s := range body.Symbols {
		if s.Status == "TRADING" && s.ContractType == "PERPETUAL" && s.QuoteAsset == "USDT" {
			symbols = append(symbols, s.Symbol)
		}
	}
	sort.Strings(symbols)

	p.mu.Lock()
	p.symbols = symbols
	p.symbolsAt = time.Now()
	p.mu.Unlock()
	return symbols, nil
}

// SearchSymbols returns up to limit symbols matching query (case-insensitive
// substring), favouring prefix matches. An empty query returns the first limit
// symbols.
func (p *BinanceProvider) SearchSymbols(ctx context.Context, query string, limit int) ([]string, error) {
	all, err := p.Symbols(ctx)
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 20
	}
	query = strings.ToUpper(strings.TrimSpace(query))

	var prefix, contains []string
	for _, sym := range all {
		if query == "" || strings.HasPrefix(sym, query) {
			prefix = append(prefix, sym)
		} else if strings.Contains(sym, query) {
			contains = append(contains, sym)
		}
	}
	out := append(prefix, contains...)
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (p *BinanceProvider) Funding(ctx context.Context, symbol string) (Funding, error) {
	var body struct {
		Symbol          string `json:"symbol"`
		MarkPrice       string `json:"markPrice"`
		IndexPrice      string `json:"indexPrice"`
		LastFundingRate string `json:"lastFundingRate"`
		NextFundingTime int64  `json:"nextFundingTime"`
		Time            int64  `json:"time"`
	}
	if err := p.get(ctx, "/fapi/v1/premiumIndex", url.Values{"symbol": {symbol}}, &body); err != nil {
		return Funding{}, err
	}
	return Funding{
		Symbol:          body.Symbol,
		MarkPrice:       parseOrZero(body.MarkPrice),
		IndexPrice:      parseOrZero(body.IndexPrice),
		LastFundingRate: parseOrZero(body.LastFundingRate),
		NextFundingTime: msToTime(body.NextFundingTime),
		At:              msToTime(body.Time),
	}, nil
}

func (p *BinanceProvider) OpenInterest(ctx context.Context, symbol string) (OpenInterest, error) {
	var body struct {
		Symbol       string `json:"symbol"`
		OpenInterest string `json:"openInterest"`
		Time         int64  `json:"time"`
	}
	if err := p.get(ctx, "/fapi/v1/openInterest", url.Values{"symbol": {symbol}}, &body); err != nil {
		return OpenInterest{}, err
	}
	return OpenInterest{
		Symbol:       body.Symbol,
		OpenInterest: parseOrZero(body.OpenInterest),
		At:           msToTime(body.Time),
	}, nil
}

func (p *BinanceProvider) LongShortRatio(ctx context.Context, symbol, period string) (LongShortRatio, error) {
	var rows []struct {
		Symbol         string `json:"symbol"`
		LongShortRatio string `json:"longShortRatio"`
		LongAccount    string `json:"longAccount"`
		ShortAccount   string `json:"shortAccount"`
		Timestamp      int64  `json:"timestamp"`
	}
	if err := p.get(ctx, "/futures/data/globalLongShortAccountRatio",
		url.Values{"symbol": {symbol}, "period": {validPeriod(period)}, "limit": {"1"}}, &rows); err != nil {
		return LongShortRatio{}, err
	}
	if len(rows) == 0 {
		return LongShortRatio{}, fmt.Errorf("marketdata: no long/short ratio for %s", symbol)
	}
	row := rows[len(rows)-1] // most recent
	return LongShortRatio{
		Symbol:       symbol,
		Period:       validPeriod(period),
		Ratio:        parseOrZero(row.LongShortRatio),
		LongAccount:  parseOrZero(row.LongAccount),
		ShortAccount: parseOrZero(row.ShortAccount),
		At:           msToTime(row.Timestamp),
	}, nil
}

func (p *BinanceProvider) TakerFlow(ctx context.Context, symbol, period string) (TakerFlow, error) {
	var rows []struct {
		BuySellRatio string `json:"buySellRatio"`
		BuyVol       string `json:"buyVol"`
		SellVol      string `json:"sellVol"`
		Timestamp    int64  `json:"timestamp"`
	}
	if err := p.get(ctx, "/futures/data/takerlongshortRatio",
		url.Values{"symbol": {symbol}, "period": {validPeriod(period)}, "limit": {"1"}}, &rows); err != nil {
		return TakerFlow{}, err
	}
	if len(rows) == 0 {
		return TakerFlow{}, fmt.Errorf("marketdata: no taker flow for %s", symbol)
	}
	row := rows[len(rows)-1]
	return TakerFlow{
		Symbol:       symbol,
		Period:       validPeriod(period),
		BuySellRatio: parseOrZero(row.BuySellRatio),
		BuyVolume:    parseOrZero(row.BuyVol),
		SellVolume:   parseOrZero(row.SellVol),
		At:           msToTime(row.Timestamp),
	}, nil
}

// Closes returns the closing prices of the most recent `limit` klines for a
// symbol/interval, oldest first. The values are float64 because they feed
// advisory technical indicators (EMA/RSI/MACD), never order sizing. interval is
// a Binance kline interval ("1m","5m","15m","1h","4h","1d", ...).
func (p *BinanceProvider) Closes(ctx context.Context, symbol, interval string, limit int) ([]float64, error) {
	if limit <= 0 {
		limit = 200
	}
	params := url.Values{
		"symbol":   {symbol},
		"interval": {interval},
		"limit":    {strconv.Itoa(limit)},
	}
	var rows [][]any
	if err := p.get(ctx, "/fapi/v1/klines", params, &rows); err != nil {
		return nil, err
	}
	closes := make([]float64, 0, len(rows))
	for _, row := range rows {
		if len(row) < 5 {
			continue
		}
		// Kline close is index 4, encoded as a JSON string.
		text, ok := row[4].(string)
		if !ok {
			continue
		}
		value, err := strconv.ParseFloat(strings.TrimSpace(text), 64)
		if err != nil {
			return nil, fmt.Errorf("marketdata: parse kline close %q: %w", text, err)
		}
		closes = append(closes, value)
	}
	if len(closes) == 0 {
		return nil, fmt.Errorf("marketdata: no klines for %s %s", symbol, interval)
	}
	return closes, nil
}

func (p *BinanceProvider) get(ctx context.Context, path string, params url.Values, out any) error {
	endpoint := p.baseURL + path
	if len(params) > 0 {
		endpoint += "?" + params.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode >= 400 {
		return fmt.Errorf("marketdata: %s returned %d: %s", path, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("marketdata: decode %s: %w", path, err)
	}
	return nil
}

// validPeriod constrains the sampling window to Binance's accepted set,
// defaulting to 5m.
func validPeriod(period string) string {
	switch period {
	case "5m", "15m", "30m", "1h", "2h", "4h", "6h", "12h", "1d":
		return period
	default:
		return "5m"
	}
}

func parseOrZero(value string) decimal.Decimal {
	value = strings.TrimSpace(value)
	if value == "" {
		return decimal.Zero()
	}
	parsed, err := decimal.Parse(value)
	if err != nil {
		return decimal.Zero()
	}
	return parsed
}

func msToTime(ms int64) time.Time {
	if ms <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms).UTC()
}
