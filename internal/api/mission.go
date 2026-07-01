package api

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"bottrade/internal/backtest"
	"bottrade/internal/campaign"
	"bottrade/internal/decimal"
	"bottrade/internal/marketdata"
	"bottrade/internal/orders"
	"bottrade/internal/signals"
	"bottrade/internal/strategy/annybasic"

	"github.com/gofiber/fiber/v3"
)

// A live Mission turns the Goal's strategy/AI into ONE real order on the user's
// own key, then hands it to the SAME confirm flow as every other trade: it is
// prepared (staged, with idempotency + TTL) and only executes after the user
// presses Confirm. Safety is inherited, not re-implemented — the order service
// refuses live-account orders unless REAL_TRADING_ENABLED (off), routes to the
// user's testnet key, and in dry-run mode places nothing at all. The mission
// just picks side/levels/size; it never bypasses a gate.

const (
	missionSLPct       = 0.01 // stop distance as a fraction of entry
	missionMaxLeverage = 100  // testnet cap (BTC testnet allows up to 125x)
	missionMaxSizeUSDT = 200  // hard cap on notional so a mission can't go large
)

type timedMission struct {
	UserID   int64
	Symbol   string
	Duration time.Duration
}

func planDuration(value string) time.Duration {
	switch value {
	case "15m":
		return 15 * time.Minute
	case "1h":
		return time.Hour
	case "2h":
		return 2 * time.Hour
	case "4h":
		return 4 * time.Hour
	case "8h":
		return 8 * time.Hour
	case "12h":
		return 12 * time.Hour
	case "24h":
		return 24 * time.Hour
	case "48h":
		return 48 * time.Hour
	case "1w":
		return 7 * 24 * time.Hour
	default:
		return time.Hour
	}
}

// missionLeverageFor applies a percentage to the configured safety ceiling.
// Capital risk is deliberately not reused as leverage.
func missionLeverageFor(usePct, maxLeverage int) int {
	if usePct <= 0 {
		usePct = 25
	}
	if usePct > 100 {
		usePct = 100
	}
	if maxLeverage <= 0 || maxLeverage > missionMaxLeverage {
		maxLeverage = missionMaxLeverage
	}
	lev := (maxLeverage*usePct + 99) / 100
	if lev < 1 {
		lev = 1
	}
	return lev
}

func (s *Server) handleMissionPrepare(c fiber.Ctx) error {
	if s.orders == nil {
		return c.JSON(fiber.Map{"output": "Live trading is not enabled on this server."})
	}
	if !s.approved(c) {
		return c.JSON(fiber.Map{"output": "Your access is pending approval — request it on Home first."})
	}
	userID, ok := webUserID(c)
	if !ok {
		return c.JSON(fiber.Map{"output": "A live Mission needs a Telegram login (your key is tied to your Telegram account)."})
	}
	// A live Mission places an order on the user's active testnet key. Require one
	// to be set up and active first, and point them to Settings to do it.
	if s.credentials != nil && !s.hasActiveKey(c) {
		return c.JSON(fiber.Map{
			"output":   "🔑 No active testnet Binance key yet. Open Settings → add a testnet key profile (Futures on, Withdrawals off) → tap “Make active”, then run the Mission.",
			"need_key": true,
		})
	}
	if allowed, msg := s.allow(c.Context(), claimsOf(c).Subject, "mission"); !allowed {
		return c.JSON(fiber.Map{"output": "🔒 " + msg})
	}

	var req goalRequest
	if err := json.Unmarshal(c.Body(), &req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid JSON body"})
	}
	symbol := normalizeSymbol(req.Symbol)
	durationKey, spec := missionSpecFor(req.Duration)
	interval := spec.ExecutionInterval
	strategyName := "ema"
	switch req.Strategy {
	case "rsi", "macd", "sma", "breakout", "auto":
		strategyName = req.Strategy
	case annybasic.ID:
		strategyName = annybasic.ID
		interval = "1m"
	}

	candles, err := s.market.Candles(c.Context(), symbol, interval, 120)
	if err != nil || len(candles) == 0 {
		return c.Status(fiber.StatusBadGateway).JSON(fiber.Map{"error": "could not load market data for " + symbol})
	}
	closes := make([]float64, len(candles))
	for i, cd := range candles {
		closes[i] = cd.Close
	}

	// Direction: the strategy's current signal, optionally constrained by AI.
	side := ""
	reason := strategyName
	modelLeverageCap := missionMaxLeverage
	if strategyName == annybasic.ID {
		decision, err := s.annyBasicLiveDecision(c.Context(), symbol, candles)
		if err != nil {
			return c.Status(fiber.StatusBadGateway).JSON(fiber.Map{"error": "could not assess ANNY Basic setup for " + symbol})
		}
		if decision.Stop || decision.Side == annybasic.SideNone {
			return s.armANNYBasicMission(c, req, userID, symbol, decision.Reason)
		}
		side = string(decision.Side)
		reason = annybasic.ID + " v" + annybasic.Version + " · " + decision.Reason
		modelLeverageCap = decision.MaxLeverage
	} else {
		side = signalSideStr(campaign.StrategyFor(strategyName).Evaluate(closes))
	}
	if req.UseAI {
		bias, note := s.aiBias(c.Context(), claimsOf(c).Subject, symbol, closes[len(closes)-1])
		reason = strings.TrimSpace(reason + " · " + note)
		if strategyName == annybasic.ID {
			if (bias == campaign.BiasLong && side == "short") || (bias == campaign.BiasShort && side == "long") {
				return c.JSON(fiber.Map{"output": "🤖 No ANNY Basic setup right now — AI bias conflicts with CDC/QQE. No testnet order is staged."})
			}
		} else {
			switch bias {
			case campaign.BiasLong:
				side = "long"
			case campaign.BiasShort:
				side = "short"
			}
		}
	}
	if side == "" {
		return c.JSON(fiber.Map{"output": "🤖 No setup right now — ANNY stays in cash. Try another coin or timeframe."})
	}

	entry := candles[len(candles)-1].Close
	sl, tp := missionBracket(side, entry)
	size := missionSize(req.Capital)
	leverage := missionLeverageFor(req.LeverageUsePct, minPositive(s.cfg.App.MaxLeverage, modelLeverageCap))

	decision := signals.Decision{
		Action:     signals.ActionOpen,
		Symbol:     symbol,
		Side:       side,
		Leverage:   leverage,
		Entry:      trimPrice(entry),
		StopLoss:   trimPrice(sl),
		TakeProfit: trimPrice(tp),
		SizeUSDT:   strconv.FormatFloat(size, 'f', 2, 64),
		Reason:     reason,
	}
	// Missions cap leverage at missionMaxLeverage (testnet), independent of the
	// global MaxLeverage knob used for chat-driven trades.
	intent, err := signals.DecisionToIntent(decision, missionMaxLeverage)
	if err != nil {
		return c.JSON(fiber.Map{"output": "⚠️ Could not build the mission: " + err.Error()})
	}
	confirmation, err := s.orders.Prepare(c.Context(), userID, intent)
	if err != nil {
		return c.JSON(fiber.Map{"output": "⚠️ " + err.Error()})
	}
	// Persist a durable awaiting-entry close now (keyed by the confirmation id). It
	// stays poller-invisible until the entry actually confirms, and survives an API
	// restart — so a confirm after a restart still arms the plan-end close. If it
	// can't be persisted, cancel the confirmation rather than let the user confirm an
	// entry with no timed close while the copy promises one.
	if _, err := s.scheduleTimedMissionClose(timedMission{
		UserID: userID, Symbol: symbol, Duration: planDuration(durationKey),
	}, confirmation.ID); err != nil {
		s.logger.Warn("could not persist awaiting timed close; cancelling confirmation", "error", err)
		_ = s.orders.Cancel(c.Context(), userID, confirmation.ID)
		return c.JSON(fiber.Map{"output": "⚠️ Could not prepare this Mission's timed close — nothing was staged. Please try again."})
	}
	s.usage.Incr(claimsOf(c).Subject, "mission") // count the attempt toward the daily limit
	return c.JSON(fiber.Map{
		"output":     "🚀 Review this live Mission (testnet) — Confirm authorizes the entry and a timed close at the end of the plan if TP/SL has not closed it first:\n\n" + orders.Summary(intent) + "\n\nA protective stop, take-profit, and timed close are attached to this testnet entry.",
		"confirm_id": confirmation.ID,
		"mission": fiber.Map{
			"symbol": symbol, "side": side, "entry": trimPrice(entry),
			"stop_loss": trimPrice(sl), "take_profit": trimPrice(tp),
			"leverage": leverage, "duration": durationKey, "size_usdt": strconv.FormatFloat(size, 'f', 2, 64),
		},
	})
}

