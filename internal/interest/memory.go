package interest

import (
	"context"
	"errors"
	"sort"
	"sync"
	"time"
)

type MemoryRepository struct {
	mu      sync.Mutex
	records map[string]Record
}

func (r *MemoryRepository) All(_ context.Context) ([]Record, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Record, 0, len(r.records))
	for _, rec := range r.records {
		out = append(out, rec)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}
func (r *MemoryRepository) SetInvite(_ context.Context, email, hash string, expires, invited time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	rec, ok := r.records[email]
	if !ok {
		return errors.New("interest: not found")
	}
	rec.InviteHash, rec.InviteExpiresAt, rec.InvitedAt, rec.Status = hash, expires, invited, "invited"
	r.records[email] = rec
	return nil
}
func (r *MemoryRepository) FindByInviteHash(_ context.Context, hash string) (Record, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, rec := range r.records {
		if rec.InviteHash == hash && rec.Status == "invited" {
			return rec, nil
		}
	}
	return Record{}, errors.New("interest: invite not found")
}
func (r *MemoryRepository) MarkRegistered(_ context.Context, email string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	rec := r.records[email]
	rec.Status = "registered"
	rec.InviteHash = ""
	r.records[email] = rec
	return nil
}
func (r *MemoryRepository) MarkWaitlisted(_ context.Context, email string, _ time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	rec, ok := r.records[email]
	if !ok {
		return errors.New("interest: not found")
	}
	rec.Status, rec.InviteHash = "waitlisted", ""
	r.records[email] = rec
	return nil
}

func NewMemoryRepository() *MemoryRepository {
	return &MemoryRepository{records: make(map[string]Record)}
}

func (r *MemoryRepository) Create(_ context.Context, record Record) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.records[record.Email]; exists {
		return ErrAlreadyRegistered
	}
	r.records[record.Email] = record
	return nil
}
