// Package server wires HTTP routes and owns the *http.Server lifecycle.
//
// The package is split by concern:
//
//	server.go      Config, Server, Start/Shutdown — lifecycle only.
//	routes.go      registerRoutes — the single place routes are declared.
//	handlers.go    HTTP handler funcs.
//	middleware.go  Custom middleware.
//
// In later steps we'll add WebSocket upgrade handlers and inject a room
// manager via Config. For now the server only serves static files from the
// web/ directory and a /healthz endpoint for liveness checks.
package server

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/malhar/mafia-the-game/internal/transport/ws"
)

// Config holds runtime configuration for the server.
type Config struct {
	// Addr is the TCP address to listen on, e.g. ":8080".
	Addr string

	// WebFS is the filesystem containing static assets (index.html, app.js, ...).
	// We accept an fs.FS rather than a path so tests can pass an in-memory FS
	// and so we can later switch to go:embed without changing this package.
	WebFS fs.FS

	// WS is the WebSocket handler that owns the room manager. If nil,
	// the /api/rooms and /ws/* routes are not registered; useful for
	// tests that only exercise the static-serving paths.
	WS *ws.Handler

	// Logger is the structured logger used for request and lifecycle logs.
	Logger *slog.Logger
}

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

	return &Server{
		cfg: cfg,
		http: &http.Server{
			Addr:              cfg.Addr,
			Handler:           r,
			ReadHeaderTimeout: 5 * time.Second,
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
