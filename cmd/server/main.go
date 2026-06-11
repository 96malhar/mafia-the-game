// Command server runs the Mafia game HTTP/WebSocket server.
//
// Configuration is entirely via environment variables (see envConfig):
//
//	ADDR                        public listen address     (default ":8080")
//	METRICS_ADDR                private /metrics address   (default ":9091")
//	LOG_LEVEL                   debug|info|warn|error     (default "debug")
//	ALLOWED_ORIGINS             comma-separated WS origin allowlist
//	INSECURE_SKIP_ORIGIN_CHECK  disable WS origin check   (default false)
//	TRUSTED_CLIENT_IP_HEADER    proxy header for real IP  (e.g. Fly-Client-IP)
//	ROOM_CREATE_RPM             per-IP room-create limit  (0 = disabled)
//	LOG_FORMAT                  text|json                 (default "text")
//	DEPLOY_ENV                  resource deployment.environment (e.g. "prod")
//
// Local dev needs none of these: the secure defaults work for same-
// origin access via http://localhost:8080. Metrics are exposed at
// GET /metrics on METRICS_ADDR (a separate port from the public app so
// they aren't internet-reachable behind a single-port proxy like fly).
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/96malhar/mafia-the-game/internal/obs"
	"github.com/96malhar/mafia-the-game/internal/room"
	"github.com/96malhar/mafia-the-game/internal/server"
	"github.com/96malhar/mafia-the-game/internal/transport/ws"
	"github.com/96malhar/mafia-the-game/web"
)

// version is the build version, injected at link time via
// -ldflags "-X main.version=...". Defaults to "dev" for un-stamped
// builds (plain `go run` / `go build`). The release workflow passes the
// release tag; the Dockerfile threads it through a VERSION build-arg.
var version = "dev"

func main() {
	cfg := loadEnvConfig()

	// Configure logging and a Prometheus-backed meter provider. The
	// returned handler renders /metrics; metrics are scraped (pull), not
	// pushed, so there's nothing to flush on shutdown.
	logger, metricsHandler, err := obs.Setup(obs.Config{
		ServiceName: "mafia",
		Version:     version,
		Environment: cfg.deployEnv,
		LogLevel:    cfg.logLevel,
		LogFormat:   cfg.logFormat,
	})
	if err != nil {
		slog.Error("observability setup failed", "err", err)
		os.Exit(1)
	}

	// Each WebSocket connection holds a file descriptor, so the soft
	// RLIMIT_NOFILE caps how many players this instance can serve — and the
	// container default (commonly ~1024) is well below what the VM's CPU/memory
	// can handle. Raise the soft limit to the hard limit at startup so the box
	// isn't artificially capped around 1k connections. Distroless has no shell,
	// so a `ulimit` in an entrypoint isn't an option; do it in-process.
	raiseFileLimit(logger)

	// Build the room manager first so the WebSocket handler can reach
	// it. The manager owns its own context which we cancel during
	// shutdown so all rooms drain cleanly.
	managerCtx, cancelManager := context.WithCancel(context.Background())
	defer cancelManager()

	mgr := room.NewManager(managerCtx, logger)

	// Origin checking is ON by default. With no AllowedOrigins, the
	// WebSocket library enforces same-origin (Origin host == request
	// Host), which is correct for single-origin deploys (incl. fly.io)
	// and for localhost dev. Set ALLOWED_ORIGINS to broaden, or
	// INSECURE_SKIP_ORIGIN_CHECK=true ONLY for a cross-origin dev setup.
	wsHandler := ws.NewHandler(mgr, logger, ws.HandlerConfig{
		AllowedOrigins:          cfg.allowedOrigins,
		InsecureSkipOriginCheck: cfg.insecureSkipOriginCheck,
	})

	logger.Info("starting",
		"version", version,
		"addr", cfg.addr,
		"metrics_addr", cfg.metricsAddr,
		"origin_check", !cfg.insecureSkipOriginCheck,
		"allowed_origins", cfg.allowedOrigins,
		"trusted_ip_header", cfg.trustedClientIPHeader,
		"room_create_rpm", cfg.roomCreateRPM,
	)

	srv := server.New(server.Config{
		Addr:                  cfg.addr,
		WebFS:                 web.FS,
		WS:                    wsHandler,
		Logger:                logger,
		TrustedClientIPHeader: cfg.trustedClientIPHeader,
		RoomCreateRPM:         cfg.roomCreateRPM,
	})

	// Serve /metrics on its OWN listener, separate from the public app
	// server. On fly.io the public proxy only routes the ports declared
	// under [http_service]; this port is not one of them, so /metrics is
	// reachable only over the private 6PN network that fly's managed
	// Prometheus scrapes from — never from the public internet.
	metricsSrv := startMetricsServer(cfg.metricsAddr, metricsHandler, logger)

	// Run the server in a goroutine so main() can wait for either
	// a fatal startup error or a shutdown signal, whichever comes first.
	errCh := make(chan error, 1)
	go func() { errCh <- srv.Start() }()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)

	select {
	case err := <-errCh:
		if err != nil {
			logger.Error("server failed", "err", err)
			os.Exit(1)
		}
	case sig := <-sigCh:
		logger.Info("shutting down", "signal", sig.String())
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := srv.Shutdown(ctx); err != nil {
			logger.Error("graceful shutdown failed", "err", err)
			os.Exit(1)
		}
		_ = metricsSrv.Shutdown(ctx)
		if err := mgr.Close(ctx); err != nil {
			logger.Error("manager shutdown failed", "err", err)
		}
	}
}

