package binance

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"bottrade/internal/decimal"
	"bottrade/internal/domain"
	"bottrade/internal/orders"
)

type ExecutorConfig struct {
	APIKey               string
	APISecret            string
	BaseURL              string
	Testnet              bool
	RealTradingEnabled   bool
	RequestTimeout       time.Duration
	ExchangeInfoCacheTTL time.Duration
	HTTPClient           *http.Client
}

type Executor struct {
	cfg    ExecutorConfig
	client *http.Client
	logger *slog.Logger
	now    func() time.Time

	mu               sync.Mutex
	filters          map[string]SymbolFilters
	filtersRefreshed time.Time
}

type SymbolFilters struct {
	TickSize    decimal.Decimal
	StepSize    decimal.Decimal
	MinQty      decimal.Decimal
	MinNotional decimal.Decimal
}

func NewExecutor(cfg ExecutorConfig, logger *slog.Logger) *Executor {
	if cfg.RequestTimeout <= 0 {
		cfg.RequestTimeout = 10 * time.Second
	}
	if cfg.ExchangeInfoCacheTTL <= 0 {
		cfg.ExchangeInfoCacheTTL = 15 * time.Minute
	}
	if logger == nil {
		logger = slog.Default()
	}

	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: cfg.RequestTimeout}
	}

	return &Executor{
		cfg:     cfg,
		client:  client,
		logger:  logger,
		now:     time.Now,
		filters: make(map[string]SymbolFilters),
	}
}

func (e *Executor) Execute(ctx context.Context, confirmation orders.Confirmation) (orders.ExecutionResult, error) {
	if err := e.validateConfig(); err != nil {
		return orders.ExecutionResult{}, err
	}

	switch confirmation.Intent.Type {
	case domain.IntentOpen:
		return e.executeOpen(ctx, confirmation)
	case domain.IntentClose:
		return e.executeClose(ctx, confirmation)
	default:
		return orders.ExecutionResult{}, fmt.Errorf("unsupported Binance intent %q", confirmation.Intent.Type)
	}
}

func (e *Executor) Positions(ctx context.Context) ([]domain.Position, error) {
	if err := e.validateConfig(); err != nil {
		return nil, err
	}

	risks, err := e.positionRisk(ctx, "")
	if err != nil {
		return nil, err
	}

	positions := make([]domain.Position, 0, len(risks))
	for _, risk := range risks {
		position, ok, err := positionFromRisk(risk)
		if err != nil {
			return nil, err
		}
		if ok {
			positions = append(positions, position)
		}
	}

	return positions, nil
}

func (e *Executor) validateConfig() error {
	if strings.TrimSpace(e.cfg.APIKey) == "" || strings.TrimSpace(e.cfg.APISecret) == "" {
		return fmt.Errorf("BINANCE_API_KEY and BINANCE_API_SECRET are required for Binance execution")
	}
	if strings.TrimSpace(e.cfg.BaseURL) == "" {
		return fmt.Errorf("BINANCE_FUTURES_BASE_URL is required for Binance execution")
	}
	if e.cfg.RealTradingEnabled {
		return fmt.Errorf("live Binance execution is not wired in this phase; keep REAL_TRADING_ENABLED=false")
	}
	if !e.cfg.Testnet {
		return fmt.Errorf("refusing Binance execution when BINANCE_TESTNET=false")
	}

	return nil
}

