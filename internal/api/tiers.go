package api

import (
	"context"
	"fmt"
	"strconv"
	"sync"
	"time"
)

// Tiers gate the two real cost drivers — AI calls and live (real) missions —
// behind soft daily limits, so the free Crew tier is genuinely usable but
// nudges toward Captain. Paper goal runs are unlimited (they cost ~nothing).
// Capital is never enforced (a rich user isn't heavier load); resource limits
// are. Admin upgrades users manually for now — no payment integration yet.

const (
	TierFree      = "free"      // Crew
	TierCaptain   = "captain"   // Captain (paid)
	TierCommander = "commander" // Commander (enterprise / admin)
)

// TierLimits is per-day; -1 means unlimited.
type TierLimits struct {
	AIPerDay       int
	MissionsPerDay int
}

var tierLimits = map[string]TierLimits{
	TierFree:      {AIPerDay: 10, MissionsPerDay: 5},
	TierCaptain:   {AIPerDay: 200, MissionsPerDay: 50},
	TierCommander: {AIPerDay: -1, MissionsPerDay: -1},
}

func limitsFor(tier string) TierLimits {
	if l, ok := tierLimits[tier]; ok {
		return l
	}
	return tierLimits[TierFree]
}

func tierTitle(tier string) string {
	switch tier {
	case TierCaptain:
		return "Captain"
	case TierCommander:
		return "Commander"
	default:
		return "Crew (free)"
	}
}

func validTier(tier string) bool {
	_, ok := tierLimits[tier]
	return ok
}

func today() string { return time.Now().UTC().Format("2006-01-02") }

// memUsage tracks per-user daily counts in-process. For MVP this is fine: limits
// are daily and a restart only resets them generously (never over-counts). A
// shared store can replace it later if the api runs multiple instances.
type memUsage struct {
	mu     sync.Mutex
	counts map[string]int // subject|day|kind -> n
}

func newMemUsage() *memUsage { return &memUsage{counts: make(map[string]int)} }

func usageKey(subject, kind string) string { return subject + "|" + today() + "|" + kind }

func (m *memUsage) Get(subject, kind string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.counts[usageKey(subject, kind)]
}

func (m *memUsage) Incr(subject, kind string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := usageKey(subject, kind)
	m.counts[k]++
	return m.counts[k]
}

// tierOfSubject resolves a user's tier from the access record; the admin is
// always Commander (unlimited).
func (s *Server) tierOfSubject(ctx context.Context, subject string) string {
	if s.cfg.Telegram.AdminUserID != 0 && subject == "tg:"+strconv.FormatInt(s.cfg.Telegram.AdminUserID, 10) {
		return TierCommander
	}
	if s.access != nil {
		if rec, ok, err := s.access.Get(ctx, subject); err == nil && ok && rec.Tier != "" {
			return rec.Tier
		}
	}
	return TierFree
}

// allow reports whether the subject is under its daily limit for kind
// ("ai"|"mission"); on false it returns an upgrade message.
func (s *Server) allow(ctx context.Context, subject, kind string) (bool, string) {
	tier := s.tierOfSubject(ctx, subject)
	lim := limitsFor(tier)
	limit := lim.AIPerDay
	if kind == "mission" {
		limit = lim.MissionsPerDay
	}
	if limit < 0 {
		return true, ""
	}
	if s.usage.Get(subject, kind) >= limit {
		label := "AI runs"
		if kind == "mission" {
			label = "live missions"
		}
		return false, fmt.Sprintf("Daily %s limit (%d) reached on the %s plan — upgrade to Captain for more.", label, limit, tierTitle(tier))
	}
	return true, ""
}
