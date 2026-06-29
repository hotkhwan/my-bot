package mongo

import (
	"context"
	"fmt"

	"bottrade/internal/interest"
	mongodriver "go.mongodb.org/mongo-driver/v2/mongo"
)

type InterestRepository struct {
	coll *mongodriver.Collection
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
