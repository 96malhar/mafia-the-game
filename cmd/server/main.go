// Command server runs the Mafia game HTTP/WebSocket server.
//
// Usage:
//
//	go run ./cmd/server          # listens on :8080
//	ADDR=:9000 go run ./cmd/server
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/malhar/mafia-the-game/internal/server"
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))

	addr := os.Getenv("ADDR")
	if addr == "" {
		addr = ":8080"
	}

	srv := server.New(server.Config{
		Addr:   addr,
		WebFS:  os.DirFS("web"),
		Logger: logger,
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
	}
}
