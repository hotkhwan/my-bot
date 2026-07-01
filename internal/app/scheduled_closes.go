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

// mongoScheduledCloses persists plan-end close jobs so Mission cleanup survives
// API restarts. It mirrors the API store interface without making storage
// depend on transport concerns.
type mongoScheduledCloses struct {
	coll *mongodriver.Collection
}

func newMongoScheduledCloses(coll *mongodriver.Collection) *mongoScheduledCloses {
	return &mongoScheduledCloses{coll: coll}
}

func (m *mongoScheduledCloses) Save(ctx context.Context, close api.ScheduledClose) error {
	_, err := m.coll.InsertOne(ctx, close)
	return err
}

func (m *mongoScheduledCloses) ListDue(ctx context.Context, now time.Time, limit int) ([]api.ScheduledClose, error) {
	if limit <= 0 {
		limit = 100
	}
	cursor, err := m.coll.Find(ctx, scheduledCloseClaimFilter("", now),
		options.Find().SetSort(bson.D{{Key: "due_at", Value: 1}}).SetLimit(int64(limit)))
	if err != nil {
		return nil, err
	}
	defer cursor.Close(ctx)
	var rows []api.ScheduledClose
	if err := cursor.All(ctx, &rows); err != nil {
		return nil, err
	}
	return rows, nil
}

func (m *mongoScheduledCloses) ClaimDue(ctx context.Context, id string, now time.Time) (api.ScheduledClose, bool, error) {
	return m.transition(ctx, scheduledCloseClaimFilter(id, now), bson.D{{Key: "$set", Value: bson.D{
		{Key: "status", Value: api.ScheduledCloseStatusExecuting},
		{Key: "updated_at", Value: now},
	}}})
}

func scheduledCloseClaimFilter(id string, now time.Time) bson.D {
	staleBefore := now.Add(-api.ScheduledCloseClaimTimeout)
	filter := bson.D{}
	if id != "" {
		filter = append(filter, bson.E{Key: "_id", Value: id})
	}
	return append(filter, bson.E{Key: "$or", Value: bson.A{
		bson.D{
			{Key: "status", Value: api.ScheduledCloseStatusPending},
			{Key: "due_at", Value: bson.D{{Key: "$lte", Value: now}}},
		},
		bson.D{
			{Key: "status", Value: api.ScheduledCloseStatusExecuting},
			{Key: "due_at", Value: bson.D{{Key: "$lte", Value: now}}},
			{Key: "updated_at", Value: bson.D{{Key: "$lte", Value: staleBefore}}},
		},
	}})
}

func (m *mongoScheduledCloses) MarkDone(ctx context.Context, id, confirmationID, reason string, now time.Time) (api.ScheduledClose, bool, error) {
	return m.markTerminal(ctx, id, api.ScheduledCloseStatusDone, confirmationID, reason, now)
}

func (m *mongoScheduledCloses) MarkSkipped(ctx context.Context, id, confirmationID, reason string, now time.Time) (api.ScheduledClose, bool, error) {
	return m.markTerminal(ctx, id, api.ScheduledCloseStatusSkipped, confirmationID, reason, now)
}

func (m *mongoScheduledCloses) MarkCancelled(ctx context.Context, id, reason string, now time.Time) (api.ScheduledClose, bool, error) {
	return m.transition(ctx, bson.D{
		{Key: "_id", Value: id},
		{Key: "status", Value: bson.D{{Key: "$in", Value: bson.A{
			api.ScheduledCloseStatusPending,
			api.ScheduledCloseStatusExecuting,
		}}}},
	}, bson.D{{Key: "$set", Value: bson.D{
		{Key: "status", Value: api.ScheduledCloseStatusCancelled},
		{Key: "reason", Value: reason},
		{Key: "updated_at", Value: now},
		{Key: "purge_at", Value: terminalScheduledClosePurgeAt(now)},
	}}})
}

