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

// Save inserts open rows idempotently. A duplicate open write must not revive a
// row that Close already finalized.
func (r *MemoryRepository) Save(_ context.Context, trade Trade) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if trade.Outcome == OutcomeOpen {
		if _, exists := r.trades[trade.ID]; exists {
			return nil
		}
	}
	r.trades[trade.ID] = trade
	return nil
}

// Close updates a row only while it is still open.
func (r *MemoryRepository) Close(_ context.Context, id string, update CloseUpdate) (Trade, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	trade, ok := r.trades[id]
	if !ok || trade.Outcome != OutcomeOpen {
		return Trade{}, false, nil
	}
	trade.Exit = update.Exit
	trade.PnLUSDT = update.PnLUSDT
	trade.Outcome = update.Outcome
	trade.ClosedAt = update.ClosedAt
	if update.Mode != "" {
		trade.Mode = update.Mode
	}
	r.trades[id] = trade
	return trade, true, nil
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
