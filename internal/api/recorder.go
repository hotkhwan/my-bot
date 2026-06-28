package api

import (
	"sort"
	"strconv"
	"strings"
	"time"

	"bottrade/internal/journal"
	"bottrade/internal/transparency"

	"github.com/gofiber/fiber/v3"
)

// missionReason summarises why a Mission ran, from its strategy and any AI
// models that voted. Richer reason/confidence will come once decision context
// is persisted alongside the trade.
func missionReason(strategy string, models []string) string {
	parts := make([]string, 0, 2)
	if strategy != "" {
		parts = append(parts, strategy)
	}
	if len(models) > 0 {
		parts = append(parts, "AI: "+strings.Join(models, "+"))
	}
	return strings.Join(parts, " · ")
}

// The Flight Recorder is ANNY's transparency feed: an append-only, content-
// hashed log of every decision and result the companion produced for this user —
// real testnet/live trades from the journal plus paper goal runs — each clearly
// labelled so paper is never passed off as a live track record. A Merkle root
// over the page is returned so the records are tamper-evident now and anchorable
// on-chain at launch. Read-only; never places or mutates anything.

const recorderMaxEntries = 100

type recorderEntry struct {
	MissionNo  int       `json:"mission_no"`
	At         time.Time `json:"at"`
	Kind       string    `json:"kind"`       // "trade" | "goal"
	Label      string    `json:"label"`      // paper | testnet | live
	Autonomous bool      `json:"autonomous"` // ran as an ANNY campaign
	Symbol     string    `json:"symbol"`
	Side       string    `json:"side,omitempty"`
	Leverage   int       `json:"leverage,omitempty"`
	Entry      string    `json:"entry,omitempty"`
	Exit       string    `json:"exit,omitempty"`
	Action     string    `json:"action"`
	Reason     string    `json:"reason,omitempty"`
	PnL        string    `json:"pnl"`
	Win        bool      `json:"win"`
	Hash       string    `json:"hash"`
}

type recorderStats struct {
	Since        *time.Time `json:"since"`
	RealTrades   int        `json:"real_trades"`
	RealWins     int        `json:"real_wins"`
	RealLosses   int        `json:"real_losses"`
	RealWinRate  float64    `json:"real_win_rate"`
	RealPnL      string     `json:"real_pnl"`
	PaperRuns    int        `json:"paper_runs"`
	Transparency int        `json:"transparency"` // always 100 — nothing hidden
}

func (s *Server) handleRecorder(c fiber.Ctx) error {
	userKey := claimsOf(c).Subject
	entries := make([]recorderEntry, 0, 64)
	stats := recorderStats{Transparency: 100}
	var realPnL float64

	// Real trades (testnet/live/dry-run) from the journal, keyed by the numeric
	// Telegram id. Web-only (password) users have no journal trades; that is fine.
	if userID, ok := webUserID(c); ok && s.report != nil {
		trades, err := s.report.List(c.Context(), journal.Filter{UserID: userID, ClosedOnly: true})
		if err == nil {
			for _, t := range trades {
				label := transparency.LabelForMode(t.Mode)
				win := t.PnLUSDT.IsPositive()
				at := t.ClosedAt
				if at.IsZero() {
					at = t.OpenedAt
				}
				entries = append(entries, recorderEntry{
					At:         at,
					Kind:       "trade",
					Label:      label,
					Autonomous: t.CampaignID != "",
					Symbol:     t.Symbol,
					Side:       t.Side,
					Leverage:   t.Leverage,
					Entry:      t.Entry.String(),
					Exit:       t.Exit.String(),
					Action:     string(t.Outcome),
					Reason:     missionReason(t.Strategy, t.Models),
					PnL:        t.PnLUSDT.String(),
					Win:        win,
					Hash: transparency.Hash("trade", t.ID, t.Symbol, t.Side, t.Mode,
						t.Entry.String(), t.Exit.String(), t.PnLUSDT.String(), string(t.Outcome), at.UTC().Format(time.RFC3339)),
				})
				if transparency.IsReal(label) {
					stats.RealTrades++
					if win {
						stats.RealWins++
					} else if !t.PnLUSDT.IsZero() {
						stats.RealLosses++
					}
					if v, perr := strconv.ParseFloat(t.PnLUSDT.String(), 64); perr == nil {
						realPnL += v
					}
				}
			}
		}
	}

	// Paper goal runs, keyed by the JWT subject (works for any signed-in user).
	if userKey != "" {
		runs, err := s.goalRuns.List(c.Context(), userKey, recorderMaxEntries)
		if err == nil {
			for _, r := range runs {
				win := false
				if v, perr := strconv.ParseFloat(r.RealizedPnL, 64); perr == nil {
					win = v > 0
				}
				entries = append(entries, recorderEntry{
					At:     r.CreatedAt,
					Kind:   "goal",
					Label:  transparency.LabelPaper,
					Symbol: r.Symbol,
					Action: "paper goal",
					Reason: r.Strategy + " · " + r.Verdict + " · WR " + strconv.FormatFloat(r.WinRatePct, 'f', 0, 64) + "%",
					PnL:    r.RealizedPnL,
					Win:    win,
					Hash: transparency.Hash("goal", r.Symbol, r.Strategy, r.ProfitTarget, r.Capital,
						r.RealizedPnL, r.Verdict, r.CreatedAt.UTC().Format(time.RFC3339)),
				})
				stats.PaperRuns++
			}
		}
	}

	// Newest first; number Missions oldest=1 so the count grows over time.
	sort.Slice(entries, func(i, j int) bool { return entries[i].At.After(entries[j].At) })
	total := len(entries)
	for i := range entries {
		entries[i].MissionNo = total - i
	}
	if total > 0 {
		since := entries[total-1].At
		stats.Since = &since
	}
	if stats.RealTrades > 0 {
		stats.RealWinRate = float64(stats.RealWins) / float64(stats.RealTrades) * 100
	}
	stats.RealPnL = strconv.FormatFloat(realPnL, 'f', 2, 64)

	if len(entries) > recorderMaxEntries {
		entries = entries[:recorderMaxEntries]
	}
	hashes := make([]string, len(entries))
	for i, e := range entries {
		hashes[i] = e.Hash
	}

	return c.JSON(fiber.Map{
		"stats":       stats,
		"entries":     entries,
		"merkle_root": transparency.MerkleRoot(hashes),
		"anchored":    false,
		"anchor_note": "On-chain anchoring (opBNB) goes live at launch — the Merkle root is computed and verifiable now.",
	})
}