func (m *mongoScheduledCloses) markTerminal(ctx context.Context, id string, status api.ScheduledCloseStatus, confirmationID, reason string, now time.Time) (api.ScheduledClose, bool, error) {
	return m.transition(ctx, bson.D{
		{Key: "_id", Value: id},
		{Key: "status", Value: api.ScheduledCloseStatusExecuting},
	}, bson.D{{Key: "$set", Value: bson.D{
		{Key: "status", Value: status},
		{Key: "confirmation_id", Value: confirmationID},
		{Key: "reason", Value: reason},
		{Key: "updated_at", Value: now},
		{Key: "purge_at", Value: terminalScheduledClosePurgeAt(now)},
	}}})
}

func (m *mongoScheduledCloses) ActivateByEntryConfirmation(ctx context.Context, entryConfirmationID string, now time.Time) (api.ScheduledClose, bool, error) {
	// Pipeline update so due_at = now + window_seconds is computed from the row's own
	// field atomically (the entry may confirm minutes after prepare).
	return m.transition(ctx, bson.D{
		{Key: "entry_confirmation_id", Value: entryConfirmationID},
		{Key: "status", Value: api.ScheduledCloseStatusAwaitingEntry},
	}, mongodriver.Pipeline{
		{{Key: "$set", Value: bson.D{
			{Key: "status", Value: api.ScheduledCloseStatusPending},
			{Key: "updated_at", Value: now},
			{Key: "due_at", Value: bson.D{{Key: "$add", Value: bson.A{
				now, bson.D{{Key: "$multiply", Value: bson.A{"$window_seconds", int64(1000)}}},
			}}}},
		}}},
	})
}

func (m *mongoScheduledCloses) CancelByEntryConfirmation(ctx context.Context, entryConfirmationID, reason string, now time.Time) (api.ScheduledClose, bool, error) {
	return m.transition(ctx, bson.D{
		{Key: "entry_confirmation_id", Value: entryConfirmationID},
		{Key: "status", Value: bson.D{{Key: "$in", Value: bson.A{
			api.ScheduledCloseStatusAwaitingEntry,
			api.ScheduledCloseStatusPending,
		}}}},
	}, bson.D{{Key: "$set", Value: bson.D{
		{Key: "status", Value: api.ScheduledCloseStatusCancelled},
		{Key: "reason", Value: reason},
		{Key: "updated_at", Value: now},
		{Key: "purge_at", Value: terminalScheduledClosePurgeAt(now)},
	}}})
}

func (m *mongoScheduledCloses) ListAwaitingEntry(ctx context.Context, limit int) ([]api.ScheduledClose, error) {
	if limit <= 0 {
		limit = 100
	}
	cursor, err := m.coll.Find(ctx, bson.D{{Key: "status", Value: api.ScheduledCloseStatusAwaitingEntry}},
		options.Find().SetSort(bson.D{{Key: "created_at", Value: 1}}).SetLimit(int64(limit)))
	if err != nil {
		return nil, err
	}
	defer cursor.Close(ctx)
	var rows []api.ScheduledClose
	if err := cursor.All(ctx, &rows); err != nil {
		return nil, err
	}
	return rows, nil
}

func terminalScheduledClosePurgeAt(now time.Time) time.Time {
	return now.Add(90 * 24 * time.Hour)
}

func (m *mongoScheduledCloses) transition(ctx context.Context, filter bson.D, update any) (api.ScheduledClose, bool, error) {
	var close api.ScheduledClose
	err := m.coll.FindOneAndUpdate(ctx, filter, update, options.FindOneAndUpdate().SetReturnDocument(options.After)).Decode(&close)
	if errors.Is(err, mongodriver.ErrNoDocuments) {
		return api.ScheduledClose{}, false, nil
	}
	if err != nil {
		return api.ScheduledClose{}, false, err
	}
	return close, true, nil
}
