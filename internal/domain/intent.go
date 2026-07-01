package domain

import "bottrade/internal/decimal"

type IntentType string

const (
	IntentUnknown    IntentType = ""
	IntentOpen       IntentType = "open"
	IntentClose      IntentType = "close"
	IntentBreakeven  IntentType = "breakeven"
	IntentTrail      IntentType = "trail"
	IntentAdd        IntentType = "add"
	IntentStatus     IntentType = "status"
	IntentPlanStatus IntentType = "plan_status"
)

type Side string

const (
	SideLong  Side = "long"
	SideShort Side = "short"
)

type SizeKind string

const (
	SizeUnknown SizeKind = ""
	SizeUSDT    SizeKind = "usdt"
	SizeQty     SizeKind = "qty"
)

type OrderSize struct {
	Kind   SizeKind
	Amount decimal.Decimal
}

type Intent struct {
	Type       IntentType
	RawText    string
	Open       *OpenIntent
	Close      *CloseIntent
	Breakeven  *BreakevenIntent
	Trail      *TrailIntent
	Add        *AddIntent
	PlanStatus *PlanStatusIntent
}

type OpenIntent struct {
	Symbol      string
	Side        Side
	Leverage    int
	Entry       decimal.Decimal
	StopLoss    decimal.Decimal
	TakeProfits []decimal.Decimal
	Size        OrderSize
	PlanID      string
	Strategy    string
	Models      []string
	Reason      string
	Confidence  int
	CampaignID  string
}

type CloseIntent struct {
	Symbol              string
	All                 bool
	Percent             decimal.Decimal
	HasPercent          bool
	ResolvedPercent     decimal.Decimal
	EntryConfirmationID string
}

type BreakevenIntent struct {
	Symbol string
}

type TrailIntent struct {
	Symbol       string
	CallbackRate decimal.Decimal
}

type AddIntent struct {
	Symbol string
	Size   OrderSize
}

type PlanStatusIntent struct {
	PlanID string
}

func (i Intent) IsExchangeChanging() bool {
	return i.Type == IntentOpen || i.Type == IntentClose || i.Type == IntentBreakeven || i.Type == IntentTrail || i.Type == IntentAdd
}
