package interest

import (
	"context"
	"errors"
	"testing"
)

func TestRegister(t *testing.T) {
	service, err := NewService(NewMemoryRepository())
	if err != nil {
		t.Fatal(err)
	}
	record, err := service.Register(context.Background(), " Hello@Example.com ", "")
	if err != nil {
		t.Fatal(err)
	}
	if record.Email != "hello@example.com" || record.Source != "login" {
		t.Fatalf("record = %+v", record)
	}
	if _, err := service.Register(context.Background(), "hello@example.com", "login"); !errors.Is(err, ErrAlreadyRegistered) {
		t.Fatalf("duplicate error = %v", err)
	}
	if _, err := service.Register(context.Background(), "not-an-email", "login"); err == nil {
		t.Fatal("invalid email accepted")
	}
}
