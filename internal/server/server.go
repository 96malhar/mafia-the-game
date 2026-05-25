// Package server wires HTTP routes and owns the *http.Server lifecycle.
//
// In later steps we'll add WebSocket upgrade handlers and inject a room
// manager here. For now it only serves static files from the web/ directory
// and a /healthz endpoint for liveness checks.
package server

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

// Config holds runtime configuration for the server.
type Config struct {
	// Addr is the TCP address to listen on, e.g. ":8080".
	Addr string

	// WebFS is the filesystem containing static assets (index.html, app.js, ...).
	// We accept an fs.FS rather than a path so tests can pass an in-memory FS
	// and so we can later switch to go:embed without changing this package.
	WebFS fs.FS

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
	fileServer := http.FileServer(http.FS(cfg.WebFS))
	r.Handle("/*", fileServer)

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

func handleHealth(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

// requestLogger is a chi-compatible access log built on slog. We write our
// own (rather than using middleware.Logger) so output is structured JSON-
// friendly and uses the same logger as the rest of the app.
func requestLogger(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
			next.ServeHTTP(ww, r)
			logger.Info("http",
				"method", r.Method,
				"path", r.URL.Path,
				"status", ww.Status(),
				"bytes", ww.BytesWritten(),
				"dur_ms", time.Since(start).Milliseconds(),
				"req_id", middleware.GetReqID(r.Context()),
				"remote", r.RemoteAddr,
			)
		})
	}
}
