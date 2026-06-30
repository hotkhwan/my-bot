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

const maxKlineCandlesPerRequest = 1000

// BinanceProvider reads Binance Futures' free public market-data endpoints. No
// API key is required. It is safe for concurrent use.
type BinanceProvider struct {
	baseURL string
	client  *http.Client

	mu         sync.Mutex
	symbols    []string
	symbolsAt  time.Time
	symbolsTTL time.Duration

	tickers    map[string]Ticker
	tickersAt  time.Time
	tickersTTL time.Duration
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
	return &BinanceProvider{baseURL: strings.TrimRight(baseURL, "/"), client: client, symbolsTTL: time.Hour, tickersTTL: 5 * time.Second}
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

// Tickers returns the last price and 24h change percent for the requested
// symbols. It fetches the full Binance Futures 24h ticker list in one request
// and caches it briefly, so quoting a handful of favourites costs at most one
// upstream call every few seconds. An empty symbols slice returns every ticker.
// Symbols with no match are simply omitted from the result.
func (p *BinanceProvider) Tickers(ctx context.Context, symbols []string) (map[string]Ticker, error) {
	all, err := p.allTickers(ctx)
	if err != nil {
		return nil, err
	}
	if len(symbols) == 0 {
		return all, nil
	}
	out := make(map[string]Ticker, len(symbols))
	for _, sym := range symbols {
		key := strings.ToUpper(strings.TrimSpace(sym))
		if t, ok := all[key]; ok {
			out[key] = t
		}
	}
	return out, nil
}

// allTickers returns every futures ticker keyed by symbol, served from a short
// TTL cache shared across callers.
func (p *BinanceProvider) allTickers(ctx context.Context) (map[string]Ticker, error) {
	p.mu.Lock()
	if p.tickers != nil && time.Since(p.tickersAt) < p.tickersTTL {
		cached := p.tickers
		p.mu.Unlock()
		return cached, nil
	}
	p.mu.Unlock()

	var rows []struct {
		Symbol             string `json:"symbol"`
		LastPrice          string `json:"lastPrice"`
		PriceChangePercent string `json:"priceChangePercent"`
	}
	if err := p.get(ctx, "/fapi/v1/ticker/24hr", nil, &rows); err != nil {
		return nil, err
	}
	now := time.Now()
	tickers := make(map[string]Ticker, len(rows))
	for _, r := range rows {
		tickers[r.Symbol] = Ticker{
			Symbol:         r.Symbol,
			LastPrice:      parseOrZero(r.LastPrice),
			PriceChangePct: parseOrZero(r.PriceChangePercent),
			At:             now,
		}
	}

	p.mu.Lock()
	p.tickers = tickers
	p.tickersAt = now
	p.mu.Unlock()
	return tickers, nil
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

// Candles returns the most recent `limit` OHLC klines for a symbol/interval,
// oldest first. It backs paper-trading simulation, which resolves wins/losses
// from real intrabar highs and lows (whether stop-loss or take-profit was hit
// first), so it needs full candles, not just closes.
func (p *BinanceProvider) Candles(ctx context.Context, symbol, interval string, limit int) ([]Candle, error) {
	if limit <= 0 {
		limit = 200
	}
	if limit > maxKlineCandlesPerRequest {
		return p.candlesPaged(ctx, symbol, interval, limit)
	}
	return p.candlePage(ctx, symbol, interval, limit, 0)
}

func (p *BinanceProvider) candlesPaged(ctx context.Context, symbol, interval string, limit int) ([]Candle, error) {
	remaining := limit
	var endTime int64
	var out []Candle
	for remaining > 0 {
		pageLimit := remaining
		if pageLimit > maxKlineCandlesPerRequest {
			pageLimit = maxKlineCandlesPerRequest
		}
		page, err := p.candlePage(ctx, symbol, interval, pageLimit, endTime)
		if err != nil {
			return nil, err
		}
		if len(page) == 0 {
			break
		}
		nextEnd := page[0].OpenTime.UnixMilli() - 1
		if endTime > 0 && nextEnd >= endTime {
			break
		}
		out = append(page, out...)
		endTime = nextEnd
		remaining -= len(page)
		if len(page) < pageLimit {
			break
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("marketdata: no klines for %s %s", symbol, interval)
	}
	if len(out) > limit {
		out = out[len(out)-limit:]
	}
	return out, nil
}

func (p *BinanceProvider) candlePage(ctx context.Context, symbol, interval string, limit int, endTime int64) ([]Candle, error) {
	params := url.Values{
		"symbol":   {symbol},
		"interval": {interval},
		"limit":    {strconv.Itoa(limit)},
	}
	if endTime > 0 {
		params.Set("endTime", strconv.FormatInt(endTime, 10))
	}
	var rows [][]any
	if err := p.get(ctx, "/fapi/v1/klines", params, &rows); err != nil {
		return nil, err
	}
	candles := make([]Candle, 0, len(rows))
	for _, row := range rows {
		if len(row) < 6 {
			continue
		}
		openTime, _ := row[0].(float64) // kline open time, ms since epoch
		o, err1 := klineFloat(row[1])
		h, err2 := klineFloat(row[2])
		l, err3 := klineFloat(row[3])
		c, err4 := klineFloat(row[4])
		v, err5 := klineFloat(row[5])
		if err1 != nil || err2 != nil || err3 != nil || err4 != nil || err5 != nil {
			continue
		}
		candles = append(candles, Candle{
			OpenTime: msToTime(int64(openTime)),
			Open:     o,
			High:     h,
			Low:      l,
			Close:    c,
			Volume:   v,
		})
	}
	if len(candles) == 0 {
		return nil, fmt.Errorf("marketdata: no klines for %s %s", symbol, interval)
	}
	return candles, nil
}

// klineFloat parses a Binance kline numeric field, which arrives as a JSON
// string.
func klineFloat(v any) (float64, error) {
	text, ok := v.(string)
	if !ok {
		return 0, fmt.Errorf("marketdata: kline field %v is not a string", v)
	}
	return strconv.ParseFloat(strings.TrimSpace(text), 64)
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