// raiseFileLimit lifts the process's soft open-file limit (RLIMIT_NOFILE) to
// its hard ceiling, so the number of concurrent WebSocket connections is bound
// by the machine's CPU/memory rather than a low inherited default. It is
// best-effort: any failure is logged and the server continues on the inherited
// limit (raising it is an optimization, not a correctness requirement).
//
// In a Linux container (production) the hard limit is a concrete large value
// (e.g. ~1M), so the soft limit becomes that. On a macOS dev box the hard
// limit is "unlimited"; setting the soft limit there is accepted but the real
// ceiling stays the kernel's per-process max — harmless, since dev never
// approaches it.
func raiseFileLimit(logger *slog.Logger) {
	var lim syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &lim); err != nil {
		logger.Warn("could not read RLIMIT_NOFILE", "err", err)
		return
	}
	if lim.Cur >= lim.Max {
		logger.Info("RLIMIT_NOFILE soft limit already at hard limit", "limit", lim.Cur)
		return
	}
	before := lim.Cur
	lim.Cur = lim.Max
	if err := syscall.Setrlimit(syscall.RLIMIT_NOFILE, &lim); err != nil {
		logger.Warn("could not raise RLIMIT_NOFILE soft limit; using inherited default",
			"err", err, "soft", before, "hard", lim.Max)
		return
	}
	logger.Info("raised RLIMIT_NOFILE soft limit", "from", before, "to", lim.Max)
}

// startMetricsServer runs a minimal HTTP server that serves only the
// Prometheus scrape endpoint at /metrics on addr, in its own goroutine.
// It deliberately has none of the app middleware (no access log, no body
// limit). A failure here is logged but non-fatal: losing metrics must
// never take down live games.
func startMetricsServer(addr string, h http.Handler, logger *slog.Logger) *http.Server {
	mux := http.NewServeMux()
	mux.Handle("/metrics", h)
	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("metrics server failed", "err", err)
		}
	}()
	return srv
}

// envConfig is the fully-resolved runtime configuration read from the
// environment, with defaults applied.
type envConfig struct {
	addr                    string
	metricsAddr             string
	logLevel                slog.Level
	logFormat               string
	deployEnv               string
	allowedOrigins          []string
	insecureSkipOriginCheck bool
	trustedClientIPHeader   string
	roomCreateRPM           int
}

func loadEnvConfig() envConfig {
	return envConfig{
		addr:                    envOr("ADDR", ":8080"),
		metricsAddr:             envOr("METRICS_ADDR", ":9091"),
		logLevel:                parseLogLevel(os.Getenv("LOG_LEVEL")),
		logFormat:               parseLogFormat(os.Getenv("LOG_FORMAT")),
		deployEnv:               strings.TrimSpace(os.Getenv("DEPLOY_ENV")),
		allowedOrigins:          parseCSV(os.Getenv("ALLOWED_ORIGINS")),
		insecureSkipOriginCheck: parseBool(os.Getenv("INSECURE_SKIP_ORIGIN_CHECK")),
		trustedClientIPHeader:   strings.TrimSpace(os.Getenv("TRUSTED_CLIENT_IP_HEADER")),
		roomCreateRPM:           parseInt(os.Getenv("ROOM_CREATE_RPM"), 0),
	}
}

// parseLogFormat normalizes LOG_FORMAT to "json" or "text" (the default).
func parseLogFormat(s string) string {
	if strings.EqualFold(strings.TrimSpace(s), "json") {
		return "json"
	}
	return "text"
}

func envOr(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func parseLogLevel(s string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "info":
		return slog.LevelInfo
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		// Default to debug (incl. unset): verbose by default for local
		// dev. Production sets LOG_LEVEL=info explicitly (see fly.toml).
		return slog.LevelDebug
	}
}

// parseCSV splits a comma-separated env value into trimmed, non-empty
// entries. Returns nil for an empty/blank input so callers see the
// "unset" case as a nil slice.
func parseCSV(s string) []string {
	var out []string
	for part := range strings.SplitSeq(s, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func parseBool(s string) bool {
	v, err := strconv.ParseBool(strings.TrimSpace(s))
	return err == nil && v
}

func parseInt(s string, fallback int) int {
	v, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return fallback
	}
	return v
}
