package server

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/httprate"
)

// registerRoutes is the single source of truth for the URL surface.
//
// New endpoints (WebSocket upgrade, REST API, etc.) should be added here so
// the full route table is greppable in one place.
func registerRoutes(r chi.Router, cfg Config) {
	// Middleware order matters: RequestID first so every later log line
	// can correlate; realClientIP next so the logger and rate limiter
	// see the true client IP; Recoverer last so it catches panics in
	// everything above it.
	//
	// We do NOT use chi's middleware.RealIP — it trusts client-supplied
	// X-Forwarded-For / X-Real-IP unconditionally, which is spoofable
	// from the public internet (see chi GHSA-3fxj-6jh8-hvhx).
	// realClientIP instead trusts only the single, exact header set by
	// our known proxy (Config.TrustedClientIPHeader), and is a no-op
	// when that's empty.
	r.Use(middleware.RequestID)
	r.Use(realClientIP(cfg.TrustedClientIPHeader))
	r.Use(requestLogger(cfg.Logger))
	r.Use(securityHeaders())
	r.Use(limitBody(maxRequestBytes))
	r.Use(middleware.Compress(5))
	r.Use(recoverer(cfg.Logger))

	r.Get("/healthz", handleHealth)

	// Game routes — only registered when the WebSocket handler is wired
	// in (Config.WS != nil). Tests that only need static-serving leave
	// it nil.
	if cfg.WS != nil {
		// Rate-limit room creation per client IP so a single source
		// can't churn through the server's room capacity. Disabled
		// when RoomCreateRPM <= 0 (local dev / tests).
		r.Group(func(r chi.Router) {
			if cfg.RoomCreateRPM > 0 {
				r.Use(httprate.LimitByIP(cfg.RoomCreateRPM, time.Minute))
			}
			r.Post("/api/rooms", cfg.WS.CreateRoom)
		})
		r.Get("/ws/{code}", cfg.WS.Connect)
	}

	// Static assets are mounted last so API/WS routes take precedence.
	// chi's Handle with "/*" matches the remaining tree.
	r.Handle("/*", http.FileServer(http.FS(cfg.WebFS)))
}
