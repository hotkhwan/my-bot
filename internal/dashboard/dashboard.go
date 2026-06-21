// Package dashboard serves the web UI bundled into the binary at build time.
//
// The frontend build output lives in dist/ and is embedded via go:embed, so the
// api binary is fully self-contained: there is no on-disk path to configure and
// nothing to copy alongside the binary at deploy time. Replace dist/ with the
// real frontend build (e.g. `vite build --outDir internal/dashboard/dist`) and
// commit it, or add a frontend build stage to the Dockerfile before `go build`.
package dashboard

import (
	"embed"
	"fmt"
	"io/fs"

	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/static"
)

//go:embed all:dist
var embedded embed.FS

// FS returns the embedded build rooted at dist/, so callers see index.html at
// the root rather than under a dist/ prefix.
func FS() (fs.FS, error) {
	return fs.Sub(embedded, "dist")
}

// Register mounts the embedded dashboard as a catch-all static handler.
//
// It MUST be called after every API route is registered: Fiber matches routes
// in registration order, so the "/*" catch-all here only handles paths that no
// earlier API route claimed. Unknown paths fall back to index.html so a
// single-page app keeps working on deep links and refreshes.
func Register(app *fiber.App) error {
	dist, err := FS()
	if err != nil {
		return fmt.Errorf("dashboard: open embedded fs: %w", err)
	}

	index, err := fs.ReadFile(dist, "index.html")
	if err != nil {
		return fmt.Errorf("dashboard: read index.html: %w", err)
	}

	app.Get("/*", static.New("", static.Config{
		FS: dist,
		NotFoundHandler: func(c fiber.Ctx) error {
			// The static handler already set 404 before delegating here; reset
			// to 200 so SPA deep links resolve to the app shell, not an error.
			c.Status(fiber.StatusOK)
			c.Set(fiber.HeaderContentType, fiber.MIMETextHTMLCharsetUTF8)
			return c.Send(index)
		},
	}))

	return nil
}
