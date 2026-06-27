// Package users provides account registration and password authentication so
// the bot can serve more than one trader (e.g. the owner and their partner),
// which in turn produces more trades for the journal's statistics. Each user
// later attaches their own encrypted Binance credentials via internal/auth.
package users

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
)

const minPasswordLength = 8

var (
	// ErrNotFound is returned by a Repository when no user matches.
	ErrNotFound = errors.New("users: user not found")
	// ErrUsernameTaken is returned when registering a username that exists.
	ErrUsernameTaken = errors.New("users: username already taken")
	// ErrInvalidLogin is returned for any failed authentication. It is
	// deliberately identical for unknown-user and wrong-password so the API does
	// not leak which usernames exist.
	ErrInvalidLogin = errors.New("users: invalid username or password")
)

// Role gates what a user may do.
type Role string

const (
	RoleUser  Role = "user"
	RoleAdmin Role = "admin"
)

// User is an account. PasswordHash is a bcrypt hash and is never serialised to
// clients (json:"-").
type User struct {
	ID           string    `bson:"_id" json:"id"`
	Username     string    `bson:"username" json:"username"`
	PasswordHash string    `bson:"password_hash" json:"-"`
	Role         Role      `bson:"role" json:"role"`
	CreatedAt    time.Time `bson:"created_at" json:"created_at"`
}

// Repository persists users. Create must reject a duplicate username with
// ErrUsernameTaken; FindByUsername must return ErrNotFound when absent.
type Repository interface {
	Create(ctx context.Context, user User) error
	FindByUsername(ctx context.Context, username string) (User, error)
}

// Service registers and authenticates users.
type Service struct {
	repo Repository
	now  func() time.Time
	idFn func() string
}

// Option customises a Service (used by tests for deterministic id/clock).
type Option func(*Service)

// WithClock overrides the timestamp source.
func WithClock(now func() time.Time) Option { return func(s *Service) { s.now = now } }

// WithIDGenerator overrides the id source.
func WithIDGenerator(fn func() string) Option { return func(s *Service) { s.idFn = fn } }

// NewService wires a repository.
func NewService(repo Repository, opts ...Option) (*Service, error) {
	if repo == nil {
		return nil, errors.New("users: repository is required")
	}
	service := &Service{repo: repo, now: time.Now, idFn: uuid.NewString}
	for _, opt := range opts {
		opt(service)
	}
	return service, nil
}

// Register validates input, hashes the password, and creates the account.
func (s *Service) Register(ctx context.Context, username, password string) (User, error) {
	username = normalizeUsername(username)
	if username == "" {
		return User{}, errors.New("users: username is required")
	}
	if len(password) < minPasswordLength {
		return User{}, fmt.Errorf("users: password must be at least %d characters", minPasswordLength)
	}

	switch _, err := s.repo.FindByUsername(ctx, username); {
	case err == nil:
		return User{}, ErrUsernameTaken
	case !errors.Is(err, ErrNotFound):
		return User{}, err
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return User{}, fmt.Errorf("users: hash password: %w", err)
	}

	user := User{
		ID:           s.idFn(),
		Username:     username,
		PasswordHash: string(hash),
		Role:         RoleUser,
		CreatedAt:    s.now(),
	}
	if err := s.repo.Create(ctx, user); err != nil {
		return User{}, err
	}
	return user, nil
}

// Authenticate verifies a username/password pair. It returns ErrInvalidLogin for
// both an unknown user and a wrong password (no account enumeration).
func (s *Service) Authenticate(ctx context.Context, username, password string) (User, error) {
	user, err := s.repo.FindByUsername(ctx, normalizeUsername(username))
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return User{}, ErrInvalidLogin
		}
		return User{}, err
	}
	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(password)); err != nil {
		return User{}, ErrInvalidLogin
	}
	return user, nil
}

func normalizeUsername(username string) string {
	return strings.ToLower(strings.TrimSpace(username))
}
