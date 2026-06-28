package app

import (
	"context"

	"go.mongodb.org/mongo-driver/v2/bson"
	mongodriver "go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"bottrade/internal/api"
)

// mongoGoalRuns persists paper goal runs in MongoDB so the dashboard's goal
// history survives restarts and is shared across api instances. It implements
// api.GoalRunStore. The adapter lives here (top-level app) so the storage
// package stays free of the transport layer's record type.
type mongoGoalRuns struct {
	coll *mongodriver.Collection
}

func newMongoGoalRuns(coll *mongodriver.Collection) *mongoGoalRuns {
	return &mongoGoalRuns{coll: coll}
}

func (m *mongoGoalRuns) Save(ctx context.Context, run api.GoalRun) error {
	_, err := m.coll.InsertOne(ctx, run)
	return err
}

func (m *mongoGoalRuns) List(ctx context.Context, userKey string, limit int) ([]api.GoalRun, error) {
	if limit <= 0 {
		limit = 50
	}
	opts := options.Find().
		SetSort(bson.D{{Key: "created_at", Value: -1}}).
		SetLimit(int64(limit))
	cursor, err := m.coll.Find(ctx, bson.D{{Key: "user_key", Value: userKey}}, opts)
	if err != nil {
		return nil, err
	}
	defer cursor.Close(ctx)
	var runs []api.GoalRun
	if err := cursor.All(ctx, &runs); err != nil {
		return nil, err
	}
	return runs, nil
}

// Community returns the most recent runs across all users for aggregate stats.
func (m *mongoGoalRuns) Community(ctx context.Context, limit int) ([]api.GoalRun, error) {
	if limit <= 0 {
		limit = 1000
	}
	opts := options.Find().
		SetSort(bson.D{{Key: "created_at", Value: -1}}).
		SetLimit(int64(limit))
	cursor, err := m.coll.Find(ctx, bson.D{}, opts)
	if err != nil {
		return nil, err
	}
	defer cursor.Close(ctx)
	var runs []api.GoalRun
	if err := cursor.All(ctx, &runs); err != nil {
		return nil, err
	}
	return runs, nil
}
