package api

import (
	"context"
	"sync"
	"time"
)

// CampaignMissionStatus is the lifecycle state of a durable multi-trade mission.
type CampaignMissionStatus string

const (
	CampaignMissionStatusRunning  CampaignMissionStatus = "running"
	CampaignMissionStatusReached  CampaignMissionStatus = "reached"  // profit target hit
	CampaignMissionStatusStopped  CampaignMissionStatus = "stopped"  // model/campaign stop rule
	CampaignMissionStatusExpired  CampaignMissionStatus = "expired"  // plan window elapsed
	CampaignMissionStatusDisarmed CampaignMissionStatus = "disarmed" // user cancelled
)

// CampaignMission is a durable, restart-safe multi-trade mission. Unlike an
// ArmedMission (one entry then stop), it runs campaign.Engine toward a profit
// target within a plan window, re-entering on each ANNY Basic setup. Its progress
// fields persist the campaign State so a restart resumes counting toward the
// model's target / two-loss / trade-cap stops rather than starting from zero.
type CampaignMission struct {
	ID                    string                `json:"id" bson:"_id"`
	UserKey               string                `json:"-" bson:"user_key"`
	UserID                int64                 `json:"-" bson:"user_id"`
	Symbol                string                `json:"symbol" bson:"symbol"`
	Strategy              string                `json:"strategy" bson:"strategy"`
	CapitalUSDT           string                `json:"capital_usdt" bson:"capital_usdt"`
	CapitalRiskPct        int                   `json:"capital_risk_pct" bson:"capital_risk_pct"`
	LeverageUsePct        int                   `json:"leverage_use_pct" bson:"leverage_use_pct"`
	TargetProfitUSDT      string                `json:"target_profit_usdt" bson:"target_profit_usdt"`
	MaxTrades             int                   `json:"max_trades" bson:"max_trades"`
	Duration              string                `json:"duration" bson:"duration"`
	DurationWindowSeconds int64                 `json:"duration_window_seconds" bson:"duration_window_seconds"`
	UseAI                 bool                  `json:"used_ai" bson:"used_ai"`
	Status                CampaignMissionStatus `json:"status" bson:"status"`
	Verdict               string                `json:"verdict,omitempty" bson:"verdict,omitempty"`

	// Durable progress — rehydrated into annybasic.State on restart.
	TradesClosed            int    `json:"trades_closed" bson:"trades_closed"`
	RealizedPnLUSDT         string `json:"realized_pnl_usdt" bson:"realized_pnl_usdt"`
	ConsecutiveLosses       int    `json:"consecutive_losses" bson:"consecutive_losses"`
	LastTradeIdempotencySeq int64  `json:"last_trade_seq" bson:"last_trade_seq"`

	ArmedAt    time.Time  `json:"armed_at" bson:"armed_at"`
	ExpiresAt  time.Time  `json:"expires_at" bson:"expires_at"`
	FinishedAt *time.Time `json:"finished_at,omitempty" bson:"finished_at,omitempty"`
	PurgeAt    *time.Time `json:"purge_at,omitempty" bson:"purge_at,omitempty"`
	CreatedAt  time.Time  `json:"created_at" bson:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at" bson:"updated_at"`
}

// CampaignMissionStore persists multi-trade missions so a running campaign
// survives an API restart (mirrors ArmedMissionStore).
type CampaignMissionStore interface {
	Save(ctx context.Context, mission CampaignMission) error
	Get(ctx context.Context, id string) (CampaignMission, bool, error)
	ListActive(ctx context.Context, now time.Time) ([]CampaignMission, error)
	ListUser(ctx context.Context, userKey string, limit int) ([]CampaignMission, error)
	// UpdateProgress persists one closed trade's cumulative state so a restart
	// resumes counting from it. Only updates a still-running mission.
	UpdateProgress(ctx context.Context, id string, tradesClosed int, realizedPnLUSDT string, consecutiveLosses int, lastSeq int64, now time.Time) (CampaignMission, bool, error)
	// Finish marks a running mission terminal (reached/stopped/expired) with the
	// campaign verdict. Only updates a still-running mission.
	Finish(ctx context.Context, id string, status CampaignMissionStatus, verdict string, now time.Time) (CampaignMission, bool, error)
	Disarm(ctx context.Context, userKey, id string, now time.Time) (CampaignMission, bool, error)
	// ExpireStale marks running missions whose window elapsed as expired (orphan
	// sweep on boot). Returns how many were swept.
	ExpireStale(ctx context.Context, now time.Time) (int, error)
}