func (e *Executor) executeOpen(ctx context.Context, confirmation orders.Confirmation) (orders.ExecutionResult, error) {
	intent := confirmation.Intent.Open
	if intent == nil || len(intent.TakeProfits) == 0 {
		return orders.ExecutionResult{}, fmt.Errorf("open intent is incomplete")
	}

	filters, err := e.symbolFilters(ctx, intent.Symbol)
	if err != nil {
		return orders.ExecutionResult{}, err
	}

	entry, err := intent.Entry.FloorToStep(filters.TickSize)
	if err != nil {
		return orders.ExecutionResult{}, fmt.Errorf("round entry price: %w", err)
	}
	stopLoss, err := intent.StopLoss.FloorToStep(filters.TickSize)
	if err != nil {
		return orders.ExecutionResult{}, fmt.Errorf("round stop loss: %w", err)
	}
	takeProfit, err := intent.TakeProfits[0].FloorToStep(filters.TickSize)
	if err != nil {
		return orders.ExecutionResult{}, fmt.Errorf("round take profit: %w", err)
	}

	quantity, err := e.openQuantity(intent, entry, filters)
	if err != nil {
		return orders.ExecutionResult{}, err
	}
	if err := validateQuantityAndNotional(quantity, entry, filters); err != nil {
		return orders.ExecutionResult{}, err
	}

	if err := e.changeMarginType(ctx, intent.Symbol, "ISOLATED"); err != nil {
		return orders.ExecutionResult{}, err
	}
	if err := e.changeLeverage(ctx, intent.Symbol, intent.Leverage); err != nil {
		return orders.ExecutionResult{}, err
	}

	entrySide, exitSide := orderSides(intent.Side)
	entryOrder, err := e.newOrder(ctx, url.Values{
		"symbol":           {intent.Symbol},
		"side":             {entrySide},
		"type":             {"LIMIT"},
		"timeInForce":      {"GTC"},
		"quantity":         {quantity.String()},
		"price":            {entry.String()},
		"newClientOrderId": {clientOrderID(confirmation.ID, "entry")},
	})
	if err != nil {
		return orders.ExecutionResult{}, fmt.Errorf("place entry order: %w", err)
	}

	// Conditional orders (STOP_MARKET / TAKE_PROFIT_MARKET) moved to the Algo
	// Order endpoint on 2025-12-09; /fapi/v1/order now rejects them with -4120.
	// Params differ from the classic endpoint: algoType, triggerPrice (not
	// stopPrice), clientAlgoId (not newClientOrderId).
	stopOrder, err := e.newAlgoOrder(ctx, url.Values{
		"symbol":        {intent.Symbol},
		"side":          {exitSide},
		"algoType":      {"CONDITIONAL"},
		"type":          {"STOP_MARKET"},
		"triggerPrice":  {stopLoss.String()},
		"closePosition": {"true"},
		"workingType":   {"MARK_PRICE"},
		"clientAlgoId":  {clientOrderID(confirmation.ID, "sl")},
	})
	if err != nil {
		e.rollbackOpen(ctx, intent.Symbol, confirmation.ID, false)
		return orders.ExecutionResult{}, fmt.Errorf("place stop loss order: %w", err)
	}

	takeProfitOrder, err := e.newAlgoOrder(ctx, url.Values{
		"symbol":        {intent.Symbol},
		"side":          {exitSide},
		"algoType":      {"CONDITIONAL"},
		"type":          {"TAKE_PROFIT_MARKET"},
		"triggerPrice":  {takeProfit.String()},
		"closePosition": {"true"},
		"workingType":   {"MARK_PRICE"},
		"clientAlgoId":  {clientOrderID(confirmation.ID, "tp")},
	})
	if err != nil {
		e.rollbackOpen(ctx, intent.Symbol, confirmation.ID, true)
		return orders.ExecutionResult{}, fmt.Errorf("place take profit order: %w", err)
	}

	return orders.ExecutionResult{
		Mode:          e.mode(),
		ClientOrderID: entryOrder.ClientOrderID,
		Message: fmt.Sprintf(
			"%s order submitted.\n\n%s\n\nRounded quantity: %s\nEntry order: %s #%d\nSL order: %s #%d\nTP order: %s #%d",
			strings.ToUpper(e.mode()),
			orders.Summary(confirmation.Intent),
			quantity.String(),
			entryOrder.ClientOrderID,
			entryOrder.OrderID,
			stopOrder.ClientAlgoID,
			stopOrder.AlgoID,
			takeProfitOrder.ClientAlgoID,
			takeProfitOrder.AlgoID,
		),
	}, nil
}

