// Package annybasic defines the versioned policy and decision contract for the
// first ANNY-owned strategy model. It does not place orders.
package annybasic

import "bottrade/internal/decimal"

const (
	ID      = "anny_basic"
	Name    = "ANNY Basic"
	Version = "1.2"
)

type Side string

const (
	SideNone  Side = ""
	SideLong  Side = "long"
	SideShort Side = "short"
)

type Phase string

const (
	PhaseFast      Phase = "fast"
	PhaseNormal    Phase = "normal"
	PhaseDefensive Phase = "defensive"
)

type CDCZone string

const (
	CDCNeutral CDCZone = "neutral"
	CDCGreen   CDCZone = "green"
	CDCRed     CDCZone = "red"
)

type QQECross string

const (
	QQENone      QQECross = "none"
	QQECrossUp   QQECross = "up"
	QQECrossDown QQECross = "down"
)

// Observation contains indicator facts produced by a market-data adapter.
type Observation struct {
	CDC15m             CDCZone
	QQEValue           float64
	QQECross           QQECross
	ExecutionAligned   bool
	MomentumConfirmed  bool
	EntryExtended      bool
	AbnormalVolatility bool
	Sideways           bool
}

type State struct {
	TradesClosed      int
	ConsecutiveLosses int
	RealizedPnLUSDT   decimal.Decimal
}

type Decision struct {
	Side        Side
	Phase       Phase
	MaxLeverage int
	Reason      string
	Stop        bool
}

// Evaluate applies entry and campaign-stop policy. The platform/user leverage
// ceiling always takes precedence over the model's requested ceiling.
func Evaluate(obs Observation, state State, maxLeverage int) Decision {
	phase := phaseFor(state.TradesClosed)
	if state.RealizedPnLUSDT.Cmp(decimal.NewFromInt(10)) >= 0 {
		return Decision{Phase: phase, Reason: "profit target reached", Stop: true}
	}
	if state.TradesClosed >= 15 {
		return Decision{Phase: PhaseDefensive, Reason: "15-trade hard stop", Stop: true}
	}
	if state.ConsecutiveLosses >= 2 {
		return Decision{Phase: phase, Reason: "two consecutive losses", Stop: true}
	}
	if obs.AbnormalVolatility || obs.Sideways || obs.EntryExtended || !obs.ExecutionAligned {
		return Decision{Phase: phase, Reason: "no-trade market condition"}
	}

	side := SideNone
	switch {
	case obs.CDC15m == CDCGreen && obs.QQEValue > 50 && obs.QQECross == QQECrossUp:
		side = SideLong
	case obs.CDC15m == CDCRed && obs.QQEValue < 50 && obs.QQECross == QQECrossDown:
		side = SideShort
	default:
		return Decision{Phase: phase, Reason: "CDC and QQE are not aligned"}
	}

	modelCap := 50
	if obs.MomentumConfirmed && phase != PhaseDefensive {
		modelCap = 100
	}
	if phase == PhaseDefensive {
		modelCap = 50
	}
	return Decision{
		Side:        side,
		Phase:       phase,
		MaxLeverage: clampLeverage(modelCap, maxLeverage),
		Reason:      "CDC and QQE aligned",
	}
}

func phaseFor(tradesClosed int) Phase {
	switch {
	case tradesClosed < 3:
		return PhaseFast
	case tradesClosed < 10:
		return PhaseNormal
	default:
		return PhaseDefensive
	}
}

func clampLeverage(modelCap, platformCap int) int {
	if platformCap <= 0 {
		platformCap = 20
	}
	if platformCap < modelCap {
		return platformCap
	}
	return modelCap
}
