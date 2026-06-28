package api

import (
	"context"
	"encoding/json"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/gofiber/fiber/v3"
)

// Access gating: pre-launch, non-admin users land on Home only and must Request
// access; the admin (TELEGRAM_ADMIN_USER_ID) approves them, which unlocks the
// app. Flip ACCESS_OPEN=true to auto-approve everyone (free & open) at launch.
// The admin is always approved. Records are keyed by JWT subject.

type AccessRecord struct {
	Subject     string    `json:"subject" bson:"_id"`
	Name        string    `json:"name" bson:"name"`
	Status      string    `json:"status" bson:"status"`                 // requested | approved
	Tier        string    `json:"tier,omitempty" bson:"tier,omitempty"` // free | captain | commander
	RequestedAt time.Time `json:"requested_at" bson:"requested_at"`
	ApprovedAt  time.Time `json:"approved_at,omitempty" bson:"approved_at,omitempty"`
}

const (
	accessRequested = "requested"
	accessApproved  = "approved"
)

// AccessStore persists per-user access state.
type AccessStore interface {
	Get(ctx context.Context, subject string) (AccessRecord, bool, error)
	Request(ctx context.Context, subject, name string) error
	Approve(ctx context.Context, subject string) error
	SetTier(ctx context.Context, subject, tier string) error
	Pending(ctx context.Context) ([]AccessRecord, error)
}

func (s *Server) isAdmin(c fiber.Ctx) bool {
	if s.cfg.Telegram.AdminUserID == 0 {
		return false
	}
	return claimsOf(c).Subject == "tg:"+strconv.FormatInt(s.cfg.Telegram.AdminUserID, 10)
}

// approved reports whether the subject may use the app beyond Home.
func (s *Server) approved(c fiber.Ctx) bool {
	if s.cfg.App.AccessOpen || s.isAdmin(c) {
		return true
	}
	rec, ok, err := s.access.Get(c.Context(), claimsOf(c).Subject)
	return err == nil && ok && rec.Status == accessApproved
}

// handleMe tells the dashboard who the user is and whether they're unlocked.
func (s *Server) handleMe(c fiber.Ctx) error {
	subject := claimsOf(c).Subject
	admin := s.isAdmin(c)
	approved := admin || s.cfg.App.AccessOpen
	status := ""
	if !approved {
		if rec, ok, err := s.access.Get(c.Context(), subject); err == nil && ok {
			status = rec.Status
			approved = rec.Status == accessApproved
		}
	}
	tier := s.tierOfSubject(c.Context(), subject)
	lim := limitsFor(tier)
	return c.JSON(fiber.Map{
		"subject":       subject,
		"username":      claimsOf(c).Username,
		"admin":         admin,
		"approved":      approved,
		"status":        status,
		"open":          s.cfg.App.AccessOpen,
		"tier":          tier,
		"tier_title":    tierTitle(tier),
		"ai_limit":      lim.AIPerDay,
		"ai_used":       s.usage.Get(subject, "ai"),
		"mission_limit": lim.MissionsPerDay,
		"mission_used":  s.usage.Get(subject, "mission"),
	})
}

// handleAdminTier sets a user's tier (admin only): free | captain | commander.
func (s *Server) handleAdminTier(c fiber.Ctx) error {
	if !s.isAdmin(c) {
		return c.Status(fiber.StatusForbidden).JSON(fiber.Map{"error": "admin only"})
	}
	var body struct {
		Subject string `json:"subject"`
		Tier    string `json:"tier"`
	}
	if err := json.Unmarshal(c.Body(), &body); err != nil || body.Subject == "" || !validTier(body.Tier) {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "subject and a valid tier (free|captain|commander) are required"})
	}
	if err := s.access.SetTier(c.Context(), body.Subject, body.Tier); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "could not set tier"})
	}
	return c.JSON(fiber.Map{"subject": body.Subject, "tier": body.Tier})
}

// handleAccessRequest records a crew-access request from a non-approved user.
func (s *Server) handleAccessRequest(c fiber.Ctx) error {
	if s.approved(c) {
		return c.JSON(fiber.Map{"status": accessApproved})
	}
	if err := s.access.Request(c.Context(), claimsOf(c).Subject, claimsOf(c).Username); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "could not record request"})
	}
	return c.JSON(fiber.Map{"status": accessRequested})
}

// handleAdminPending lists pending access requests (admin only).
func (s *Server) handleAdminPending(c fiber.Ctx) error {
	if !s.isAdmin(c) {
		return c.Status(fiber.StatusForbidden).JSON(fiber.Map{"error": "admin only"})
	}
	pending, err := s.access.Pending(c.Context())
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "could not load requests"})
	}
	if pending == nil {
		pending = []AccessRecord{}
	}
	return c.JSON(fiber.Map{"pending": pending})
}

// handleAdminApprove approves one user (admin only).
func (s *Server) handleAdminApprove(c fiber.Ctx) error {
	if !s.isAdmin(c) {
		return c.Status(fiber.StatusForbidden).JSON(fiber.Map{"error": "admin only"})
	}
	var body struct {
		Subject string `json:"subject"`
	}
	if err := json.Unmarshal(c.Body(), &body); err != nil || body.Subject == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "subject is required"})
	}
	if err := s.access.Approve(c.Context(), body.Subject); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "could not approve"})
	}
	return c.JSON(fiber.Map{"approved": body.Subject})
}

// memAccess is the default in-process access store. Production wires a Mongo
// store so approvals survive restarts.
type memAccess struct {
	mu   sync.Mutex
	recs map[string]AccessRecord
}

func newMemAccess() *memAccess { return &memAccess{recs: make(map[string]AccessRecord)} }

func (m *memAccess) Get(_ context.Context, subject string) (AccessRecord, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	rec, ok := m.recs[subject]
	return rec, ok, nil
}

func (m *memAccess) Request(_ context.Context, subject, name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if rec, ok := m.recs[subject]; ok && rec.Status == accessApproved {
		return nil
	}
	m.recs[subject] = AccessRecord{Subject: subject, Name: name, Status: accessRequested, RequestedAt: time.Now().UTC()}
	return nil
}

func (m *memAccess) Approve(_ context.Context, subject string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	rec := m.recs[subject]
	rec.Subject = subject
	rec.Status = accessApproved
	if rec.Tier == "" {
		rec.Tier = TierFree
	}
	rec.ApprovedAt = time.Now().UTC()
	m.recs[subject] = rec
	return nil
}

func (m *memAccess) SetTier(_ context.Context, subject, tier string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	rec := m.recs[subject]
	rec.Subject = subject
	if rec.Status == "" {
		rec.Status = accessApproved // setting a paid tier implies access
	}
	rec.Tier = tier
	m.recs[subject] = rec
	return nil
}

func (m *memAccess) Pending(_ context.Context) ([]AccessRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []AccessRecord
	for _, rec := range m.recs {
		if rec.Status == accessRequested {
			out = append(out, rec)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].RequestedAt.Before(out[j].RequestedAt) })
	return out, nil
}
