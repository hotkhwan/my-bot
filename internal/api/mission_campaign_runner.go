package api

import (
	"context"
	"log/slog"
	"time"

	"bottrade/internal/campaign"
	"bottrade/internal/decimal"
	"bottrade/internal/signals"
	"bottrade/internal/strategy/annybasic"
)

// recordingTrader wraps the campaign trader so each closed trade's realized PnL is
// folded back into the ANNY Basic advisor's State before the next Decide. Without
// this the model's target / two-loss / trade-cap stops never see live outcomes.
type recordingTrader struct {
	inner   campaign.Trader
	advisor *missionCampaignAdvisor
	// onRecord persists the advisor's accumulated State after each closed trade so
	// a restart resumes counting from it. Optional (nil in unit tests).
	onRecord func(state annybasic.State, seq int64)
}

func (t *recordingTrader) Trade(ctx context.Context, decision signals.Decision) (decimal.Decimal, error) {
	pnl, err := t.inner.Trade(ctx, decision)
	if err != nil {
		return pnl, err
	}
	if decision.Action == signals.ActionOpen {
		t.advisor.Record(pnl)
		if t.onRecord != nil {
			t.onRecord(t.advisor.State(), int64(t.advisor.State().TradesClosed))
		}
	}
	return pnl, nil
}

// missionCampaignControl decorates the advisor with the plan-window policy the raw
// Engine loop lacks: (1) stop opening new trades once the late-window entry cutoff
// passes, and (2) end the run as soon as the model's own stop rule fires (the
// Engine's skip guard is disabled for windowed missions, so a "stopped" advisor
// would otherwise hold forever until the deadline). It never places orders.
type missionCampaignControl struct {
	inner         *missionCampaignAdvisor
	now           func() time.Time
	entryDeadline time.Time
	onStop        func()
	logger        *slog.Logger
	stopFired     bool
}

func (c *missionCampaignControl) Decide(ctx context.Context, signal signals.MarketSignal) (signals.Decision, error) {
	if !c.now().Before(c.entryDeadline) {
		return signals.Decision{Action: signals.ActionHold, Symbol: signal.Symbol, Reason: "plan window closing — no new entries"}, nil
	}
	decision, err := c.inner.Decide(ctx, signal)
	if err != nil {
		return decision, err
	}
	if stopped, reason := c.inner.Stopped(); stopped && !c.stopFired {
		c.stopFired = true
		c.logger.Info("mission campaign stop rule fired", "reason", reason)
		if c.onStop != nil {
			c.onStop()
		}
	}
	return decision, err
}

// pacedSignals throttles the Engine's otherwise-tight loop to one observation per
// pollInterval, so a quiet market does not hammer the market-data API. It respects
// context cancellation while waiting.
type pacedSignals struct {
	inner    campaign.SignalSource
	interval time.Duration
	now      func() time.Time
	sleep    func(ctx context.Context, d time.Duration) error
	last     time.Time
}

func (p *pacedSignals) Signal(ctx context.Context, symbol string) (signals.MarketSignal, error) {
	if !p.last.IsZero() {
		if wait := p.interval - p.now().Sub(p.last); wait > 0 {
			if err := p.sleep(ctx, wait); err != nil {
				return signals.MarketSignal{}, err
			}
		}
	}
	p.last = p.now()
	return p.inner.Signal(ctx, symbol)
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// missionCampaignRunner drives one multi-trade mission: it wraps campaign.Engine
// with a plan-window deadline, a late-window entry cutoff, paced observation, and
// the State-feedback trader. Each entry is placed by the required-user idempotency
// placer and protected by its own durable per-trade close, exactly like the
// single-shot armed path — so the last open trade at window end is flushed by its
// own scheduled close.
type missionCampaignRunner struct {
	advisor      *missionCampaignAdvisor
	signals      campaign.SignalSource
	placer       campaign.OrderPlacer
	resolver     campaign.CloseResolver
	goal         campaign.Goal
	symbol       string
	userID       int64
	window       time.Duration
	entryCutoff  time.Duration
	pollInterval time.Duration
	now          func() time.Time
	sleep        func(ctx context.Context, d time.Duration) error
	logger       *slog.Logger
}

// Run executes the mission until a stop rule fires (model target / two losses /
// trade cap, or the campaign Goal's target / drawdown / max-trades) or the plan
// window expires. It returns the final campaign state and verdict.
func (r *missionCampaignRunner) Run(ctx context.Context) (campaign.State, campaign.Verdict, error) {
	now := r.now
	if now == nil {
		now = time.Now
	}
	sleep := r.sleep
	if sleep == nil {
		sleep = sleepCtx
	}
	logger := r.logger
	if logger == nil {
		logger = slog.Default()
	}
	pollInterval := r.pollInterval
	if pollInterval <= 0 {
		pollInterval = armedMissionCheckInterval
	}

	runCtx, cancel := context.WithTimeout(ctx, r.window)
	defer cancel()

	control := &missionCampaignControl{
		inner:         r.advisor,
		now:           now,
		entryDeadline: now().Add(r.window - r.entryCutoff),
		onStop:        cancel,
		logger:        logger,
	}
	trader := &recordingTrader{
		inner:   campaign.NewLiveTrader(r.userID, r.placer, r.resolver, logger),
		advisor: r.advisor,
	}
	paced := &pacedSignals{inner: r.signals, interval: pollInterval, now: now, sleep: sleep}

	engine, err := campaign.NewEngine(campaign.EngineConfig{
		Goal:                r.goal,
		Symbol:              r.symbol,
		Signals:             paced,
		Advisor:             control,
		Trader:              trader,
		Logger:              logger,
		MaxConsecutiveSkips: 0, // window deadline / stop rules end the run, not a skip count
	})
	if err != nil {
		return campaign.State{}, campaign.Continue, err
	}
	return engine.Run(runCtx)
}
