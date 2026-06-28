package mongo

import (
	"context"
	"errors"
	"fmt"

	"go.mongodb.org/mongo-driver/v2/bson"
	mongodriver "go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"bottrade/internal/auth"
)

// CredentialRepository persists per-user encrypted Binance credential profiles.
// It implements auth.CredentialRepository and only ever stores Sealed values.
// Records are keyed by (user_id, profile).
type CredentialRepository struct {
	coll *mongodriver.Collection
}

// Credentials returns the MongoDB-backed credential repository.
func (s *Store) Credentials() *CredentialRepository {
	return &CredentialRepository{coll: s.credentials}
}

// Save upserts a sealed credential profile by (user_id, profile).
func (r *CredentialRepository) Save(ctx context.Context, cred auth.BinanceCredential) error {
	filter := bson.M{"user_id": cred.UserID, "profile": cred.Profile}
	if _, err := r.coll.ReplaceOne(ctx, filter, cred, options.Replace().SetUpsert(true)); err != nil {
		return fmt.Errorf("save credential: %w", err)
	}
	return nil
}

// List returns all of a user's credential profiles.
func (r *CredentialRepository) List(ctx context.Context, userID string) ([]auth.BinanceCredential, error) {
	cursor, err := r.coll.Find(ctx, bson.M{"user_id": userID})
	if err != nil {
		return nil, fmt.Errorf("list credentials: %w", err)
	}
	var creds []auth.BinanceCredential
	if err := cursor.All(ctx, &creds); err != nil {
		return nil, fmt.Errorf("list credentials: %w", err)
	}
	return creds, nil
}

// FindActive returns the user's active sealed credential or auth.ErrNoCredential.
func (r *CredentialRepository) FindActive(ctx context.Context, userID string) (auth.BinanceCredential, error) {
	var cred auth.BinanceCredential
	err := r.coll.FindOne(ctx, bson.M{"user_id": userID, "active": true}).Decode(&cred)
	if errors.Is(err, mongodriver.ErrNoDocuments) {
		return auth.BinanceCredential{}, auth.ErrNoCredential
	}
	if err != nil {
		return auth.BinanceCredential{}, fmt.Errorf("find active credential: %w", err)
	}
	return cred, nil
}

// Remove deletes one of a user's credential profiles.
func (r *CredentialRepository) Remove(ctx context.Context, userID, profile string) error {
	if _, err := r.coll.DeleteOne(ctx, profileFilter(userID, profile)); err != nil {
		return fmt.Errorf("remove credential: %w", err)
	}
	return nil
}

// SetActive marks one profile active and the user's others inactive.
func (r *CredentialRepository) SetActive(ctx context.Context, userID, profile string) error {
	if _, err := r.coll.UpdateMany(ctx, bson.M{"user_id": userID}, bson.M{"$set": bson.M{"active": false}}); err != nil {
		return fmt.Errorf("clear active credential: %w", err)
	}
	if _, err := r.coll.UpdateOne(ctx, profileFilter(userID, profile), bson.M{"$set": bson.M{"active": true}}); err != nil {
		return fmt.Errorf("set active credential: %w", err)
	}
	return nil
}

// profileFilter matches a stored profile by name. A legacy "unnamed" profile
// (empty name) may have been written before the profile field existed, so in
// Mongo it can be empty, null, or absent — match all three so it can still be
// removed or activated.
func profileFilter(userID, profile string) bson.M {
	if profile == "" {
		return bson.M{"user_id": userID, "$or": []bson.M{
			{"profile": ""},
			{"profile": nil},
			{"profile": bson.M{"$exists": false}},
		}}
	}
	return bson.M{"user_id": userID, "profile": profile}
}
