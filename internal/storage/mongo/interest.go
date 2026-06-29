package mongo

import (
	"context"
	"errors"
	"fmt"
	"time"

	"bottrade/internal/interest"
	"go.mongodb.org/mongo-driver/v2/bson"
	mongodriver "go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

type InterestRepository struct {
	coll *mongodriver.Collection
}

func (r *InterestRepository) All(ctx context.Context) ([]interest.Record, error) {
	cur, err := r.coll.Find(ctx, bson.D{}, options.Find().SetSort(bson.D{{Key: "created_at", Value: -1}}).SetLimit(500))
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	var out []interest.Record
	err = cur.All(ctx, &out)
	return out, err
}
func (r *InterestRepository) SetInvite(ctx context.Context, email, hash string, expires, invited time.Time) error {
	_, err := r.coll.UpdateOne(ctx, bson.M{"email": email}, bson.M{"$set": bson.M{"invite_hash": hash, "invite_expires_at": expires, "invited_at": invited, "status": "invited"}})
	return err
}
func (r *InterestRepository) FindByInviteHash(ctx context.Context, hash string) (interest.Record, error) {
	var rec interest.Record
	err := r.coll.FindOne(ctx, bson.M{"invite_hash": hash, "status": "invited"}).Decode(&rec)
	if errors.Is(err, mongodriver.ErrNoDocuments) {
		return rec, errors.New("interest: invite not found")
	}
	return rec, err
}
func (r *InterestRepository) MarkRegistered(ctx context.Context, email string) error {
	_, err := r.coll.UpdateOne(ctx, bson.M{"email": email}, bson.M{"$set": bson.M{"status": "registered"}, "$unset": bson.M{"invite_hash": ""}})
	return err
}
func (r *InterestRepository) MarkWaitlisted(ctx context.Context, email string, at time.Time) error {
	_, err := r.coll.UpdateOne(ctx, bson.M{"email": email}, bson.M{
		"$set":   bson.M{"status": "waitlisted", "waitlisted_at": at},
		"$unset": bson.M{"invite_hash": "", "invite_expires_at": ""},
	})
	return err
}

func (s *Store) Interest() *InterestRepository {
	return &InterestRepository{coll: s.interest}
}

func (r *InterestRepository) Create(ctx context.Context, record interest.Record) error {
	if _, err := r.coll.InsertOne(ctx, record); err != nil {
		if mongodriver.IsDuplicateKeyError(err) {
			return interest.ErrAlreadyRegistered
		}
		return fmt.Errorf("insert interest: %w", err)
	}
	return nil
}
