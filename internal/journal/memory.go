package journal

import (
	"context"
	"sync"
)

// MemoryRepository is an in-memory Repository, used in tests and as a default
// before the MongoDB-backed repository is wired (Phase 1 follow-up).
type MemoryRepository struct {
	mu     sync.Mutex
	trades map[string]Trade
}

// NewMemoryRepository returns an empty in-memory repository.
func NewMemoryRepository() *MemoryRepository {
	return &MemoryRepository{trades: make(map[string]Trade)}
}

// Save upserts by Trade.ID.
func (r *MemoryRepository) Save(_ context.Context, trade Trade) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.trades[trade.ID] = trade
	return nil
}

// List returns the trades matching filter, in no particular order.
func (r *MemoryRepository) List(_ context.Context, filter Filter) ([]Trade, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Trade, 0, len(r.trades))
	for _, trade := range r.trades {
		if filter.matches(trade) {
			out = append(out, trade)
		}
	}
	return out, nil
}
