package api

import (
	"context"
	"encoding/json"
	"strings"
	"sync"

	"github.com/gofiber/fiber/v3"
)

// Favourites are a user's starred coins. They are stored server-side (keyed by
// JWT subject) so they follow the user across clients — the web browser and the
// Telegram mini app share the same list instead of each keeping its own
// localStorage copy.

const maxFavourites = 60

// FavouritesStore persists a user's favourite symbols, keyed by JWT subject.
type FavouritesStore interface {
	Get(ctx context.Context, subject string) ([]string, error)
	Set(ctx context.Context, subject string, symbols []string) error
}

func (s *Server) handleGetFavourites(c fiber.Ctx) error {
	subject := claimsOf(c).Subject
	if s.favourites == nil || subject == "" {
		return c.JSON(fiber.Map{"symbols": []string{}})
	}
	syms, err := s.favourites.Get(c.Context(), subject)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "could not load favourites"})
	}
	if syms == nil {
		syms = []string{}
	}
	return c.JSON(fiber.Map{"symbols": syms})
}

func (s *Server) handleSetFavourites(c fiber.Ctx) error {
	subject := claimsOf(c).Subject
	if s.favourites == nil || subject == "" {
		return c.JSON(fiber.Map{"symbols": []string{}})
	}
	var body struct {
		Symbols []string `json:"symbols"`
	}
	if err := json.Unmarshal(c.Body(), &body); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid JSON body"})
	}
	clean := sanitizeSymbols(body.Symbols)
	if err := s.favourites.Set(c.Context(), subject, clean); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "could not save favourites"})
	}
	return c.JSON(fiber.Map{"symbols": clean})
}

// sanitizeSymbols upper-cases, strips junk, de-dupes (preserving order), and caps
// the list so a client can't push unbounded or malformed data.
func sanitizeSymbols(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, raw := range in {
		sym := strings.ToUpper(strings.TrimSpace(raw))
		var b strings.Builder
		for _, r := range sym {
			if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
				b.WriteRune(r)
			}
		}
		sym = b.String()
		if sym == "" || len(sym) > 20 || seen[sym] {
			continue
		}
		seen[sym] = true
		out = append(out, sym)
		if len(out) >= maxFavourites {
			break
		}
	}
	return out
}

// memFavourites is the default in-process store. Production wires a Mongo store
// so favourites survive restarts and are shared across instances.
type memFavourites struct {
	mu   sync.Mutex
	favs map[string][]string
}

func newMemFavourites() *memFavourites { return &memFavourites{favs: make(map[string][]string)} }

func (m *memFavourites) Get(_ context.Context, subject string) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, len(m.favs[subject]))
	copy(out, m.favs[subject])
	return out, nil
}

func (m *memFavourites) Set(_ context.Context, subject string, symbols []string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make([]string, len(symbols))
	copy(cp, symbols)
	m.favs[subject] = cp
	return nil
}