func (e *Executor) executeClose(ctx context.Context, confirmation orders.Confirmation) (orders.ExecutionResult, error) {
	intent := confirmation.Intent.Close
	if intent == nil {
		return orders.ExecutionResult{}, fmt.Errorf("close intent is incomplete")
	}

	positions, err := e.positionRisk(ctx, intent.Symbol)
	if err != nil {
		return orders.ExecutionResult{}, err
	}

	responses := make([]orderResponse, 0, len(positions))
	for _, position := range positions {
		if intent.Symbol != "" && position.Symbol != intent.Symbol {
			continue
		}

		amount, err := decimal.Parse(position.PositionAmt)
		if err != nil {
			return orders.ExecutionResult{}, fmt.Errorf("parse position amount for %s: %w", position.Symbol, err)
		}
		if amount.IsZero() {
			continue
		}

		filters, err := e.symbolFilters(ctx, position.Symbol)
		if err != nil {
			return orders.ExecutionResult{}, err
		}

		quantity := amount.Abs()
		if !intent.ResolvedPercent.Equal(decimal.NewFromInt(100)) {
			quantity, err = quantity.Mul(intent.ResolvedPercent).QuoFloor(decimal.NewFromInt(100), 16)
			if err != nil {
				return orders.ExecutionResult{}, err
			}
		}
		quantity, err = quantity.FloorToStep(filters.StepSize)
		if err != nil {
			return orders.ExecutionResult{}, err
		}
		if quantity.IsZero() || quantity.Cmp(filters.MinQty) < 0 {
			return orders.ExecutionResult{}, fmt.Errorf("close quantity %s is below min qty %s for %s", quantity.String(), filters.MinQty.String(), position.Symbol)
		}

		side := "SELL"
		if amount.Cmp(decimal.Zero()) < 0 {
			side = "BUY"
		}

		response, err := e.newOrder(ctx, url.Values{
			"symbol":           {position.Symbol},
			"side":             {side},
			"type":             {"MARKET"},
			"quantity":         {quantity.String()},
			"reduceOnly":       {"true"},
			"newClientOrderId": {clientOrderID(confirmation.ID, "close")},
		})
		if err != nil {
			return orders.ExecutionResult{}, fmt.Errorf("place close order for %s: %w", position.Symbol, err)
		}
		responses = append(responses, response)
	}

	if len(responses) == 0 {
		return orders.ExecutionResult{}, fmt.Errorf("no open position found to close")
	}

	lines := make([]string, 0, len(responses))
	for _, response := range responses {
		lines = append(lines, fmt.Sprintf("%s #%d", response.ClientOrderID, response.OrderID))
	}

	return orders.ExecutionResult{
		Mode:          e.mode(),
		ClientOrderID: responses[0].ClientOrderID,
		Message:       strings.ToUpper(e.mode()) + " close submitted.\n\n" + orders.Summary(confirmation.Intent) + "\n\nOrders:\n" + strings.Join(lines, "\n"),
	}, nil
}

func (e *Executor) openQuantity(intent *domain.OpenIntent, entry decimal.Decimal, filters SymbolFilters) (decimal.Decimal, error) {
	var quantity decimal.Decimal
	var err error

	switch intent.Size.Kind {
	case domain.SizeQty:
		quantity = intent.Size.Amount
	case domain.SizeUSDT:
		quantity, err = intent.Size.Amount.QuoFloor(entry, 16)
		if err != nil {
			return decimal.Zero(), fmt.Errorf("calculate quantity from USDT size: %w", err)
		}
	default:
		return decimal.Zero(), fmt.Errorf("open size must be size USDT or qty")
	}

	quantity, err = quantity.FloorToStep(filters.StepSize)
	if err != nil {
		return decimal.Zero(), fmt.Errorf("round quantity: %w", err)
	}

	return quantity, nil
}

func validateQuantityAndNotional(quantity decimal.Decimal, price decimal.Decimal, filters SymbolFilters) error {
	if quantity.IsZero() || quantity.Cmp(filters.MinQty) < 0 {
		return fmt.Errorf("quantity %s is below min qty %s", quantity.String(), filters.MinQty.String())
	}

	notional := quantity.Mul(price)
	if filters.MinNotional.IsPositive() && notional.Cmp(filters.MinNotional) < 0 {
		return fmt.Errorf("notional %s is below min notional %s", notional.String(), filters.MinNotional.String())
	}

	return nil
}

func orderSides(side domain.Side) (string, string) {
	if side == domain.SideShort {
		return "SELL", "BUY"
	}
	return "BUY", "SELL"
}

