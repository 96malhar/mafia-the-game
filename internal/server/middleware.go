package server

import (
	"log/slog"
	"net/http"
	"runtime/debug"
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
//
// It logs through the request context and scales severity to the
// response status: 5xx -> Error, 4xx -> Warn, and the happy path
// (incl. WS upgrades) -> Debug. The intent is "log when something is
// wrong": at the default info level only failed requests appear, while
// per-request volume/latency lives in the http_server_* metrics exposed
// at /metrics. Set LOG_LEVEL=debug to get a full access log.
//
// Successful /healthz probes are dropped entirely — Fly polls it every
// few seconds, so even at debug it's pure noise; a failing probe still
// logs (status >= 400). (/metrics is served on a separate port that
// never reaches this middleware.)
func requestLogger(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
			next.ServeHTTP(ww, r)

			status := ww.Status()
			if status < 400 && r.URL.Path == "/healthz" {
				return
			}

			level := slog.LevelDebug
			switch {
			case status >= 500:
				level = slog.LevelError
			case status >= 400:
				level = slog.LevelWarn
			}
			logger.LogAttrs(r.Context(), level, "http",
				slog.String("method", r.Method),
				slog.String("path", r.URL.Path),
				slog.Int("status", status),
				slog.Int("bytes", ww.BytesWritten()),
				slog.Int64("dur_ms", time.Since(start).Milliseconds()),
				slog.String("req_id", middleware.GetReqID(r.Context())),
				slog.String("remote", r.RemoteAddr),
			)
		})
	}
}

// recoverer catches panics from downstream handlers, logs them through
// slog (with stack + request context, so they're structured and carry
// trace IDs) at Error level, and returns 500. We use this instead of
// chi's middleware.Recoverer, which writes the stack to stderr outside
// our logger.
func recoverer(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				rec := recover()
				if rec == nil {
					return
				}
				// net/http uses ErrAbortHandler as a sentinel to abort a
				// connection silently; honour it rather than logging.
				if rec == http.ErrAbortHandler {
					panic(rec)
				}
				logger.LogAttrs(r.Context(), slog.LevelError, "panic recovered",
					slog.Any("panic", rec),
					slog.String("method", r.Method),
					slog.String("path", r.URL.Path),
					slog.String("req_id", middleware.GetReqID(r.Context())),
					slog.String("stack", string(debug.Stack())),
				)
				w.WriteHeader(http.StatusInternalServerError)
			}()
			next.ServeHTTP(w, r)
		})
	}
}
