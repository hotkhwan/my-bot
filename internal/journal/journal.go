// Package journal records the outcome of every trade and aggregates win-rate,
// PnL, and expectancy by strategy and by AI model. It is the measurement
// foundation of the AI trading system: nothing else should be trusted until the
// journal shows a real, fee-adjusted edge. See AI_TRADING_SYSTEM.md.
package journal

import (
	"context"
	"errors"
	"strings"
	"time"

	"bottrade/internal/decimal"
)

// Outcome is the resolved result of a trade.
type Outcome string

const (
	OutcomeOpen      Outcome = "open"
	OutcomeWin       Outcome = "win"
	OutcomeLoss      Outcome = "loss"
	OutcomeBreakeven Outcome = "breakeven"
)

func (o Outcome) valid() bool {
	switch o {
	case OutcomeOpen, OutcomeWin, OutcomeLoss, OutcomeBreakeven:
		return true
	default:
		return false
	}
}

// closed reports whether the trade has a final result (not still open).
func (o Outcome) closed() bool {
	return o == OutcomeWin || o == OutcomeLoss || o == OutcomeBreakeven
}

// Trade is one journaled position. Monetary fields use the exact decimal type;
// PnLUSDT is the realized profit/loss once the trade is closed.
type Trade struct {
	ID             string          `bson:"_id" json:"id"`
	UserID         int64           `bson:"user_id" json:"user_id"`
	CampaignID     string          `bson:"campaign_id,omitempty" json:"campaign_id,omitempty"`
	ConfirmationID string          `bson:"confirmation_id,omitempty" json:"confirmation_id,omitempty"`
	Symbol         string          `bson:"symbol" json:"symbol"`
	Side           string          `bson:"side" json:"side"`
	Strategy       string          `bson:"strategy,omitempty" json:"strategy,omitempty"`
	Models         []string        `bson:"models,omitempty" json:"models,omitempty"`
	Leverage       int             `bson:"leverage" json:"leverage"`
	Mode           string          `bson:"mode" json:"mode"`
	Entry          decimal.Decimal `bson:"entry" json:"entry"`
	Exit           decimal.Decimal `bson:"exit" json:"exit"`
	StopLoss       decimal.Decimal `bson:"stop_loss" json:"stop_loss"`
	TakeProfit     decimal.Decimal `bson:"take_profit" json:"take_profit"`
	SizeUSDT       decimal.Decimal `bson:"size_usdt" json:"size_usdt"`
	Quantity       decimal.Decimal `bson:"quantity" json:"quantity"`
	PnLUSDT        decimal.Decimal `bson:"pnl_usdt" json:"pnl_usdt"`
	Outcome        Outcome         `bson:"outcome" json:"outcome"`
	OpenedAt       time.Time       `bson:"opened_at" json:"opened_at"`
	ClosedAt       time.Time       `bson:"closed_at,omitempty" json:"closed_at,omitempty"`
}

// Filter narrows which trades a report or list covers. Zero-value fields are
// ignored, so an empty Filter matches everything.
type Filter struct {
	UserID     int64
	CampaignID string
	Symbol     string
	Strategy   string
	Mode       string
	ClosedOnly bool
}

func (f Filter) matches(t Trade) bool {
	if f.UserID != 0 && t.UserID != f.UserID {
		return false
	}
	if f.CampaignID != "" && t.CampaignID != f.CampaignID {
		return false
	}
	if f.Symbol != "" && !strings.EqualFold(t.Symbol, f.Symbol) {
		return false
	}
	if f.Strategy != "" && t.Strategy != f.Strategy {
		return false
	}
	if f.Mode != "" && t.Mode != f.Mode {
		return false
	}
	if f.ClosedOnly && !t.Outcome.closed() {
		return false
	}
	return true
}

// Repository persists journaled trades. Save upserts by Trade.ID so a trade can
// be recorded when opened and updated when it closes.
type Repository interface {
	Save(ctx context.Context, trade Trade) error
	List(ctx context.Context, filter Filter) ([]Trade, error)
}

// Service validates and records trades and produces aggregated reports.
type Service struct {
	repo Repository
}

// NewService wires a repository.
func NewService(repo Repository) (*Service, error) {
	if repo == nil {
		return nil, errors.New("journal: repository is required")
	}
	return &Service{repo: repo}, nil
}

// Record validates and upserts a trade.
func (s *Service) Record(ctx context.Context, trade Trade) error {
	if strings.TrimSpace(trade.ID) == "" {
		return errors.New("journal: trade id is required")
	}
	if trade.UserID <= 0 {
		return errors.New("journal: user id must be positive")
	}
	if strings.TrimSpace(trade.Symbol) == "" {
		return errors.New("journal: symbol is required")
	}
	if trade.Outcome == "" {
		trade.Outcome = OutcomeOpen
	}
	if !trade.Outcome.valid() {
		return errors.New("journal: invalid outcome")
	}
	return s.repo.Save(ctx, trade)
}

// Report aggregates the trades matching filter into win-rate / PnL statistics.
func (s *Service) Report(ctx context.Context, filter Filter) (Report, error) {
	trades, err := s.repo.List(ctx, filter)
	if err != nil {
		return Report{}, err
	}
	return aggregate(trades), nil
}
