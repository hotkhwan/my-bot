package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"bottrade/internal/version"

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
	accessRevoked   = "revoked"
)

// AccessStore persists per-user access state.
type AccessStore interface {
	Get(ctx context.Context, subject string) (AccessRecord, bool, error)
	Request(ctx context.Context, subject, name string) error
	Approve(ctx context.Context, subject string) error
	Revoke(ctx context.Context, subject string) error
	SetTier(ctx context.Context, subject, tier string) error
	Pending(ctx context.Context) ([]AccessRecord, error)
}

func (s *Server) isAdmin(c fiber.Ctx) bool {
	return s.isAdminSubject(claimsOf(c).Subject)
}

func (s *Server) isAdminSubject(subject string) bool {
	if s.cfg.Telegram.AdminUserID == 0 {
		return false
	}
	return subject == "tg:"+strconv.FormatInt(s.cfg.Telegram.AdminUserID, 10)
}

// aiFreeForSubject reports whether the shared-server AI is free (unmetered) for
// this user. During private beta (PRIVATE_BETA=true) the admin and every approved
// crew member get unlimited shared AI; once the public free tier opens, only the
// admin (Commander) stays unlimited and everyone else is metered by tier.
func (s *Server) aiFreeForSubject(ctx context.Context, subject string) bool {
	if s.isAdminSubject(subject) {
		return true
	}
	if !s.cfg.App.PrivateBeta {
		return false
	}
	rec, ok, err := s.access.Get(ctx, subject)
	return err == nil && ok && rec.Status == accessApproved
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
	aiLimit := lim.AIPerDay
	if s.aiFreeForSubject(c.Context(), subject) {
		aiLimit = -1 // unlimited shared AI during closed beta (or admin)
	}
	return c.JSON(fiber.Map{
		"version":       version.Version,
		"subject":       subject,
		"username":      claimsOf(c).Username,
		"admin":         admin,
		"approved":      approved,
		"status":        status,
		"open":          s.cfg.App.AccessOpen,
		"tier":          tier,
		"tier_title":    tierTitle(tier),
		"ai_limit":      aiLimit,
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
	// Ping the admin so they don't have to poll /pending.
	subject := claimsOf(c).Subject
	tgID := strings.TrimPrefix(subject, "tg:")
	s.notifyAdmin("🛡 New crew access request: " + claimsOf(c).Username + " (" + subject + ")\nApprove with  /approve " + tgID + "   ·  /pending to review.")
	return c.JSON(fiber.Map{"status": accessRequested})
}

// notifyAdmin sends a one-off Telegram message to the admin (best-effort, async).
// It uses the bot's sendMessage directly — safe from any process since only
// getUpdates (the poller) must be single-instance.
func (s *Server) notifyAdmin(text string) {
	token := s.cfg.Telegram.BotToken
	adminID := s.cfg.Telegram.AdminUserID
	if token == "" || adminID == 0 {
		s.logger.Warn("admin notify skipped (TELEGRAM_BOT_TOKEN / TELEGRAM_ADMIN_USER_ID not set on this process)")
		return
	}
	s.logger.Info("notifying admin of crew request", "admin_id", adminID)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		payload, _ := json.Marshal(map[string]any{"chat_id": adminID, "text": text})
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.telegram.org/bot"+token+"/sendMessage", bytes.NewReader(payload))
		if err != nil {
			return
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			s.logger.Warn("admin notify failed", "error", err)
			return
		}
		_ = resp.Body.Close()
	}()
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

// handleAdminRevoke revokes a user's access (admin only): they lose the app
// until re-approved. The admin themselves can't be revoked.
func (s *Server) handleAdminRevoke(c fiber.Ctx) error {
	if !s.isAdmin(c) {
		return c.Status(fiber.StatusForbidden).JSON(fiber.Map{"error": "admin only"})
	}
	var body struct {
		Subject string `json:"subject"`
	}
	if err := json.Unmarshal(c.Body(), &body); err != nil || body.Subject == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "subject is required"})
	}
	if s.isAdminSubject(body.Subject) {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "cannot revoke the admin"})
	}
	if err := s.access.Revoke(c.Context(), body.Subject); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "could not revoke"})
	}
	return c.JSON(fiber.Map{"revoked": body.Subject})
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

func (m *memAccess) Revoke(_ context.Context, subject string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	rec := m.recs[subject]
	rec.Subject = subject
	rec.Status = accessRevoked
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
