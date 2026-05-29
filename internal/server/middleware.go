package server

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5/middleware"
)

// realClientIP rewrites r.RemoteAddr from a trusted upstream proxy
// header (e.g. fly.io's "Fly-Client-IP") so logs and rate limiting see
// the actual client rather than the proxy.
//
// We deliberately do NOT use chi's middleware.RealIP, which trusts
// X-Forwarded-For / X-Real-IP unconditionally and is therefore
// spoofable when the app is reachable directly (see the note in
// registerRoutes). By naming the EXACT header our known proxy sets, we
// only honour input on a deployment we control. header == "" disables
// the rewrite entirely (correct for local dev / direct exposure), so
// nothing trusts client-supplied headers unless explicitly configured.
func realClientIP(header string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		if header == "" {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if ip := r.Header.Get(header); ip != "" {
				r.RemoteAddr = ip
			}
			next.ServeHTTP(w, r)
		})
	}
}

// securityHeaders sets conservative, non-breaking response headers on
// every response. Note: a Content-Security-Policy is intentionally NOT
// set here — the page currently loads Tailwind from a CDN and runs a
// large inline <script>, so any meaningful CSP would either break the
// app or be so permissive ('unsafe-inline' 'unsafe-eval') that it adds
// little value. Tightening CSP is a follow-up gated on bundling those
// assets locally.
func securityHeaders() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h := w.Header()
			h.Set("X-Content-Type-Options", "nosniff")
			h.Set("X-Frame-Options", "DENY")
			h.Set("Referrer-Policy", "no-referrer")
			next.ServeHTTP(w, r)
		})
	}
}

// limitBody caps the request body size to n bytes for every request.
// The app has no large-upload endpoints (create-room takes an empty
// body, the WS upgrade is a bodyless GET), so a small cap is a cheap
// guard against a client streaming an unbounded body to exhaust memory.
// It wraps r.Body lazily, so the bodyless WS GET is unaffected.
func limitBody(n int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			r.Body = http.MaxBytesReader(w, r.Body, n)
			next.ServeHTTP(w, r)
		})
	}
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
