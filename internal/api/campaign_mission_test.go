package api

import (
	"context"
	"testing"
	"time"
)

func runningMission(id, userKey string, expiresAt time.Time) CampaignMission {
	return CampaignMission{
		ID: id, UserKey: userKey, UserID: 7, Symbol: "BTCUSDT", Strategy: "anny_basic",
		TargetProfitUSDT: "5", MaxTrades: 15, Status: CampaignMissionStatusRunning,
		RealizedPnLUSDT: "0", ExpiresAt: expiresAt, CreatedAt: time.Unix(1710000000, 0),
	}
}

func TestMemCampaignMissionsListActiveFiltersByStatusAndWindow(t *testing.T) {
	store := newMemCampaignMissions()
	now := time.Unix(1710000000, 0)
	ctx := context.Background()

	_ = store.Save(ctx, runningMission("live", "u1", now.Add(time.Hour)))
	_ = store.Save(ctx, runningMission("expired", "u1", now.Add(-time.Hour)))
	finished := runningMission("done", "u1", now.Add(time.Hour))
	finished.Status = CampaignMissionStatusReached
	_ = store.Save(ctx, finished)

	active, err := store.ListActive(ctx, now)
	if err != nil {
		t.Fatalf("ListActive: %v", err)
	}
	if len(active) != 1 || active[0].ID != "live" {
		t.Fatalf("ListActive = %+v, want only the in-window running mission", active)
	}
}

func TestMemCampaignMissionsUpdateProgressPersistsForRehydrate(t *testing.T) {
	store := newMemCampaignMissions()
	now := time.Unix(1710000000, 0)
	ctx := context.Background()
	_ = store.Save(ctx, runningMission("m1", "u1", now.Add(time.Hour)))

	if _, ok, err := store.UpdateProgress(ctx, "m1", 3, "4.20", 1, 3, now); err != nil || !ok {
		t.Fatalf("UpdateProgress ok=%v err=%v", ok, err)
	}
	// Simulate a restart read: the persisted progress must round-trip so the
	// advisor's State rehydrates instead of restarting from zero.
	got, ok, err := store.Get(ctx, "m1")
	if err != nil || !ok {
		t.Fatalf("Get after progress: ok=%v err=%v", ok, err)
	}
	if got.TradesClosed != 3 || got.RealizedPnLUSDT != "4.20" || got.ConsecutiveLosses != 1 || got.LastTradeIdempotencySeq != 3 {
		t.Fatalf("persisted progress = %+v, want trades=3 pnl=4.20 losses=1 seq=3", got)
	}
}

func TestMemCampaignMissionsUpdateProgressIgnoresFinished(t *testing.T) {
	store := newMemCampaignMissions()
	now := time.Unix(1710000000, 0)
	ctx := context.Background()
	_ = store.Save(ctx, runningMission("m1", "u1", now.Add(time.Hour)))
	if _, _, err := store.Finish(ctx, "m1", CampaignMissionStatusReached, "target_reached", now); err != nil {
		t.Fatalf("Finish: %v", err)
	}
	if _, ok, _ := store.UpdateProgress(ctx, "m1", 9, "9", 0, 9, now); ok {
		t.Fatal("UpdateProgress must not mutate a finished mission")
	}
}

func TestMemCampaignMissionsFinishExcludesFromActiveAndSetsPurge(t *testing.T) {
	store := newMemCampaignMissions()
	now := time.Unix(1710000000, 0)
	ctx := context.Background()
	_ = store.Save(ctx, runningMission("m1", "u1", now.Add(time.Hour)))

	got, ok, err := store.Finish(ctx, "m1", CampaignMissionStatusStopped, "strategy_rule", now)
	if err != nil || !ok {
		t.Fatalf("Finish ok=%v err=%v", ok, err)
	}
	if got.Verdict != "strategy_rule" || got.FinishedAt == nil || got.PurgeAt == nil {
		t.Fatalf("finished mission = %+v, want verdict+finishedAt+purgeAt set", got)
	}
	active, _ := store.ListActive(ctx, now)
	if len(active) != 0 {
		t.Fatalf("ListActive = %+v, want empty after finish", active)
	}
}

func TestMemCampaignMissionsDisarmOwnershipAndExpireStale(t *testing.T) {
	store := newMemCampaignMissions()
	now := time.Unix(1710000000, 0)
	ctx := context.Background()
	_ = store.Save(ctx, runningMission("m1", "owner", now.Add(time.Hour)))

	if _, ok, _ := store.Disarm(ctx, "intruder", "m1", now); ok {
		t.Fatal("Disarm must reject a non-owner")
	}
	got, ok, err := store.Disarm(ctx, "owner", "m1", now)
	if err != nil || !ok || got.Status != CampaignMissionStatusDisarmed {
		t.Fatalf("Disarm by owner = (%+v, %v, %v)", got, ok, err)
	}

	// A separate running-but-past-window mission is swept by ExpireStale.
	_ = store.Save(ctx, runningMission("stale", "owner", now.Add(-time.Minute)))
	n, err := store.ExpireStale(ctx, now)
	if err != nil || n != 1 {
		t.Fatalf("ExpireStale = (%d, %v), want 1 swept", n, err)
	}
}
