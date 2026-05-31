// Package server wires HTTP routes and owns the *http.Server lifecycle.
//
// The package is split by concern:
//
//	server.go      Config, Server, Start/Shutdown — lifecycle only.
//	routes.go      registerRoutes — the single place routes are declared.
//	handlers.go    HTTP handler funcs.
//	middleware.go  Custom middleware.
//
// The server serves the static frontend (embedded via go:embed; see
// web/web.go), the WebSocket/REST game routes, and a /healthz endpoint
// for liveness checks.
package server

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"

	"github.com/96malhar/mafia-the-game/internal/transport/ws"
)

// Config holds runtime configuration for the server.
type Config struct {
	// Addr is the TCP address to listen on, e.g. ":8080".
	Addr string

	// WebFS is the filesystem containing static assets (index.html, favicon.png).
	// We accept an fs.FS rather than a path so production can pass the
	// go:embed FS (web.FS) while tests pass an in-memory FS.
	WebFS fs.FS

	// WS is the WebSocket handler that owns the room manager. If nil,
	// the /api/rooms and /ws/* routes are not registered; useful for
	// tests that only exercise the static-serving paths.
	WS *ws.Handler

	// Logger is the structured logger used for request and lifecycle logs.
	Logger *slog.Logger

	// TrustedClientIPHeader, when non-empty, names the upstream proxy
	// header to read the real client IP from (e.g. "Fly-Client-IP").
	// Only set this when the app runs behind a proxy that sets it on
	// every request; leaving it empty (the default) means RemoteAddr is
	// the direct peer and no client-supplied header is trusted.
	TrustedClientIPHeader string

	// RoomCreateRPM rate-limits POST /api/rooms to this many requests
	// per minute PER client IP. Zero or negative disables the limiter
	// (the default, so tests and local dev are unthrottled).
	RoomCreateRPM int
}

// maxRequestBytes caps any request body. The app has no upload paths,
// so this is purely a memory-exhaustion guard.
const maxRequestBytes = 64 << 10 // 64 KiB

// Server is a thin wrapper around *http.Server that exposes Start/Shutdown.
type Server struct {
	cfg  Config
	http *http.Server
}

// New constructs a Server with routes registered but does not start it.
func New(cfg Config) *Server {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}

	r := chi.NewRouter()
	registerRoutes(r, cfg)

	// Wrap the whole router so every request feeds the standard
	// http.server.* metrics into the global (Prometheus-backed) meter
	// provider, surfacing them at /metrics. No tracer provider is
	// installed, so the span side is a cheap no-op. otelhttp's response
	// writer forwards Unwrap()/Hijack(), so the WebSocket upgrade still
	// works through it.
	handler := otelhttp.NewHandler(r, "http.server")

	return &Server{
		cfg: cfg,
		http: &http.Server{
			Addr:    cfg.Addr,
			Handler: handler,
			// ReadHeaderTimeout bounds slow-header (slowloris) attacks.
			// We intentionally do NOT set ReadTimeout/WriteTimeout: the
			// WebSocket connections are long-lived and hijacked after
			// the upgrade, so a blanket write/read timeout would sever
			// active games. IdleTimeout reaps idle keep-alive HTTP
			// connections (the static/API side) without touching WS.
			ReadHeaderTimeout: 5 * time.Second,
			IdleTimeout:       120 * time.Second,
		},
	}
}

// Start blocks until the server stops. It returns nil on graceful shutdown.
func (s *Server) Start() error {
	s.cfg.Logger.Info("server listening", "addr", s.cfg.Addr)
	err := s.http.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// Shutdown gracefully stops the server, waiting up to ctx's deadline.
func (s *Server) Shutdown(ctx context.Context) error {
	return s.http.Shutdown(ctx)
}

// handler returns the underlying http.Handler. It is unexported and exists
// only so tests in this package can drive the server through httptest
// without binding a real port.
func (s *Server) handler() http.Handler {
	return s.http.Handler
}