func campaignMissionPurgeAt(expiresAt time.Time) *time.Time {
	if expiresAt.IsZero() {
		return nil
	}
	purgeAt := expiresAt.Add(armedMissionRetention)
	return &purgeAt
}

type memCampaignMissions struct {
	mu   sync.Mutex
	rows map[string]CampaignMission
}

func newMemCampaignMissions() *memCampaignMissions {
	return &memCampaignMissions{rows: make(map[string]CampaignMission)}
}

func (m *memCampaignMissions) Save(_ context.Context, mission CampaignMission) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rows[mission.ID] = mission
	return nil
}

func (m *memCampaignMissions) Get(_ context.Context, id string) (CampaignMission, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	row, ok := m.rows[id]
	return row, ok, nil
}

func (m *memCampaignMissions) ListActive(_ context.Context, now time.Time) ([]CampaignMission, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]CampaignMission, 0, len(m.rows))
	for _, row := range m.rows {
		if row.Status == CampaignMissionStatusRunning && now.Before(row.ExpiresAt) {
			out = append(out, row)
		}
	}
	return out, nil
}

func (m *memCampaignMissions) ListUser(_ context.Context, userKey string, limit int) ([]CampaignMission, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if limit <= 0 {
		limit = 20
	}
	out := make([]CampaignMission, 0, len(m.rows))
	for _, row := range m.rows {
		if row.UserKey == userKey {
			out = append(out, row)
		}
	}
	sortCampaignMissions(out)
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (m *memCampaignMissions) UpdateProgress(_ context.Context, id string, tradesClosed int, realizedPnLUSDT string, consecutiveLosses int, lastSeq int64, now time.Time) (CampaignMission, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	row, ok := m.rows[id]
	if !ok || row.Status != CampaignMissionStatusRunning {
		return row, false, nil
	}
	row.TradesClosed = tradesClosed
	row.RealizedPnLUSDT = realizedPnLUSDT
	row.ConsecutiveLosses = consecutiveLosses
	row.LastTradeIdempotencySeq = lastSeq
	row.UpdatedAt = now
	m.rows[id] = row
	return row, true, nil
}

func (m *memCampaignMissions) Finish(_ context.Context, id string, status CampaignMissionStatus, verdict string, now time.Time) (CampaignMission, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	row, ok := m.rows[id]
	if !ok || row.Status != CampaignMissionStatusRunning {
		return row, false, nil
	}
	row.Status = status
	row.Verdict = verdict
	row.FinishedAt = &now
	row.UpdatedAt = now
	if row.PurgeAt == nil {
		row.PurgeAt = campaignMissionPurgeAt(row.ExpiresAt)
	}
	m.rows[id] = row
	return row, true, nil
}

func (m *memCampaignMissions) Disarm(_ context.Context, userKey, id string, now time.Time) (CampaignMission, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	row, ok := m.rows[id]
	if !ok || row.UserKey != userKey {
		return CampaignMission{}, false, nil
	}
	if row.Status == CampaignMissionStatusRunning {
		row.Status = CampaignMissionStatusDisarmed
		row.UpdatedAt = now
		if row.PurgeAt == nil {
			row.PurgeAt = campaignMissionPurgeAt(row.ExpiresAt)
		}
		m.rows[id] = row
	}
	return row, true, nil
}

func (m *memCampaignMissions) ExpireStale(_ context.Context, now time.Time) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for id, row := range m.rows {
		if row.Status == CampaignMissionStatusRunning && !now.Before(row.ExpiresAt) {
			row.Status = CampaignMissionStatusExpired
			row.UpdatedAt = now
			if row.PurgeAt == nil {
				row.PurgeAt = campaignMissionPurgeAt(row.ExpiresAt)
			}
			m.rows[id] = row
			n++
		}
	}
	return n, nil
}

func sortCampaignMissions(rows []CampaignMission) {
	for i := 1; i < len(rows); i++ {
		for j := i; j > 0 && rows[j].CreatedAt.After(rows[j-1].CreatedAt); j-- {
			rows[j], rows[j-1] = rows[j-1], rows[j]
		}
	}
}
