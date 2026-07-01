package app

import (
	"context"
	"errors"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	mongodriver "go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"bottrade/internal/api"
)

// mongoCampaignMissions persists durable multi-trade missions so a running
// campaign survives an API restart. It mirrors mongoArmedMissions and implements
// api.CampaignMissionStore.
type mongoCampaignMissions struct {
	coll *mongodriver.Collection
}

func newMongoCampaignMissions(coll *mongodriver.Collection) *mongoCampaignMissions {
	return &mongoCampaignMissions{coll: coll}
}

func (m *mongoCampaignMissions) Save(ctx context.Context, mission api.CampaignMission) error {
	_, err := m.coll.InsertOne(ctx, mission)
	return err
}

func (m *mongoCampaignMissions) Get(ctx context.Context, id string) (api.CampaignMission, bool, error) {
	var mission api.CampaignMission
	err := m.coll.FindOne(ctx, bson.D{{Key: "_id", Value: id}}).Decode(&mission)
	if errors.Is(err, mongodriver.ErrNoDocuments) {
		return api.CampaignMission{}, false, nil
	}
	if err != nil {
		return api.CampaignMission{}, false, err
	}
	return mission, true, nil
}

func (m *mongoCampaignMissions) ListActive(ctx context.Context, now time.Time) ([]api.CampaignMission, error) {
	cursor, err := m.coll.Find(ctx, bson.D{
		{Key: "status", Value: api.CampaignMissionStatusRunning},
		{Key: "expires_at", Value: bson.D{{Key: "$gt", Value: now}}},
	}, options.Find().SetSort(bson.D{{Key: "expires_at", Value: 1}}).SetLimit(1000))
	if err != nil {
		return nil, err
	}
	defer cursor.Close(ctx)
	var rows []api.CampaignMission
	if err := cursor.All(ctx, &rows); err != nil {
		return nil, err
	}
	return rows, nil
}

func (m *mongoCampaignMissions) ListUser(ctx context.Context, userKey string, limit int) ([]api.CampaignMission, error) {
	if limit <= 0 {
		limit = 20
	}
	cursor, err := m.coll.Find(ctx,
		bson.D{{Key: "user_key", Value: userKey}},
		options.Find().SetSort(bson.D{{Key: "created_at", Value: -1}}).SetLimit(int64(limit)),
	)
	if err != nil {
		return nil, err
	}
	defer cursor.Close(ctx)
	var rows []api.CampaignMission
	if err := cursor.All(ctx, &rows); err != nil {
		return nil, err
	}
	return rows, nil
}

func (m *mongoCampaignMissions) UpdateProgress(ctx context.Context, id string, tradesClosed int, realizedPnLUSDT string, consecutiveLosses int, lastSeq int64, now time.Time) (api.CampaignMission, bool, error) {
	return m.transition(ctx, bson.D{
		{Key: "_id", Value: id},
		{Key: "status", Value: api.CampaignMissionStatusRunning},
	}, bson.D{{Key: "$set", Value: bson.D{
		{Key: "trades_closed", Value: tradesClosed},
		{Key: "realized_pnl_usdt", Value: realizedPnLUSDT},
		{Key: "consecutive_losses", Value: consecutiveLosses},
		{Key: "last_trade_seq", Value: lastSeq},
		{Key: "updated_at", Value: now},
	}}})
}

func (m *mongoCampaignMissions) Finish(ctx context.Context, id string, status api.CampaignMissionStatus, verdict string, now time.Time) (api.CampaignMission, bool, error) {
	return m.transition(ctx, bson.D{
		{Key: "_id", Value: id},
		{Key: "status", Value: api.CampaignMissionStatusRunning},
	}, bson.D{{Key: "$set", Value: bson.D{
		{Key: "status", Value: status},
		{Key: "verdict", Value: verdict},
		{Key: "finished_at", Value: now},
		{Key: "updated_at", Value: now},
		{Key: "purge_at", Value: campaignMissionPurge(now)},
	}}})
}

func (m *mongoCampaignMissions) Disarm(ctx context.Context, userKey, id string, now time.Time) (api.CampaignMission, bool, error) {
	mission, ok, err := m.transition(ctx, bson.D{
		{Key: "_id", Value: id},
		{Key: "user_key", Value: userKey},
		{Key: "status", Value: api.CampaignMissionStatusRunning},
	}, bson.D{{Key: "$set", Value: bson.D{
		{Key: "status", Value: api.CampaignMissionStatusDisarmed},
		{Key: "updated_at", Value: now},
		{Key: "purge_at", Value: campaignMissionPurge(now)},
	}}})
	if err != nil || ok {
		return mission, ok, err
	}
	mission, ok, err = m.Get(ctx, id)
	if err != nil || !ok || mission.UserKey != userKey {
		return api.CampaignMission{}, false, err
	}
	return mission, true, nil
}

func (m *mongoCampaignMissions) ExpireStale(ctx context.Context, now time.Time) (int, error) {
	res, err := m.coll.UpdateMany(ctx, bson.D{
		{Key: "status", Value: api.CampaignMissionStatusRunning},
		{Key: "expires_at", Value: bson.D{{Key: "$lte", Value: now}}},
	}, bson.D{{Key: "$set", Value: bson.D{
		{Key: "status", Value: api.CampaignMissionStatusExpired},
		{Key: "updated_at", Value: now},
	}}})
	if err != nil {
		return 0, err
	}
	return int(res.ModifiedCount), nil
}

func (m *mongoCampaignMissions) transition(ctx context.Context, filter bson.D, update bson.D) (api.CampaignMission, bool, error) {
	var mission api.CampaignMission
	err := m.coll.FindOneAndUpdate(ctx, filter, update, options.FindOneAndUpdate().SetReturnDocument(options.After)).Decode(&mission)
	if errors.Is(err, mongodriver.ErrNoDocuments) {
		return api.CampaignMission{}, false, nil
	}
	if err != nil {
		return api.CampaignMission{}, false, err
	}
	return mission, true, nil
}

// campaignMissionPurge sets a retention window on a terminal mission so the TTL
// index reaps it later while proof/history remains available in the meantime.
func campaignMissionPurge(now time.Time) time.Time {
	return now.Add(90 * 24 * time.Hour)
}