func positionFromRisk(risk positionRisk) (domain.Position, bool, error) {
	amount, err := decimal.Parse(risk.PositionAmt)
	if err != nil {
		return domain.Position{}, false, fmt.Errorf("parse position amount for %s: %w", risk.Symbol, err)
	}
	if amount.IsZero() {
		return domain.Position{}, false, nil
	}

	entryPrice, err := decimal.Parse(defaultDecimalString(risk.EntryPrice))
	if err != nil {
		return domain.Position{}, false, fmt.Errorf("parse entry price for %s: %w", risk.Symbol, err)
	}
	markPrice, err := decimal.Parse(defaultDecimalString(risk.MarkPrice))
	if err != nil {
		return domain.Position{}, false, fmt.Errorf("parse mark price for %s: %w", risk.Symbol, err)
	}
	unrealizedProfit, err := decimal.Parse(defaultDecimalString(risk.UnrealizedProfit))
	if err != nil {
		return domain.Position{}, false, fmt.Errorf("parse unrealized profit for %s: %w", risk.Symbol, err)
	}

	leverage := 0
	leverageText := strings.TrimSpace(risk.Leverage)
	if leverageText != "" {
		leverage, err = strconv.Atoi(leverageText)
		if err != nil {
			return domain.Position{}, false, fmt.Errorf("parse leverage for %s: %w", risk.Symbol, err)
		}
	}

	side := domain.PositionSideLong
	if amount.Cmp(decimal.Zero()) < 0 || strings.EqualFold(risk.PositionSide, "SHORT") {
		side = domain.PositionSideShort
	}

	return domain.Position{
		Symbol:           risk.Symbol,
		Side:             side,
		Amount:           amount.Abs(),
		EntryPrice:       entryPrice,
		MarkPrice:        markPrice,
		UnrealizedProfit: unrealizedProfit,
		Leverage:         leverage,
		MarginType:       strings.ToLower(strings.TrimSpace(risk.MarginType)),
	}, true, nil
}

func defaultDecimalString(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "0"
	}
	return value
}

func (e *Executor) changeMarginType(ctx context.Context, symbol string, marginType string) error {
	var response map[string]any
	err := e.signedRequest(ctx, http.MethodPost, "/fapi/v1/marginType", url.Values{
		"symbol":     {symbol},
		"marginType": {marginType},
	}, &response)
	var apiErr apiError
	if err != nil && apiErrorAs(err, &apiErr) && apiErr.Code == -4046 {
		return nil
	}
	return err
}

func (e *Executor) changeLeverage(ctx context.Context, symbol string, leverage int) error {
	var response map[string]any
	return e.signedRequest(ctx, http.MethodPost, "/fapi/v1/leverage", url.Values{
		"symbol":   {symbol},
		"leverage": {strconv.Itoa(leverage)},
	}, &response)
}

func (e *Executor) newOrder(ctx context.Context, params url.Values) (orderResponse, error) {
	var response orderResponse
	if err := e.signedRequest(ctx, http.MethodPost, "/fapi/v1/order", params, &response); err != nil {
		return orderResponse{}, err
	}
	return response, nil
}

func (e *Executor) newAlgoOrder(ctx context.Context, params url.Values) (algoOrderResponse, error) {
	var response algoOrderResponse
	if err := e.signedRequest(ctx, http.MethodPost, "/fapi/v1/algoOrder", params, &response); err != nil {
		return algoOrderResponse{}, err
	}
	return response, nil
}

func (e *Executor) cancelOrder(ctx context.Context, symbol string, clientOrderID string) error {
	var response map[string]any
	return e.signedRequest(ctx, http.MethodDelete, "/fapi/v1/order", url.Values{
		"symbol":            {symbol},
		"origClientOrderId": {clientOrderID},
	}, &response)
}

func (e *Executor) cancelAlgoOrder(ctx context.Context, clientAlgoID string) error {
	var response map[string]any
	return e.signedRequest(ctx, http.MethodDelete, "/fapi/v1/algoOrder", url.Values{
		"clientAlgoId": {clientAlgoID},
	}, &response)
}

// flattenPosition closes any open position on the symbol with a reduce-only
// market order. Used during open rollback so a filled entry is never left
// without protection.
func (e *Executor) flattenPosition(ctx context.Context, symbol string, clientOrderID string) error {
	positions, err := e.positionRisk(ctx, symbol)
	if err != nil {
		return err
	}
	for _, position := range positions {
		if position.Symbol != symbol {
			continue
		}
		amount, err := decimal.Parse(position.PositionAmt)
		if err != nil || amount.IsZero() {
			continue
		}
		filters, err := e.symbolFilters(ctx, symbol)
		if err != nil {
			return err
		}
		quantity, err := amount.Abs().FloorToStep(filters.StepSize)
		if err != nil || quantity.IsZero() {
			continue
		}
		side := "SELL"
		if amount.Cmp(decimal.Zero()) < 0 {
			side = "BUY"
		}
		if _, err := e.newOrder(ctx, url.Values{
			"symbol":           {symbol},
			"side":             {side},
			"type":             {"MARKET"},
			"quantity":         {quantity.String()},
			"reduceOnly":       {"true"},
			"newClientOrderId": {clientOrderID},
		}); err != nil {
			return err
		}
	}
	return nil
}

