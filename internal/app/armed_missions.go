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

// mongoArmedMissions persists wait-for-setup mission state so arming survives
// API restarts. It implements api.ArmedMissionStore without making storage
// depend on the transport-layer record.
type mongoArmedMissions struct {
	coll *mongodriver.Collection
}

func newMongoArmedMissions(coll *mongodriver.Collection) *mongoArmedMissions {
	return &mongoArmedMissions{coll: coll}
}

func (m *mongoArmedMissions) Save(ctx context.Context, mission api.ArmedMission) error {
	_, err := m.coll.InsertOne(ctx, mission)
	return err
}

func (m *mongoArmedMissions) Get(ctx context.Context, id string) (api.ArmedMission, bool, error) {
	var mission api.ArmedMission
	err := m.coll.FindOne(ctx, bson.D{{Key: "_id", Value: id}}).Decode(&mission)
	if errors.Is(err, mongodriver.ErrNoDocuments) {
		return api.ArmedMission{}, false, nil
	}
	if err != nil {
		return api.ArmedMission{}, false, err
	}
	return mission, true, nil
}

func (m *mongoArmedMissions) ListActive(ctx context.Context, now time.Time) ([]api.ArmedMission, error) {
	cursor, err := m.coll.Find(ctx, bson.D{
		{Key: "status", Value: api.ArmedMissionStatusArmed},
		{Key: "expires_at", Value: bson.D{{Key: "$gt", Value: now}}},
	}, options.Find().SetSort(bson.D{{Key: "expires_at", Value: 1}}).SetLimit(1000))
	if err != nil {
		return nil, err
	}
	defer cursor.Close(ctx)
	var rows []api.ArmedMission
	if err := cursor.All(ctx, &rows); err != nil {
		return nil, err
	}
	return rows, nil
}

func (m *mongoArmedMissions) ListUser(ctx context.Context, userKey string, limit int) ([]api.ArmedMission, error) {
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
	var rows []api.ArmedMission
	if err := cursor.All(ctx, &rows); err != nil {
		return nil, err
	}
	return rows, nil
}

func (m *mongoArmedMissions) Disarm(ctx context.Context, userKey, id string, now time.Time) (api.ArmedMission, bool, error) {
	mission, ok, err := m.transition(ctx, bson.D{
		{Key: "_id", Value: id},
		{Key: "user_key", Value: userKey},
		{Key: "status", Value: api.ArmedMissionStatusArmed},
	}, bson.D{{Key: "$set", Value: bson.D{
		{Key: "status", Value: api.ArmedMissionStatusDisarmed},
		{Key: "updated_at", Value: now},
	}}})
	if err != nil || ok {
		return mission, ok, err
	}
	mission, ok, err = m.Get(ctx, id)
	if err != nil || !ok || mission.UserKey != userKey {
		return api.ArmedMission{}, false, err
	}
	return mission, true, nil
}

func (m *mongoArmedMissions) MarkExpired(ctx context.Context, id string, now time.Time) (api.ArmedMission, bool, error) {
	return m.transition(ctx, bson.D{
		{Key: "_id", Value: id},
		{Key: "status", Value: api.ArmedMissionStatusArmed},
	}, bson.D{{Key: "$set", Value: bson.D{
		{Key: "status", Value: api.ArmedMissionStatusExpired},
		{Key: "updated_at", Value: now},
	}}})
}

func (m *mongoArmedMissions) MarkTriggered(ctx context.Context, id, side, reason, confirmationID string, now time.Time) (api.ArmedMission, bool, error) {
	return m.transition(ctx, bson.D{
		{Key: "_id", Value: id},
		{Key: "status", Value: api.ArmedMissionStatusArmed},
		{Key: "expires_at", Value: bson.D{{Key: "$gt", Value: now}}},
	}, bson.D{
		{Key: "$set", Value: bson.D{
			{Key: "status", Value: api.ArmedMissionStatusTriggered},
			{Key: "side", Value: side},
			{Key: "trigger_reason", Value: reason},
			{Key: "triggered_confirmation_id", Value: confirmationID},
			{Key: "triggered_at", Value: now},
			{Key: "updated_at", Value: now},
		}},
		{Key: "$unset", Value: bson.D{{Key: "purge_at", Value: ""}}},
	})
}

func (m *mongoArmedMissions) SetTriggeredConfirmation(ctx context.Context, id, confirmationID string, now time.Time) (api.ArmedMission, bool, error) {
	return m.transition(ctx, bson.D{
		{Key: "_id", Value: id},
		{Key: "status", Value: api.ArmedMissionStatusTriggered},
		{Key: "$or", Value: bson.A{
			bson.D{{Key: "triggered_confirmation_id", Value: bson.D{{Key: "$exists", Value: false}}}},
			bson.D{{Key: "triggered_confirmation_id", Value: ""}},
			bson.D{{Key: "triggered_confirmation_id", Value: confirmationID}},
		}},
	}, bson.D{{Key: "$set", Value: bson.D{
		{Key: "triggered_confirmation_id", Value: confirmationID},
		{Key: "updated_at", Value: now},
	}}})
}

func (m *mongoArmedMissions) transition(ctx context.Context, filter bson.D, update bson.D) (api.ArmedMission, bool, error) {
	var mission api.ArmedMission
	err := m.coll.FindOneAndUpdate(ctx, filter, update, options.FindOneAndUpdate().SetReturnDocument(options.After)).Decode(&mission)
	if errors.Is(err, mongodriver.ErrNoDocuments) {
		return api.ArmedMission{}, false, nil
	}
	if err != nil {
		return api.ArmedMission{}, false, err
	}
	return mission, true, nil
}
