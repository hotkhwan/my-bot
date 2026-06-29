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
	Email           string    `bson:"email" json:"email"`
	Reason          string    `bson:"reason,omitempty" json:"reason,omitempty"`
	Source          string    `bson:"source" json:"source"`
	Status          string    `bson:"status" json:"status"`
	InviteHash      string    `bson:"invite_hash,omitempty" json:"-"`
	InviteExpiresAt time.Time `bson:"invite_expires_at,omitempty" json:"-"`
	InvitedAt       time.Time `bson:"invited_at,omitempty" json:"invited_at,omitempty"`
	CreatedAt       time.Time `bson:"created_at" json:"created_at"`
}

type Repository interface {
	Create(context.Context, Record) error
	All(context.Context) ([]Record, error)
	SetInvite(context.Context, string, string, time.Time, time.Time) error
	FindByInviteHash(context.Context, string) (Record, error)
	MarkRegistered(context.Context, string) error
	MarkWaitlisted(context.Context, string, time.Time) error
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

func (s *Service) Register(ctx context.Context, email, reason, source string) (Record, error) {
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
	reason = strings.TrimSpace(reason)
	if len(reason) > 1000 {
		return Record{}, errors.New("interest: reason is too long")
	}
	record := Record{Email: email, Reason: reason, Source: source, Status: "new", CreatedAt: s.now().UTC()}
	if err := s.repo.Create(ctx, record); err != nil {
		return Record{}, fmt.Errorf("save interest: %w", err)
	}
	return record, nil
}

func (s *Service) All(ctx context.Context) ([]Record, error) { return s.repo.All(ctx) }
func (s *Service) SetInvite(ctx context.Context, email, hash string, expires time.Time) error {
	return s.repo.SetInvite(ctx, email, hash, expires, s.now().UTC())
}
func (s *Service) FindInvite(ctx context.Context, hash string) (Record, error) {
	rec, err := s.repo.FindByInviteHash(ctx, hash)
	if err == nil && s.now().After(rec.InviteExpiresAt) {
		return Record{}, errors.New("interest: invite expired")
	}
	return rec, err
}
func (s *Service) MarkRegistered(ctx context.Context, email string) error {
	return s.repo.MarkRegistered(ctx, email)
}
func (s *Service) MarkWaitlisted(ctx context.Context, email string) error {
	return s.repo.MarkWaitlisted(ctx, email, s.now().UTC())
}
