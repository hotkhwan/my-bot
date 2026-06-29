package email

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

type Sender interface {
	SendInvite(context.Context, string, string) error
}

type ResendSender struct {
	APIKey, From string
	Client       *http.Client
}

func (s *ResendSender) SendInvite(ctx context.Context, to, link string) error {
	if s.Client == nil {
		s.Client = http.DefaultClient
	}
	body, _ := json.Marshal(map[string]any{
		"from": s.From, "to": []string{to}, "subject": "Your ANNY early-access invitation",
		"html": `<h2>Welcome to ANNY early access</h2><p>Your invitation is ready.</p><p><a href="` + link + `">Create your account</a></p><p>After registration, access remains pending until an admin approves your member account.</p>`,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.resend.com/emails", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+s.APIKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.Client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("resend returned status %d", resp.StatusCode)
	}
	return nil
}

func NewResendSender(key, from string) Sender {
	if strings.TrimSpace(key) == "" || strings.TrimSpace(from) == "" {
		return nil
	}
	return &ResendSender{APIKey: key, From: from, Client: http.DefaultClient}
}
