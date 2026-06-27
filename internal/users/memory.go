package users

import (
	"context"
	"sync"
)

// MemoryRepository is an in-memory Repository for tests and for running before
// the MongoDB-backed repository is wired.
type MemoryRepository struct {
	mu     sync.Mutex
	byName map[string]User
}

// NewMemoryRepository returns an empty repository.
func NewMemoryRepository() *MemoryRepository {
	return &MemoryRepository{byName: make(map[string]User)}
}

// Create stores the user, rejecting a duplicate username.
func (r *MemoryRepository) Create(_ context.Context, user User) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.byName[user.Username]; ok {
		return ErrUsernameTaken
	}
	r.byName[user.Username] = user
	return nil
}

// FindByUsername returns the user or ErrNotFound.
func (r *MemoryRepository) FindByUsername(_ context.Context, username string) (User, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	user, ok := r.byName[username]
	if !ok {
		return User{}, ErrNotFound
	}
	return user, nil
}