// rollbackOpen best-effort undoes a partially-placed open after a stop-loss or
// take-profit leg fails: cancel the stop-loss algo order (if it was placed),
// cancel the resting entry order, and flatten any position the entry already
// opened. Rollback errors are logged, not returned — the caller returns the
// original placement failure so the user knows the open did not complete.
func (e *Executor) rollbackOpen(ctx context.Context, symbol string, confirmationID string, stopPlaced bool) {
	if stopPlaced {
		if err := e.cancelAlgoOrder(ctx, clientOrderID(confirmationID, "sl")); err != nil {
			e.logger.Warn("rollback: cancel stop loss failed", "symbol", symbol, "error", err)
		}
	}
	if err := e.cancelOrder(ctx, symbol, clientOrderID(confirmationID, "entry")); err != nil {
		e.logger.Warn("rollback: cancel entry failed", "symbol", symbol, "error", err)
	}
	if err := e.flattenPosition(ctx, symbol, clientOrderID(confirmationID, "rb")); err != nil {
		e.logger.Warn("rollback: flatten position failed", "symbol", symbol, "error", err)
	}
}

func (e *Executor) positionRisk(ctx context.Context, symbol string) ([]positionRisk, error) {
	params := url.Values{}
	if symbol != "" {
		params.Set("symbol", symbol)
	}

	var response []positionRisk
	if err := e.signedRequest(ctx, http.MethodGet, "/fapi/v3/positionRisk", params, &response); err != nil {
		return nil, err
	}
	return response, nil
}

func (e *Executor) symbolFilters(ctx context.Context, symbol string) (SymbolFilters, error) {
	e.mu.Lock()
	if filters, ok := e.filters[symbol]; ok && e.now().Sub(e.filtersRefreshed) < e.cfg.ExchangeInfoCacheTTL {
		e.mu.Unlock()
		return filters, nil
	}
	e.mu.Unlock()

	var response exchangeInfoResponse
	if err := e.publicRequest(ctx, http.MethodGet, "/fapi/v1/exchangeInfo", nil, &response); err != nil {
		return SymbolFilters{}, err
	}

	filters := make(map[string]SymbolFilters, len(response.Symbols))
	for _, symbolInfo := range response.Symbols {
		parsed, err := parseSymbolFilters(symbolInfo)
		if err != nil {
			continue
		}
		filters[symbolInfo.Symbol] = parsed
	}

	e.mu.Lock()
	e.filters = filters
	e.filtersRefreshed = e.now()
	result, ok := e.filters[symbol]
	e.mu.Unlock()

	if !ok {
		return SymbolFilters{}, fmt.Errorf("symbol filters unavailable for %s", symbol)
	}

	return result, nil
}

func parseSymbolFilters(symbol symbolInfo) (SymbolFilters, error) {
	result := SymbolFilters{}

	for _, filter := range symbol.Filters {
		switch filter.FilterType {
		case "PRICE_FILTER":
			value, err := decimal.Parse(filter.TickSize)
			if err != nil {
				return SymbolFilters{}, err
			}
			result.TickSize = value
		case "LOT_SIZE":
			stepSize, err := decimal.Parse(filter.StepSize)
			if err != nil {
				return SymbolFilters{}, err
			}
			minQty, err := decimal.Parse(filter.MinQty)
			if err != nil {
				return SymbolFilters{}, err
			}
			result.StepSize = stepSize
			result.MinQty = minQty
		case "MIN_NOTIONAL":
			value, err := decimal.Parse(filter.Notional)
			if err != nil {
				return SymbolFilters{}, err
			}
			result.MinNotional = value
		}
	}

	if !result.TickSize.IsPositive() {
		return SymbolFilters{}, fmt.Errorf("missing PRICE_FILTER for %s", symbol.Symbol)
	}
	if !result.StepSize.IsPositive() {
		return SymbolFilters{}, fmt.Errorf("missing LOT_SIZE for %s", symbol.Symbol)
	}
	if !result.MinQty.IsPositive() {
		return SymbolFilters{}, fmt.Errorf("missing min qty for %s", symbol.Symbol)
	}

	return result, nil
}

