package api

import (
	"context"
	"encoding/json"
	"strings"
	"sync"

	"bottrade/internal/ai"
	"bottrade/internal/auth"
	"bottrade/internal/signals"

	"github.com/gofiber/fiber/v3"
)

// Bring-your-own AI key: a user can store their own LLM API key (encrypted at
// rest like their Binance key) so their AI missions run on their own quota at no
// cost to the platform — the path to real AI on the free tier. When set, it
// overrides the shared server advisor for that user. The plaintext key is never
// returned or logged.

// AISecretDoc is the per-user AI advisor config, with the API key sealed.
type AISecretDoc struct {
	Subject  string      `json:"-" bson:"_id"`
	Provider string      `json:"provider" bson:"provider"` // anthropic | openai_compatible
	Model    string      `json:"model" bson:"model"`
	BaseURL  string      `json:"base_url,omitempty" bson:"base_url,omitempty"`
	Sealed   auth.Sealed `json:"-" bson:"sealed"`
}

// AISecretStore persists per-user AI keys (sealed), keyed by JWT subject.
type AISecretStore interface {
	Get(ctx context.Context, subject string) (AISecretDoc, bool, error)
	Save(ctx context.Context, doc AISecretDoc) error
	Delete(ctx context.Context, subject string) error
}

// userAdvisor builds a per-user advisor from a stored key, or returns nil when
// the user has no key / the store or keyring is absent.
func (s *Server) userAdvisor(ctx context.Context, subject string) signals.Advisor {
	if s.aiSecrets == nil || s.keyring == nil || subject == "" {
		return nil
	}
	doc, ok, err := s.aiSecrets.Get(ctx, subject)
	if err != nil || !ok {
		return nil
	}
	key, err := s.keyring.DecryptString(doc.Sealed)
	if err != nil || key == "" {
		return nil
	}
	switch doc.Provider {
	case "anthropic":
		return ai.NewAnthropicAdvisor(ai.AnthropicConfig{APIKey: key, Model: doc.Model, BaseURL: doc.BaseURL})
	default:
		return ai.NewOpenAICompatibleAdvisor(ai.OpenAICompatibleConfig{APIKey: key, Model: doc.Model, BaseURL: doc.BaseURL})
	}
}

type aiKeyBody struct {
	Provider string `json:"provider"`
	APIKey   string `json:"api_key"`
	Model    string `json:"model"`
	BaseURL  string `json:"base_url"`
}

func (s *Server) handleStoreAIKey(c fiber.Ctx) error {
	if s.aiSecrets == nil || s.keyring == nil {
		return c.Status(fiber.StatusNotImplemented).JSON(fiber.Map{"error": "AI keys are not enabled (set CREDENTIAL_ENCRYPTION_KEY)"})
	}
	var body aiKeyBody
	if err := json.Unmarshal(c.Body(), &body); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid JSON body"})
	}
	if strings.TrimSpace(body.APIKey) == "" || strings.TrimSpace(body.Model) == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "api_key and model are required"})
	}
	provider := "openai_compatible"
	if body.Provider == "anthropic" {
		provider = "anthropic"
	}
	sealed, err := s.keyring.EncryptString(strings.TrimSpace(body.APIKey))
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "could not secure the key"})
	}
	doc := AISecretDoc{
		Subject:  claimsOf(c).Subject,
		Provider: provider,
		Model:    strings.TrimSpace(body.Model),
		BaseURL:  strings.TrimSpace(body.BaseURL),
		Sealed:   sealed,
	}
	if err := s.aiSecrets.Save(c.Context(), doc); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "could not save the key"})
	}
	return c.Status(fiber.StatusCreated).JSON(fiber.Map{"saved": true})
}

// handleGetAIKey reports whether a key is set and its non-secret fields.
func (s *Server) handleGetAIKey(c fiber.Ctx) error {
	if s.aiSecrets == nil {
		return c.JSON(fiber.Map{"set": false})
	}
	doc, ok, err := s.aiSecrets.Get(c.Context(), claimsOf(c).Subject)
	if err != nil || !ok {
		return c.JSON(fiber.Map{"set": false})
	}
	return c.JSON(fiber.Map{"set": true, "provider": doc.Provider, "model": doc.Model})
}

func (s *Server) handleDeleteAIKey(c fiber.Ctx) error {
	if s.aiSecrets == nil {
		return c.Status(fiber.StatusNotImplemented).JSON(fiber.Map{"error": "AI keys are not enabled"})
	}
	if err := s.aiSecrets.Delete(c.Context(), claimsOf(c).Subject); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "could not remove the key"})
	}
	return c.JSON(fiber.Map{"deleted": true})
}

// memAISecrets is the default in-process store.
type memAISecrets struct {
	mu   sync.Mutex
	docs map[string]AISecretDoc
}

func newMemAISecrets() *memAISecrets { return &memAISecrets{docs: make(map[string]AISecretDoc)} }

func (m *memAISecrets) Get(_ context.Context, subject string) (AISecretDoc, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	doc, ok := m.docs[subject]
	return doc, ok, nil
}

func (m *memAISecrets) Save(_ context.Context, doc AISecretDoc) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.docs[doc.Subject] = doc
	return nil
}

func (m *memAISecrets) Delete(_ context.Context, subject string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.docs, subject)
	return nil
}
