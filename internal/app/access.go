package app

import (
	"context"
	"errors"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	mongodriver "go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"bottrade/internal/api"
	"bottrade/internal/telegram"
)

// crewAdmin adapts the access store to the Telegram handler's CrewAdmin, so the
// bot's /pending and /approve commands act on the same approvals as the web.
type crewAdmin struct{ store api.AccessStore }

func (c crewAdmin) Pending(ctx context.Context) ([]telegram.CrewMember, error) {
	recs, err := c.store.Pending(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]telegram.CrewMember, 0, len(recs))
	for _, r := range recs {
		out = append(out, telegram.CrewMember{Subject: r.Subject, Name: r.Name})
	}
	return out, nil
}

func (c crewAdmin) Approve(ctx context.Context, subject string) error {
	return c.store.Approve(ctx, subject)
}

// mongoAccess persists crew-access approvals in MongoDB so they survive restarts
// and are shared across api instances. It implements api.AccessStore. Records
// are keyed by JWT subject (_id).
type mongoAccess struct {
	coll *mongodriver.Collection
}

func newMongoAccess(coll *mongodriver.Collection) *mongoAccess {
	return &mongoAccess{coll: coll}
}

func (m *mongoAccess) Get(ctx context.Context, subject string) (api.AccessRecord, bool, error) {
	var rec api.AccessRecord
	err := m.coll.FindOne(ctx, bson.D{{Key: "_id", Value: subject}}).Decode(&rec)
	if errors.Is(err, mongodriver.ErrNoDocuments) {
		return api.AccessRecord{}, false, nil
	}
	if err != nil {
		return api.AccessRecord{}, false, err
	}
	return rec, true, nil
}

func (m *mongoAccess) Request(ctx context.Context, subject, name string) error {
	// Don't downgrade an already-approved user.
	filter := bson.D{{Key: "_id", Value: subject}, {Key: "status", Value: bson.D{{Key: "$ne", Value: "approved"}}}}
	update := bson.D{{Key: "$set", Value: bson.D{
		{Key: "name", Value: name},
		{Key: "status", Value: "requested"},
		{Key: "requested_at", Value: time.Now().UTC()},
	}}}
	_, err := m.coll.UpdateOne(ctx, filter, update, options.UpdateOne().SetUpsert(true))
	if mongodriver.IsDuplicateKeyError(err) {
		return nil // already exists and is approved
	}
	return err
}

func (m *mongoAccess) Approve(ctx context.Context, subject string) error {
	update := bson.D{{Key: "$set", Value: bson.D{
		{Key: "status", Value: "approved"},
		{Key: "approved_at", Value: time.Now().UTC()},
	}}}
	_, err := m.coll.UpdateOne(ctx, bson.D{{Key: "_id", Value: subject}}, update, options.UpdateOne().SetUpsert(true))
	return err
}

func (m *mongoAccess) Pending(ctx context.Context) ([]api.AccessRecord, error) {
	opts := options.Find().SetSort(bson.D{{Key: "requested_at", Value: 1}}).SetLimit(200)
	cursor, err := m.coll.Find(ctx, bson.D{{Key: "status", Value: "requested"}}, opts)
	if err != nil {
		return nil, err
	}
	defer cursor.Close(ctx)
	var recs []api.AccessRecord
	if err := cursor.All(ctx, &recs); err != nil {
		return nil, err
	}
	return recs, nil
}
