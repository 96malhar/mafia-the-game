package server

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

// registerRoutes is the single source of truth for the URL surface.
//
// New endpoints (WebSocket upgrade, REST API, etc.) should be added here so
// the full route table is greppable in one place.
func registerRoutes(r chi.Router, cfg Config) {
	// Middleware order matters: RequestID first so every later log line
	// can correlate, Recoverer last so it catches panics in everything above
	// it. We intentionally do NOT use middleware.RealIP — it trusts client-
	// supplied X-Forwarded-For / X-Real-IP headers, which is spoofable from
	// the public internet (see chi GHSA-3fxj-6jh8-hvhx). When we deploy
	// behind a known proxy, we'll add a narrowly-scoped trusted-proxy
	// middleware instead.
	r.Use(middleware.RequestID)
	r.Use(requestLogger(cfg.Logger))
	r.Use(middleware.Recoverer)

	r.Get("/healthz", handleHealth)

	// Static assets are mounted last so API/WS routes (added later) take
	// precedence. chi's Handle with "/*" matches the remaining tree.
	r.Handle("/*", http.FileServer(http.FS(cfg.WebFS)))
}
