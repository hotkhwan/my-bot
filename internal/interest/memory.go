package interest

import (
	"context"
	"sync"
)

type MemoryRepository struct {
	mu      sync.Mutex
	records map[string]Record
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
