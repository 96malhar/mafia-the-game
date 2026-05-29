// Command server runs the Mafia game HTTP/WebSocket server.
//
// Configuration is entirely via environment variables (see envConfig):
//
//	ADDR                        listen address            (default ":8080")
//	LOG_LEVEL                   debug|info|warn|error     (default "info")
//	ALLOWED_ORIGINS             comma-separated WS origin allowlist
//	INSECURE_SKIP_ORIGIN_CHECK  disable WS origin check   (default false)
//	TRUSTED_CLIENT_IP_HEADER    proxy header for real IP  (e.g. Fly-Client-IP)
//	ROOM_CREATE_RPM             per-IP room-create limit  (0 = disabled)
//
// Local dev needs none of these: the secure defaults work for same-
// origin access via http://localhost:8080.
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/malhar/mafia-the-game/internal/room"
	"github.com/malhar/mafia-the-game/internal/server"
	"github.com/malhar/mafia-the-game/internal/transport/ws"
)

// version is the build version, injected at link time via
// -ldflags "-X main.version=...". Defaults to "dev" for un-stamped
// builds (plain `go run` / `go build`). The release workflow passes the
// release tag; the Dockerfile threads it through a VERSION build-arg.
var version = "dev"

func main() {
	cfg := loadEnvConfig()

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: cfg.logLevel,
	}))

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
		"origin_check", !cfg.insecureSkipOriginCheck,
		"allowed_origins", cfg.allowedOrigins,
		"trusted_ip_header", cfg.trustedClientIPHeader,
		"room_create_rpm", cfg.roomCreateRPM,
	)

	srv := server.New(server.Config{
		Addr:                  cfg.addr,
		WebFS:                 os.DirFS("web"),
		WS:                    wsHandler,
		Logger:                logger,
		TrustedClientIPHeader: cfg.trustedClientIPHeader,
		RoomCreateRPM:         cfg.roomCreateRPM,
	})

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
		if err := mgr.Close(ctx); err != nil {
			logger.Error("manager shutdown failed", "err", err)
		}
	}
}

// envConfig is the fully-resolved runtime configuration read from the
// environment, with defaults applied.
type envConfig struct {
	addr                    string
	logLevel                slog.Level
	allowedOrigins          []string
	insecureSkipOriginCheck bool
	trustedClientIPHeader   string
	roomCreateRPM           int
}

func loadEnvConfig() envConfig {
	return envConfig{
		addr:                    envOr("ADDR", ":8080"),
		logLevel:                parseLogLevel(os.Getenv("LOG_LEVEL")),
		allowedOrigins:          parseCSV(os.Getenv("ALLOWED_ORIGINS")),
		insecureSkipOriginCheck: parseBool(os.Getenv("INSECURE_SKIP_ORIGIN_CHECK")),
		trustedClientIPHeader:   strings.TrimSpace(os.Getenv("TRUSTED_CLIENT_IP_HEADER")),
		roomCreateRPM:           parseInt(os.Getenv("ROOM_CREATE_RPM"), 0),
	}
}

func envOr(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func parseLogLevel(s string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// parseCSV splits a comma-separated env value into trimmed, non-empty
// entries. Returns nil for an empty/blank input so callers see the
// "unset" case as a nil slice.
func parseCSV(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
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
