package interest

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestRegister(t *testing.T) {
	service, err := NewService(NewMemoryRepository())
	if err != nil {
		t.Fatal(err)
	}
	record, err := service.Register(context.Background(), " Hello@Example.com ", "testing", "")
	if err != nil {
		t.Fatal(err)
	}
	if record.Email != "hello@example.com" || record.Source != "login" {
		t.Fatalf("record = %+v", record)
	}
	if _, err := service.Register(context.Background(), "hello@example.com", "", "login"); !errors.Is(err, ErrAlreadyRegistered) {
		t.Fatalf("duplicate error = %v", err)
	}
	if _, err := service.Register(context.Background(), "not-an-email", "", "login"); err == nil {
		t.Fatal("invalid email accepted")
	}
	expires := time.Now().Add(time.Hour)
	if err := service.SetInvite(context.Background(), record.Email, "hashed-token", expires); err != nil {
		t.Fatal(err)
	}
	invited, err := service.FindInvite(context.Background(), "hashed-token")
	if err != nil || invited.Email != record.Email || invited.Status != "invited" {
		t.Fatalf("invite = %+v, %v", invited, err)
	}
	if err := service.MarkRegistered(context.Background(), record.Email); err != nil {
		t.Fatal(err)
	}
	if _, err := service.FindInvite(context.Background(), "hashed-token"); err == nil {
		t.Fatal("used invite remained valid")
	}
}
