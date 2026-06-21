package dashboard

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v3"
)

func TestRegisterServesEmbeddedIndex(t *testing.T) {
	app := fiber.New()
	if err := Register(app); err != nil {
		t.Fatalf("Register: %v", err)
	}

	body, status := get(t, app, "/")
	if status != http.StatusOK {
		t.Fatalf("GET / status = %d, want 200", status)
	}
	if !strings.Contains(body, "Trading Bot Dashboard") {
		t.Fatalf("GET / body does not contain dashboard marker: %q", body)
	}
}

func TestRegisterSPAFallbackServesIndex(t *testing.T) {
	app := fiber.New()
	if err := Register(app); err != nil {
		t.Fatalf("Register: %v", err)
	}

	// A deep-link route that has no embedded file must fall back to index.html
	// (200 + HTML) so client-side routing survives a refresh.
	body, status := get(t, app, "/positions/abc123")
	if status != http.StatusOK {
		t.Fatalf("SPA fallback status = %d, want 200", status)
	}
	if !strings.Contains(body, "Trading Bot Dashboard") {
		t.Fatalf("SPA fallback did not serve index.html: %q", body)
	}
}

func TestRegisterDoesNotShadowEarlierRoutes(t *testing.T) {
	app := fiber.New()
	// API route registered before the dashboard catch-all, mirroring the api
	// server's registration order.
	app.Get("/healthz", func(c fiber.Ctx) error { return c.SendString("ok") })
	if err := Register(app); err != nil {
		t.Fatalf("Register: %v", err)
	}

	body, status := get(t, app, "/healthz")
	if status != http.StatusOK {
		t.Fatalf("GET /healthz status = %d, want 200", status)
	}
	if body != "ok" {
		t.Fatalf("GET /healthz body = %q, want \"ok\" (dashboard shadowed the API route)", body)
	}
}

func get(t *testing.T, app *fiber.App, target string) (string, int) {
	t.Helper()
	resp, err := app.Test(httptest.NewRequest(http.MethodGet, target, nil))
	if err != nil {
		t.Fatalf("Test %s: %v", target, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body %s: %v", target, err)
	}
	return string(body), resp.StatusCode
}
