package api

import (
	"encoding/json"

	"bottrade/internal/decimal"
	"bottrade/internal/domain"
	"bottrade/internal/orders"

	"github.com/gofiber/fiber/v3"
)

// Live positions: read the user's own open positions, and close one. Closing
// reuses the exact same Prepare → Confirm flow as every other order (idempotent,
// TTL'd, atomic, and inheriting the testnet/real-trading gates), so the force-
// close button can never bypass a safety check — it just stages a 100% close.

// handleGetPositions returns the signed-in user's open positions (their account).
func (s *Server) handleGetPositions(c fiber.Ctx) error {
	if s.orders == nil || !s.approved(c) {
		return c.JSON(fiber.Map{"positions": []any{}})
	}
	userID, ok := webUserID(c)
	if !ok {
		return c.JSON(fiber.Map{"positions": []any{}})
	}
	ps, err := s.orders.Positions(c.Context(), userID)
	if err != nil {
		s.logger.Warn("load positions failed", "user_id", userID, "error", err)
		return c.JSON(fiber.Map{"positions": []any{}, "error": "could not load positions"})
	}
	out := make([]fiber.Map, 0, len(ps))
	for _, p := range ps {
		if p.Amount.IsZero() {
			continue
		}
		out = append(out, fiber.Map{
			"symbol":   p.Symbol,
			"side":     string(p.Side),
			"amount":   p.Amount.String(),
			"entry":    p.EntryPrice.String(),
			"mark":     p.MarkPrice.String(),
			"pnl":      p.UnrealizedProfit.String(),
			"leverage": p.Leverage,
		})
	}
	return c.JSON(fiber.Map{"positions": out})
}

// handleClosePosition stages a 100% close of one symbol and returns a confirm_id;
// the user presses Confirm (same as any order) to actually place the close.
func (s *Server) handleClosePosition(c fiber.Ctx) error {
	if s.orders == nil {
		return c.JSON(fiber.Map{"output": "Live trading is not enabled on this server."})
	}
	if !s.approved(c) {
		return c.JSON(fiber.Map{"output": "Your access is pending approval."})
	}
	userID, ok := webUserID(c)
	if !ok {
		return c.JSON(fiber.Map{"output": "Closing a position needs a Telegram login."})
	}
	var body struct {
		Symbol string `json:"symbol"`
	}
	if err := json.Unmarshal(c.Body(), &body); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid JSON body"})
	}
	symbol := normalizeSymbol(body.Symbol)
	if symbol == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "symbol is required"})
	}
	intent := domain.Intent{
		Type: domain.IntentClose,
		Close: &domain.CloseIntent{
			Symbol:          symbol,
			All:             true,
			ResolvedPercent: decimal.NewFromInt(100),
		},
	}
	confirmation, err := s.orders.Prepare(c.Context(), userID, intent)
	if err != nil {
		return c.JSON(fiber.Map{"output": "⚠️ " + err.Error()})
	}
	return c.JSON(fiber.Map{
		"output":     "✖️ Close " + symbol + " (100%) on your active key — press Confirm:\n\n" + orders.Summary(intent),
		"confirm_id": confirmation.ID,
	})
}
