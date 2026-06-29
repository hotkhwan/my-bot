// Package interest records email addresses from people who want product updates.
// An interest record is not an account and grants no access to trading features.
package interest

import (
	"context"
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"time"
)

var ErrAlreadyRegistered = errors.New("interest: email already registered")

type Record struct {
	Email     string    `bson:"email" json:"email"`
	Source    string    `bson:"source" json:"source"`
	CreatedAt time.Time `bson:"created_at" json:"created_at"`
}

type Repository interface {
	Create(context.Context, Record) error
}

type Service struct {
	repo Repository
	now  func() time.Time
}

func NewService(repo Repository) (*Service, error) {
	if repo == nil {
		return nil, errors.New("interest: repository is required")
	}
	return &Service{repo: repo, now: time.Now}, nil
}

func (s *Service) Register(ctx context.Context, email, source string) (Record, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	address, err := mail.ParseAddress(email)
	if err != nil || address.Address != email || len(email) > 254 {
		return Record{}, errors.New("interest: valid email is required")
	}
	source = strings.TrimSpace(source)
	if source == "" {
		source = "login"
	}
	if len(source) > 50 {
		return Record{}, errors.New("interest: invalid source")
	}
	record := Record{Email: email, Source: source, CreatedAt: s.now().UTC()}
	if err := s.repo.Create(ctx, record); err != nil {
		return Record{}, fmt.Errorf("save interest: %w", err)
	}
	return record, nil
}
