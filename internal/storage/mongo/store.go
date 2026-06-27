package mongo

import (
	"context"
	"fmt"
	"strings"

	"go.mongodb.org/mongo-driver/v2/bson"
	mongodriver "go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"go.mongodb.org/mongo-driver/v2/mongo/readpref"
)

type Config struct {
	URI      string
	Database string
}

type Store struct {
	client        *mongodriver.Client
	confirmations *mongodriver.Collection
	orderIntents  *mongodriver.Collection
	signals       *mongodriver.Collection
	auditEvents   *mongodriver.Collection
	users         *mongodriver.Collection
	journalTrades *mongodriver.Collection
}

func Connect(ctx context.Context, cfg Config) (*Store, error) {
	uri := strings.TrimSpace(cfg.URI)
	if uri == "" {
		return nil, fmt.Errorf("MONGODB_URI is required")
	}

	database := strings.TrimSpace(cfg.Database)
	if database == "" {
		return nil, fmt.Errorf("MONGODB_DATABASE is required")
	}

	client, err := mongodriver.Connect(options.Client().ApplyURI(uri))
	if err != nil {
		return nil, fmt.Errorf("connect mongodb: %w", err)
	}
	if err := client.Ping(ctx, readpref.Primary()); err != nil {
		_ = client.Disconnect(ctx)
		return nil, fmt.Errorf("ping mongodb: %w", err)
	}

	db := client.Database(database)
	store := &Store{
		client:        client,
		confirmations: db.Collection("confirmations"),
		orderIntents:  db.Collection("order_intents"),
		signals:       db.Collection("signals"),
		auditEvents:   db.Collection("audit_events"),
		users:         db.Collection("users"),
		journalTrades: db.Collection("journal_trades"),
	}
	if err := store.ensureIndexes(ctx); err != nil {
		_ = client.Disconnect(ctx)
		return nil, err
	}

	return store, nil
}

func (s *Store) Disconnect(ctx context.Context) error {
	if s == nil || s.client == nil {
		return nil
	}
	return s.client.Disconnect(ctx)
}

func (s *Store) ensureIndexes(ctx context.Context) error {
	_, err := s.confirmations.Indexes().CreateMany(ctx, []mongodriver.IndexModel{
		{
			Keys: bson.D{
				{Key: "user_id", Value: 1},
				{Key: "status", Value: 1},
				{Key: "created_at", Value: -1},
			},
			Options: options.Index().SetName("user_status_created_at"),
		},
		{
			Keys: bson.D{{Key: "expires_at", Value: 1}},
			Options: options.Index().
				SetName("expires_at_ttl").
				SetExpireAfterSeconds(0),
		},
		{
			Keys: bson.D{{Key: "idempotency_key", Value: 1}},
			Options: options.Index().
				SetName("idempotency_key_unique").
				SetUnique(true).
				SetSparse(true),
		},
	})
	if err != nil {
		return fmt.Errorf("create confirmation indexes: %w", err)
	}

	_, err = s.orderIntents.Indexes().CreateMany(ctx, []mongodriver.IndexModel{
		{
			Keys: bson.D{
				{Key: "user_id", Value: 1},
				{Key: "created_at", Value: -1},
			},
			Options: options.Index().SetName("intent_user_created_at"),
		},
		{
			Keys: bson.D{
				{Key: "status", Value: 1},
				{Key: "created_at", Value: -1},
			},
			Options: options.Index().SetName("intent_status_created_at"),
		},
		{
			Keys:    bson.D{{Key: "intent_hash", Value: 1}},
			Options: options.Index().SetName("intent_hash"),
		},
		{
			Keys: bson.D{
				{Key: "user_id", Value: 1},
				{Key: "plan_id", Value: 1},
				{Key: "status", Value: 1},
			},
			Options: options.Index().SetName("intent_user_plan_status"),
		},
	})
	if err != nil {
		return fmt.Errorf("create order intent indexes: %w", err)
	}

	_, err = s.signals.Indexes().CreateMany(ctx, []mongodriver.IndexModel{
		{
			Keys: bson.D{
				{Key: "symbol", Value: 1},
				{Key: "created_at", Value: -1},
			},
			Options: options.Index().SetName("signal_symbol_created_at"),
		},
		{
			Keys: bson.D{
				{Key: "status", Value: 1},
				{Key: "created_at", Value: -1},
			},
			Options: options.Index().SetName("signal_status_created_at"),
		},
	})
	if err != nil {
		return fmt.Errorf("create signal indexes: %w", err)
	}

	_, err = s.auditEvents.Indexes().CreateMany(ctx, []mongodriver.IndexModel{
		{
			Keys: bson.D{
				{Key: "user_id", Value: 1},
				{Key: "created_at", Value: -1},
			},
			Options: options.Index().SetName("audit_user_created_at"),
		},
		{
			Keys:    bson.D{{Key: "correlation_id", Value: 1}},
			Options: options.Index().SetName("audit_correlation_id"),
		},
	})
	if err != nil {
		return fmt.Errorf("create audit indexes: %w", err)
	}

	_, err = s.users.Indexes().CreateOne(ctx, mongodriver.IndexModel{
		Keys:    bson.D{{Key: "username", Value: 1}},
		Options: options.Index().SetName("username_unique").SetUnique(true),
	})
	if err != nil {
		return fmt.Errorf("create user indexes: %w", err)
	}

	_, err = s.journalTrades.Indexes().CreateMany(ctx, []mongodriver.IndexModel{
		{
			Keys: bson.D{
				{Key: "user_id", Value: 1},
				{Key: "closed_at", Value: -1},
			},
			Options: options.Index().SetName("journal_user_closed_at"),
		},
		{
			Keys:    bson.D{{Key: "campaign_id", Value: 1}},
			Options: options.Index().SetName("journal_campaign").SetSparse(true),
		},
	})
	if err != nil {
		return fmt.Errorf("create journal indexes: %w", err)
	}

	return nil
}