func (e *Executor) publicRequest(ctx context.Context, method string, path string, params url.Values, out any) error {
	endpoint := strings.TrimRight(e.cfg.BaseURL, "/") + path
	if len(params) > 0 {
		endpoint += "?" + params.Encode()
	}
	return e.do(ctx, method, endpoint, false, out)
}

func (e *Executor) signedRequest(ctx context.Context, method string, path string, params url.Values, out any) error {
	if params == nil {
		params = url.Values{}
	}
	params.Set("timestamp", strconv.FormatInt(e.now().UnixMilli(), 10))
	params.Set("recvWindow", "5000")

	query := params.Encode()
	signature := e.sign(query)
	endpoint := strings.TrimRight(e.cfg.BaseURL, "/") + path + "?" + query + "&signature=" + signature

	return e.do(ctx, method, endpoint, true, out)
}

func (e *Executor) do(ctx context.Context, method string, endpoint string, signed bool, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, endpoint, nil)
	if err != nil {
		return err
	}
	if signed {
		req.Header.Set("X-MBX-APIKEY", e.cfg.APIKey)
	}

	resp, err := e.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode >= 400 {
		apiErr := apiError{StatusCode: resp.StatusCode}
		_ = json.Unmarshal(body, &apiErr)
		if apiErr.Message == "" {
			apiErr.Message = string(body)
		}
		return apiErr
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("decode Binance response: %w", err)
	}

	return nil
}

func (e *Executor) sign(payload string) string {
	mac := hmac.New(sha256.New, []byte(e.cfg.APISecret))
	_, _ = mac.Write([]byte(payload))
	return hex.EncodeToString(mac.Sum(nil))
}

func (e *Executor) mode() string {
	if e.cfg.Testnet {
		return "binance_testnet"
	}
	return "binance_live"
}

func clientOrderID(confirmationID string, leg string) string {
	if len(confirmationID) > 18 {
		confirmationID = confirmationID[:18]
	}
	return "tb_" + confirmationID + "_" + leg
}

type exchangeInfoResponse struct {
	Symbols []symbolInfo `json:"symbols"`
}

type symbolInfo struct {
	Symbol  string           `json:"symbol"`
	Filters []exchangeFilter `json:"filters"`
}

type exchangeFilter struct {
	FilterType string `json:"filterType"`
	TickSize   string `json:"tickSize"`
	StepSize   string `json:"stepSize"`
	MinQty     string `json:"minQty"`
	Notional   string `json:"notional"`
}

type orderResponse struct {
	ClientOrderID string `json:"clientOrderId"`
	OrderID       int64  `json:"orderId"`
	Symbol        string `json:"symbol"`
	Status        string `json:"status"`
	Type          string `json:"type"`
}

// algoOrderResponse is the response shape of /fapi/v1/algoOrder, which uses
// algoId/clientAlgoId/algoStatus rather than the classic order fields.
type algoOrderResponse struct {
	ClientAlgoID string `json:"clientAlgoId"`
	AlgoID       int64  `json:"algoId"`
	Symbol       string `json:"symbol"`
	AlgoStatus   string `json:"algoStatus"`
	Type         string `json:"type"`
}

type positionRisk struct {
	Symbol           string `json:"symbol"`
	PositionAmt      string `json:"positionAmt"`
	EntryPrice       string `json:"entryPrice"`
	MarkPrice        string `json:"markPrice"`
	UnrealizedProfit string `json:"unRealizedProfit"`
	Leverage         string `json:"leverage"`
	MarginType       string `json:"marginType"`
	PositionSide     string `json:"positionSide"`
}

type apiError struct {
	StatusCode int
	Code       int    `json:"code"`
	Message    string `json:"msg"`
}

func (e apiError) Error() string {
	if e.Code != 0 {
		return fmt.Sprintf("binance api error %d: %s", e.Code, e.Message)
	}
	return fmt.Sprintf("binance http error %d: %s", e.StatusCode, e.Message)
}

func apiErrorAs(err error, target *apiError) bool {
	if err == nil {
		return false
	}
	if value, ok := err.(apiError); ok {
		*target = value
		return true
	}
	return false
}