func (s *Server) annyBasicLiveDecision(ctx context.Context, symbol string, executionCandles []marketdata.Candle) (annybasic.Decision, error) {
	if s.annyBasicDecider != nil {
		return s.annyBasicDecider(ctx, symbol, executionCandles)
	}
	mainCandles, err := s.market.Candles(ctx, symbol, "15m", 200)
	if err != nil {
		return annybasic.Decision{}, err
	}
	if len(executionCandles) == 0 {
		return annybasic.Decision{Reason: "no execution candles"}, nil
	}
	obs, err := annybasic.ObserveAt(mainCandles, executionCandles, len(executionCandles)-1)
	if err != nil {
		return annybasic.Decision{Reason: err.Error()}, nil
	}
	return annybasic.Evaluate(obs, annybasic.State{RealizedPnLUSDT: decimal.Zero()}, missionMaxLeverage), nil
}

func minPositive(a, b int) int {
	switch {
	case a <= 0:
		return b
	case b <= 0:
		return a
	case a < b:
		return a
	default:
		return b
	}
}

// hasActiveKey reports whether the user has an active testnet Binance key.
func (s *Server) hasActiveKey(c fiber.Ctx) bool {
	return s.hasActiveKeyForSubject(c.Context(), claimsOf(c).Subject)
}

func missionBracket(side string, entry float64) (sl, tp float64) {
	if side == "long" {
		return entry * (1 - missionSLPct), entry * (1 + missionSLPct*2)
	}
	return entry * (1 + missionSLPct), entry * (1 - missionSLPct*2)
}

// missionSize derives a capped notional from the user's stated capital.
func missionSize(capital json.Number) float64 {
	v, err := strconv.ParseFloat(strings.TrimSpace(capital.String()), 64)
	if err != nil || v <= 0 {
		v = 50
	}
	if v > missionMaxSizeUSDT {
		v = missionMaxSizeUSDT
	}
	if v < 5 {
		v = 5
	}
	return v
}

func signalSideStr(sig backtest.Signal) string {
	switch sig {
	case backtest.Long:
		return "long"
	case backtest.Short:
		return "short"
	default:
		return ""
	}
}

func trimPrice(v float64) string {
	return strconv.FormatFloat(roundSig(v, 6), 'f', -1, 64)
}

// roundSig rounds to n significant figures so prices stay tidy; the executor
// applies real exchange tick-size precision before placing.
func roundSig(v float64, n int) float64 {
	if v == 0 {
		return 0
	}
	s := strconv.FormatFloat(v, 'g', n, 64)
	out, _ := strconv.ParseFloat(s, 64)
	return out
}
