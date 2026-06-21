package app

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"bottrade/internal/config"
)

// Each role entrypoint must check the context before opening any external
// connection (MongoDB, Telegram, HTTP). A pre-cancelled context therefore
// returns immediately with the context error and never touches the network,
// which is what lets these run without a database in tests.
func TestIsShutdown(t *testing.T) {
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	live := context.Background()

	cases := []struct {
		name string
		ctx  context.Context
		err  error
		want bool
	}{
		{"cancelled + bare sentinel", cancelled, context.Canceled, true},
		{"cancelled + wrapped sentinel", cancelled, fmt.Errorf("listen http: %w", context.Canceled), true},
		{"cancelled + nil", cancelled, nil, false},
		{"cancelled + unrelated error", cancelled, errors.New("boom"), false},
		{"live ctx + cancelled-looking error", live, context.Canceled, false},
		{"live ctx + real error", live, errors.New("boom"), false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsShutdown(tc.ctx, tc.err); got != tc.want {
				t.Fatalf("IsShutdown = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestRunRoles_ReturnOnCancelledContext(t *testing.T) {
	roles := map[string]func(*App, context.Context) error{
		"worker": (*App).RunWorker,
		"api":    (*App).RunAPI,
		"all":    (*App).Run,
	}

	for name, run := range roles {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()

			a := New(config.Config{}, nil)
			err := run(a, ctx)
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("role %s: expected context.Canceled, got %v", name, err)
			}
		})
	}
}
