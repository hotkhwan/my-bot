package api

import (
	"encoding/json"
	"strconv"
	"strings"

	"bottrade/internal/backtest"
	"bottrade/internal/campaign"
	"bottrade/internal/orders"
	"bottrade/internal/signals"

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
	missionLeverage    = 3
	missionMaxSizeUSDT = 200 // hard cap on notional so a mission can't go large
)

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
	// A live Mission places a real order on the user's active key. Require one to
	// be set up and active first, and point them to Settings to do it.
	if s.credentials != nil && !s.hasActiveKey(c) {
		return c.JSON(fiber.Map{
			"output":   "🔑 No active Binance key yet. Open Settings → add a key profile (testnet, Futures on, Withdrawals off) → tap “Make active”, then run the Mission.",
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
	interval := req.Interval
	if !allowedIntervals[interval] {
		interval = "1h"
	}
	strategyName := "ema"
	switch req.Strategy {
	case "rsi", "macd", "sma", "breakout", "auto":
		strategyName = req.Strategy
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
	side := signalSideStr(campaign.StrategyFor(strategyName).Evaluate(closes))
	reason := strategyName
	if req.UseAI {
		bias, note := s.aiBias(c.Context(), claimsOf(c).Subject, symbol, closes[len(closes)-1])
		reason = strings.TrimSpace(strategyName + " · " + note)
		switch bias {
		case campaign.BiasLong:
			side = "long"
		case campaign.BiasShort:
			side = "short"
		}
	}
	if side == "" {
		return c.JSON(fiber.Map{"output": "🤖 No setup right now — ANNY stays in cash. Try another coin or timeframe."})
	}

	entry := candles[len(candles)-1].Close
	sl, tp := missionBracket(side, entry)
	size := missionSize(req.Capital)

	decision := signals.Decision{
		Action:     signals.ActionOpen,
		Symbol:     symbol,
		Side:       side,
		Leverage:   missionLeverage,
		Entry:      trimPrice(entry),
		StopLoss:   trimPrice(sl),
		TakeProfit: trimPrice(tp),
		SizeUSDT:   strconv.FormatFloat(size, 'f', 2, 64),
		Reason:     reason,
	}
	intent, err := signals.DecisionToIntent(decision, s.cfg.App.MaxLeverage)
	if err != nil {
		return c.JSON(fiber.Map{"output": "⚠️ Could not build the mission: " + err.Error()})
	}
	confirmation, err := s.orders.Prepare(c.Context(), userID, intent)
	if err != nil {
		return c.JSON(fiber.Map{"output": "⚠️ " + err.Error()})
	}
	s.usage.Incr(claimsOf(c).Subject, "mission") // count the attempt toward the daily limit
	return c.JSON(fiber.Map{
		"output":     "🚀 Review this live Mission (testnet) — press Confirm to place it on your active key:\n\n" + orders.Summary(intent),
		"confirm_id": confirmation.ID,
	})
}

// hasActiveKey reports whether the user has a Binance key profile marked active.
func (s *Server) hasActiveKey(c fiber.Ctx) bool {
	if s.credentials == nil {
		return false
	}
	profiles, err := s.credentials.Profiles(c.Context(), claimsOf(c).Subject)
	if err != nil {
		return false
	}
	for _, p := range profiles {
		if p.Active {
			return true
		}
	}
	return false
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
