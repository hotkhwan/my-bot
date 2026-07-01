package signals

import (
	"fmt"
	"strings"
	"time"

	"bottrade/internal/decimal"
	"bottrade/internal/domain"
	"bottrade/internal/parser"
)

type MarketSignal struct {
	Source     string            `json:"source"`
	Symbol     string            `json:"symbol"`
	Interval   string            `json:"interval"`
	Price      string            `json:"price"`
	Strategy   string            `json:"strategy"`
	ActionHint string            `json:"action_hint"`
	SideHint   string            `json:"side_hint"`
	Message    string            `json:"message"`
	Indicators map[string]string `json:"indicators"`
	ReceivedAt time.Time         `json:"received_at"`
}

type DecisionAction string

const (
	ActionHold  DecisionAction = "hold"
	ActionOpen  DecisionAction = "open"
	ActionClose DecisionAction = "close"
)

type Decision struct {
	Action            DecisionAction `json:"action"`
	Symbol            string         `json:"symbol"`
	Strategy          string         `json:"strategy,omitempty"`
	Side              string         `json:"side,omitempty"`
	Leverage          int            `json:"leverage,omitempty"`
	Entry             string         `json:"entry,omitempty"`
	StopLoss          string         `json:"stop_loss,omitempty"`
	TakeProfit        string         `json:"take_profit,omitempty"`
	SizeUSDT          string         `json:"size_usdt,omitempty"`
	Qty               string         `json:"qty,omitempty"`
	ClosePercent      string         `json:"close_percent,omitempty"`
	ConfidencePercent int            `json:"confidence_percent"`
	Reason            string         `json:"reason"`
	// Models lists the AI models that produced this decision. Empty for a single
	// advisor; the ensemble fills it so the journal can attribute per-model
	// performance. Not parsed from an advisor's JSON response.
	Models []string `json:"models,omitempty"`
}

func (s MarketSignal) Sanitized() MarketSignal {
	s.Source = strings.TrimSpace(s.Source)
	if s.Source == "" {
		s.Source = "tradingview"
	}
	s.Symbol = normalizeSymbol(s.Symbol)
	s.Interval = strings.TrimSpace(s.Interval)
	s.Price = strings.TrimSpace(s.Price)
	s.Strategy = strings.TrimSpace(s.Strategy)
	s.ActionHint = strings.ToLower(strings.TrimSpace(s.ActionHint))
	s.SideHint = strings.ToLower(strings.TrimSpace(s.SideHint))
	s.Message = strings.TrimSpace(s.Message)
	if s.Indicators == nil {
		s.Indicators = map[string]string{}
	}
	if s.ReceivedAt.IsZero() {
		s.ReceivedAt = time.Now()
	}
	return s
}

func (s MarketSignal) Validate() error {
	if strings.TrimSpace(s.Symbol) == "" {
		return fmt.Errorf("symbol is required")
	}
	if strings.TrimSpace(s.Price) != "" {
		price, err := decimal.Parse(s.Price)
		if err != nil || !price.IsPositive() {
			return fmt.Errorf("price must be a positive decimal")
		}
	}
	return nil
}

func DecisionToIntent(decision Decision, maxLeverage int) (domain.Intent, error) {
	action := DecisionAction(strings.ToLower(strings.TrimSpace(string(decision.Action))))
	switch action {
	case ActionHold:
		return domain.Intent{Type: domain.IntentStatus, RawText: "status"}, nil
	case ActionOpen:
		return openDecisionToIntent(decision, maxLeverage)
	case ActionClose:
		return closeDecisionToIntent(decision)
	default:
		return domain.Intent{}, fmt.Errorf("decision action must be hold, open, or close")
	}
}

func openDecisionToIntent(decision Decision, maxLeverage int) (domain.Intent, error) {
	side := strings.ToLower(strings.TrimSpace(decision.Side))
	if side != "long" && side != "short" {
		return domain.Intent{}, fmt.Errorf("open decision side must be long or short")
	}
	if decision.Leverage <= 0 {
		return domain.Intent{}, fmt.Errorf("open decision leverage must be greater than zero")
	}

	size := ""
	switch {
	case strings.TrimSpace(decision.SizeUSDT) != "":
		size = "size " + strings.TrimSpace(decision.SizeUSDT) + "usdt"
	case strings.TrimSpace(decision.Qty) != "":
		size = "qty " + strings.TrimSpace(decision.Qty)
	default:
		return domain.Intent{}, fmt.Errorf("open decision must include size_usdt or qty")
	}

	command := fmt.Sprintf(
		"%s %s %dx entry %s sl %s tp %s %s",
		side,
		decision.Symbol,
		decision.Leverage,
		strings.TrimSpace(decision.Entry),
		strings.TrimSpace(decision.StopLoss),
		strings.TrimSpace(decision.TakeProfit),
		size,
	)
	intent, err := parser.Parse(command, parser.Options{MaxLeverage: maxLeverage})
	if err != nil {
		return domain.Intent{}, err
	}
	if intent.Open != nil {
		intent.Open.Strategy = strings.TrimSpace(decision.Strategy)
		intent.Open.Models = append([]string(nil), decision.Models...)
		intent.Open.Reason = strings.TrimSpace(decision.Reason)
		intent.Open.Confidence = decision.ConfidencePercent
	}
	return intent, nil
}

func closeDecisionToIntent(decision Decision) (domain.Intent, error) {
	if strings.EqualFold(strings.TrimSpace(decision.Symbol), "all") {
		return parser.Parse("close all", parser.Options{})
	}

	command := "close " + strings.TrimSpace(decision.Symbol)
	if strings.TrimSpace(decision.ClosePercent) != "" {
		command += " " + strings.TrimSpace(decision.ClosePercent) + "%"
	}
	return parser.Parse(command, parser.Options{})
}

func normalizeSymbol(symbol string) string {
	value := strings.ToUpper(strings.TrimSpace(symbol))
	value = strings.ReplaceAll(value, "/", "")
	if value != "" && !strings.HasSuffix(value, "USDT") {
		value += "USDT"
	}
	return value
}
