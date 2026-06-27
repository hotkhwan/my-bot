package campaign

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"bottrade/internal/decimal"
	"bottrade/internal/signals"
)

// Trader executes a decision and blocks until the position resolves, returning
// the realized PnL (positive = win, negative = loss). The real implementation
// places the order and waits for the journal/monitor to report the close.
type Trader interface {
	Trade(ctx context.Context, decision signals.Decision) (decimal.Decimal, error)
}

// SignalSource provides the current market snapshot fed to the advisor.
type SignalSource interface {
	Signal(ctx context.Context, symbol string) (signals.MarketSignal, error)
}

// EngineConfig configures a campaign run.
type EngineConfig struct {
	Goal    Goal
	Symbol  string
	Signals SignalSource
	Advisor signals.Advisor
	Trader  Trader
	Logger  *slog.Logger
	// MaxConsecutiveSkips stops the run when the advisor declines to trade this
	// many times in a row (no opportunity / stay in cash). 0 disables the guard.
	MaxConsecutiveSkips int
}

// Engine drives a goal-seeking sequence of trades: ask the advisor, trade when
// it says open, tally the realized PnL, and stop when a campaign rule fires.
type Engine struct {
	cfg EngineConfig
}

// NewEngine validates dependencies and returns the engine.
func NewEngine(cfg EngineConfig) (*Engine, error) {
	if cfg.Signals == nil || cfg.Advisor == nil || cfg.Trader == nil {
		return nil, errors.New("campaign: signals, advisor and trader are required")
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	return &Engine{cfg: cfg}, nil
}

// Run drives the campaign until a stop rule fires (target reached / max drawdown
// / max trades) or the advisor stays in cash too long. It returns the final
// state and the verdict that stopped it.
func (e *Engine) Run(ctx context.Context) (State, Verdict, error) {
	state := State{Goal: e.cfg.Goal}
	skips := 0

	for {
		if verdict := Evaluate(state); verdict != Continue {
			e.cfg.Logger.Info("campaign finished",
				"verdict", verdict, "pnl", state.RealizedPnL.String(), "trades", state.TradesClosed)
			return state, verdict, nil
		}
		if err := ctx.Err(); err != nil {
			return state, Continue, err
		}

		signal, err := e.cfg.Signals.Signal(ctx, e.cfg.Symbol)
		if err != nil {
			return state, Continue, fmt.Errorf("campaign: signal: %w", err)
		}

		decision, err := e.cfg.Advisor.Decide(ctx, signal)
		if err != nil {
			e.cfg.Logger.Warn("campaign advisor failed", "error", err)
			if e.skipExhausted(&skips) {
				return state, Continue, nil
			}
			continue
		}
		if decision.Action != signals.ActionOpen {
			if e.skipExhausted(&skips) {
				e.cfg.Logger.Info("campaign stopped: advisor stayed in cash", "skips", skips)
				return state, Continue, nil
			}
			continue
		}
		skips = 0

		pnl, err := e.cfg.Trader.Trade(ctx, decision)
		if err != nil {
			e.cfg.Logger.Warn("campaign trade failed", "error", err)
			continue
		}
		state.RealizedPnL = state.RealizedPnL.Add(pnl)
		state.TradesClosed++
		e.cfg.Logger.Info("campaign trade closed",
			"pnl", pnl.String(), "total", state.RealizedPnL.String(), "trades", state.TradesClosed)
	}
}

func (e *Engine) skipExhausted(skips *int) bool {
	*skips++
	return e.cfg.MaxConsecutiveSkips > 0 && *skips >= e.cfg.MaxConsecutiveSkips
}
