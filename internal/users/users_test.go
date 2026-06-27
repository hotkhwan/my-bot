package users

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func newService(t *testing.T) *Service {
	t.Helper()
	svc, err := NewService(NewMemoryRepository(),
		WithClock(func() time.Time { return time.Unix(1700000000, 0) }),
		WithIDGenerator(func() string { return "fixed-id" }),
	)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	return svc
}

func TestRegisterHashesPassword(t *testing.T) {
	svc := newService(t)
	user, err := svc.Register(context.Background(), "  Alice  ", "supersecret")
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if user.Username != "alice" {
		t.Fatalf("username = %q, want normalized 'alice'", user.Username)
	}
	if user.Role != RoleUser {
		t.Fatalf("role = %q, want user", user.Role)
	}
	if user.PasswordHash == "" || strings.Contains(user.PasswordHash, "supersecret") {
		t.Fatalf("password stored in the clear: %q", user.PasswordHash)
	}
}

func TestRegisterRejectsDuplicateAndWeak(t *testing.T) {
	svc := newService(t)
	ctx := context.Background()
	if _, err := svc.Register(ctx, "bob", "longenough"); err != nil {
		t.Fatalf("first register: %v", err)
	}
	if _, err := svc.Register(ctx, "BOB", "longenough"); !errors.Is(err, ErrUsernameTaken) {
		t.Fatalf("duplicate err = %v, want ErrUsernameTaken", err)
	}
	if _, err := svc.Register(ctx, "carol", "short"); err == nil {
		t.Fatal("expected error for too-short password")
	}
	if _, err := svc.Register(ctx, "   ", "longenough"); err == nil {
		t.Fatal("expected error for blank username")
	}
}

func TestAuthenticate(t *testing.T) {
	svc := newService(t)
	ctx := context.Background()
	if _, err := svc.Register(ctx, "dave", "correcthorse"); err != nil {
		t.Fatalf("register: %v", err)
	}

	if _, err := svc.Authenticate(ctx, "DAVE", "correcthorse"); err != nil {
		t.Fatalf("valid login failed: %v", err)
	}
	// Wrong password and unknown user must be indistinguishable.
	if err := authErr(svc, "dave", "wrong"); !errors.Is(err, ErrInvalidLogin) {
		t.Fatalf("wrong password err = %v, want ErrInvalidLogin", err)
	}
	if err := authErr(svc, "ghost", "whatever"); !errors.Is(err, ErrInvalidLogin) {
		t.Fatalf("unknown user err = %v, want ErrInvalidLogin", err)
	}
}

func authErr(svc *Service, username, password string) error {
	_, err := svc.Authenticate(context.Background(), username, password)
	return err
}
