package api

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/gofiber/fiber/v3"

	"bottrade/internal/strategy/annybasic"
)

// handleArmCampaignMission arms a durable multi-trade mission: one confirmation
// pre-authorizes a bounded series of automatic testnet re-entries toward a profit
// target within the plan window (target / two losses / trade cap / window all stop
// it). Testnet-only, gated exactly like the single-shot armed path.
func (s *Server) handleArmCampaignMission(c fiber.Ctx) error {
	if s.campaignMissions == nil || s.orders == nil {
		return c.JSON(fiber.Map{"output": "Multi-trade missions are not enabled on this server."})
	}
	if !s.approved(c) {
		return c.JSON(fiber.Map{"output": "Your access is pending approval — request it on Home first."})
	}
	userID, ok := webUserID(c)
	if !ok {
		return c.JSON(fiber.Map{"output": "A live Mission needs a Telegram login (your key is tied to your Telegram account)."})
	}
	if !s.campaignMissionRuntimeAllowed() {
		return c.JSON(fiber.Map{"output": "Multi-trade missions run only in testnet live-mission mode (CampaignLiveEnabled, Binance testnet, dry-run off, real trading off) with the realtime gateway on. No order was staged."})
	}
	userKey := claimsOf(c).Subject
	if !s.hasActiveKeyForSubject(c.Context(), userKey) {
		return c.JSON(fiber.Map{
			"output":   "🔑 No active testnet Binance key yet. Open Settings → add a testnet key profile (Futures on, Withdrawals off) → tap “Make active”, then arm the Mission.",
			"need_key": true,
		})
	}

	var req goalRequest
	if err := json.Unmarshal(c.Body(), &req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid request"})
	}
	symbol := normalizeMissionSymbol(req.Symbol)
	if symbol == "" {
		return c.JSON(fiber.Map{"output": "Pick a symbol first (e.g. BTC)."})
	}

	now := time.Now().UTC()
	durationKey := missionDurationKey(req.Duration)
	window := planDuration(durationKey)

	active, err := s.campaignMissions.ListActive(c.Context(), now)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "could not arm mission"})
	}
	activeForUser := 0
	for _, row := range active {
		if row.UserKey != userKey {
			continue
		}
		if row.Symbol == symbol {
			return c.JSON(fiber.Map{"output": fmt.Sprintf("A multi-trade mission is already running on %s. Disarm it before arming another.", symbol), "campaign": row})
		}
		activeForUser++
	}
	if activeForUser >= maxActiveCampaignMissionsPerUser {
		return c.JSON(fiber.Map{"output": fmt.Sprintf("You already have %d multi-trade missions running. Disarm one first.", maxActiveCampaignMissionsPerUser)})
	}

	id, err := newCampaignMissionID()
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "could not arm mission"})
	}
	risk := req.CapitalRiskPct
	if risk == 0 {
		risk = req.Risk
	}
	if risk <= 0 {
		risk = 30
	}
	if risk > 100 {
		risk = 100
	}
	leverageUse := req.LeverageUsePct
	if leverageUse <= 0 {
		leverageUse = 25
	}
	if leverageUse > 100 {
		leverageUse = 100
	}
	expiresAt := now.Add(window)
	mission := CampaignMission{
		ID:                    id,
		UserKey:               userKey,
		UserID:                userID,
		Symbol:                symbol,
		Strategy:              annybasic.ID,
		CapitalUSDT:           capitalString(req.Capital),
		CapitalRiskPct:        risk,
		LeverageUsePct:        leverageUse,
		TargetProfitUSDT:      capitalString(req.Profit),
		MaxTrades:             15,
		Duration:              durationKey,
		DurationWindowSeconds: int64(window.Seconds()),
		UseAI:                 req.UseAI,
		Status:                CampaignMissionStatusRunning,
		RealizedPnLUSDT:       "0",
		ArmedAt:               now,
		ExpiresAt:             expiresAt,
		PurgeAt:               campaignMissionPurgeAt(expiresAt),
		CreatedAt:             now,
		UpdatedAt:             now,
	}
	if err := s.campaignMissions.Save(c.Context(), mission); err != nil {
		s.logger.Warn("campaign mission persist failed", "user", userKey, "symbol", symbol, "error", err)
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "could not arm mission"})
	}
	s.usage.Incr(userKey, "mission") // one quota unit per mission (not per re-entry)
	if runtimeCtx := s.runtimeContext(); runtimeCtx != nil {
		s.startCampaignMissionRunner(runtimeCtx, mission)
	}
	output := fmt.Sprintf("Running — a multi-trade ANNY Basic mission on %s (testnet). ANNY will auto-open and close capped entries using your active testnet key, with protective exits and a timed close on each. The series stops at whichever comes first: a %s USDT profit target, two consecutive losses, %d trades, or the plan window end at %s. Entries may result in losses. This one confirmation pre-authorizes this bounded, capped series; you can disarm any time. Trading digital assets involves substantial risk. Not financial advice.",
		symbol, mission.TargetProfitUSDT, mission.MaxTrades, expiresAt.Format(time.RFC3339))
	return c.JSON(fiber.Map{"output": output, "campaign": mission})
}

func (s *Server) handleCampaignMissions(c fiber.Ctx) error {
	if s.campaignMissions == nil {
		return c.JSON(fiber.Map{"campaign": []CampaignMission{}})
	}
	rows, err := s.campaignMissions.ListUser(c.Context(), claimsOf(c).Subject, 20)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "could not load missions"})
	}
	if rows == nil {
		rows = []CampaignMission{}
	}
	return c.JSON(fiber.Map{"campaign": rows})
}

func (s *Server) handleDisarmCampaignMission(c fiber.Ctx) error {
	if s.campaignMissions == nil {
		return c.Status(fiber.StatusNotImplemented).JSON(fiber.Map{"error": "multi-trade missions are not enabled"})
	}
	var body struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(c.Body(), &body); err != nil || strings.TrimSpace(body.ID) == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "missing mission id"})
	}
	id := strings.TrimSpace(body.ID)
	mission, ok, err := s.campaignMissions.Disarm(c.Context(), claimsOf(c).Subject, id, time.Now().UTC())
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "could not disarm mission"})
	}
	if !ok {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "mission not found"})
	}
	// Stop the running goroutine; an in-flight trade finishes resolving, but no new
	// entry opens. Disarm already set the terminal status, so the runner's Finish is
	// a no-op.
	if v, ok := s.campaignRunners.Load(id); ok {
		if cancel, ok := v.(context.CancelFunc); ok {
			cancel()
		}
	}
	output := "Disarmed. No new entries will open."
	switch mission.Status {
	case CampaignMissionStatusReached:
		output = "Target already reached. Check the Live monitor for the mission's trades."
	case CampaignMissionStatusExpired:
		output = "Already ended (window elapsed)."
	}
	return c.JSON(fiber.Map{"output": output, "campaign": mission})
}

// normalizeMissionSymbol upcases and appends USDT if the user typed a bare base
// (BTC → BTCUSDT), matching how the mission/goal flow resolves symbols.
func normalizeMissionSymbol(v string) string {
	sym := strings.ToUpper(strings.TrimSpace(v))
	if sym == "" {
		return ""
	}
	if !strings.HasSuffix(sym, "USDT") {
		sym += "USDT"
	}
	return sym
}
