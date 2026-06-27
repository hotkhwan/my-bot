package mongo

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	mongodriver "go.mongodb.org/mongo-driver/v2/mongo"

	"bottrade/internal/users"
)

type userDoc struct {
	ID           string    `bson:"_id"`
	Username     string    `bson:"username"`
	PasswordHash string    `bson:"password_hash"`
	Role         string    `bson:"role"`
	CreatedAt    time.Time `bson:"created_at"`
}

// UserRepository persists accounts in MongoDB. It implements users.Repository.
type UserRepository struct {
	coll *mongodriver.Collection
}

// Users returns the MongoDB-backed user repository.
func (s *Store) Users() *UserRepository {
	return &UserRepository{coll: s.users}
}

// Create inserts a user, mapping a duplicate-username collision (enforced by the
// unique index) to users.ErrUsernameTaken.
func (r *UserRepository) Create(ctx context.Context, user users.User) error {
	_, err := r.coll.InsertOne(ctx, userDoc{
		ID:           user.ID,
		Username:     user.Username,
		PasswordHash: user.PasswordHash,
		Role:         string(user.Role),
		CreatedAt:    user.CreatedAt,
	})
	if mongodriver.IsDuplicateKeyError(err) {
		return users.ErrUsernameTaken
	}
	if err != nil {
		return fmt.Errorf("insert user: %w", err)
	}
	return nil
}

// FindByUsername returns the user or users.ErrNotFound.
func (r *UserRepository) FindByUsername(ctx context.Context, username string) (users.User, error) {
	var doc userDoc
	err := r.coll.FindOne(ctx, bson.M{"username": username}).Decode(&doc)
	if errors.Is(err, mongodriver.ErrNoDocuments) {
		return users.User{}, users.ErrNotFound
	}
	if err != nil {
		return users.User{}, fmt.Errorf("find user: %w", err)
	}
	return users.User{
		ID:           doc.ID,
		Username:     doc.Username,
		PasswordHash: doc.PasswordHash,
		Role:         users.Role(doc.Role),
		CreatedAt:    doc.CreatedAt,
	}, nil
}
