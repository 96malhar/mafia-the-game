// Command sim runs a scripted Mafia game against a running server.
//
// It treats the server as a black box reachable via HTTP + WebSocket on
// the public JSON wire format. The sim:
//
//  1. POSTs /api/rooms to create a fresh game.
//  2. Spawns N bot clients in goroutines; each joins, then plays a
//     deterministic role-aware strategy.
//  3. From the main goroutine, advances phases on a fixed cadence so
//     the bots get a chance to act before each transition.
//  4. Waits for a "gameEnded" event from any bot, prints the winner
//     and revealed roles, and exits.
//
// The simulator is intentionally not a load test — it runs ONE game.
// For load tests we'd parameterise concurrency.
//
// Usage:
//
//	# In one shell: start the server.
//	task run
//
//	# In another: run a sim.
//	task sim
//	task sim -- -addr=http://localhost:9000 -players=5 -tick=500ms
//
// Exit codes:
//
//	0  game completed; winner printed.
//	1  fatal setup error (server unreachable, etc.).
//	2  hard timeout — no gameEnded within -timeout.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"
)

func main() {
	addr := flag.String("addr", "http://localhost:8080", "server base URL")
	nPlayers := flag.Int("players", 5, "number of bot players (must match the server's roster size)")
	tick := flag.Duration("tick", 500*time.Millisecond, "delay between phase advances")
	timeout := flag.Duration("timeout", 30*time.Second, "hard cap on total game duration")
	verbose := flag.Bool("verbose", false, "enable debug-level logging")
	flag.Parse()

	logLevel := slog.LevelInfo
	if *verbose {
		logLevel = slog.LevelDebug
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: logLevel}))

	// Honour Ctrl-C so a stuck sim is easy to interrupt.
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	sigCtx, _ := signalContext(ctx)

	if err := run(sigCtx, logger, *addr, *nPlayers, *tick); err != nil {
		if errors.Is(err, errGameTimeout) {
			logger.Error("sim timed out", "err", err)
			os.Exit(2)
		}
		logger.Error("sim failed", "err", err)
		os.Exit(1)
	}
}

// errGameTimeout is returned when the game doesn't complete in time.
var errGameTimeout = errors.New("sim: game did not end within timeout")

func run(ctx context.Context, logger *slog.Logger, addr string, nPlayers int, tick time.Duration) error {
	code, err := createRoom(ctx, addr)
	if err != nil {
		return fmt.Errorf("create room: %w", err)
	}
	logger.Info("room created", "code", code)

	wsURL := httpToWS(addr) + "/ws/" + code

	bots := make([]*Bot, nPlayers)
	for i := 0; i < nPlayers; i++ {
		bots[i] = NewBot(fmt.Sprintf("Bot%d", i+1), logger)
	}

	// Connect + join all bots. We do this sequentially so PlayerIDs
	// come out in order (p1, p2, ... pN), making strategy decisions
	// deterministic across runs.
	for _, b := range bots {
		if err := b.Connect(ctx, wsURL); err != nil {
			return fmt.Errorf("%s connect: %w", b.name, err)
		}
		if err := b.Join(ctx); err != nil {
			return fmt.Errorf("%s join: %w", b.name, err)
		}
	}
	defer func() {
		for _, b := range bots {
			b.Close()
		}
	}()

	// Each bot runs in its own goroutine. The first one to see
	// gameEnded posts to `ended`.
	ended := make(chan evGameEnded, 1)
	var wg sync.WaitGroup
	for _, b := range bots {
		wg.Add(1)
		go func(b *Bot) {
			defer wg.Done()
			if err := b.Run(ctx, ended); err != nil {
				logger.Warn("bot exited", "name", b.name, "err", err)
			}
		}(b)
	}

	// Drive phase advancement from the host bot. We wait `tick` so
	// every bot has a chance to act on phase entry before we push the
	// game forward.
	//
	// Sequence (default 5-player roster, 1 mafia / 1 detective / 1 doctor / 2 villagers):
	//
	//   startGame -> phase = night            (bots: mafia/doctor/detective act)
	//   advancePhase -> phase = day_discussion
	//   advancePhase -> phase = day_vote      (bots vote)
	//   advancePhase -> phase = night (or ended)
	//   ...
	host := bots[0]
	go func() {
		// Brief settle before we kick off so all PlayerJoined events
		// have been processed by everyone.
		time.Sleep(tick)

		if err := host.send(ctx, "startGame", struct{}{}); err != nil {
			logger.Error("startGame send failed", "err", err)
			return
		}
		// After startGame, the room enters Night. Keep advancing until
		// gameEnded fires (handled separately) or context cancels.
		ticker := time.NewTicker(tick)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				// We don't track phase here; just advance. The engine
				// rejects no-ops with no ill effect.
				if err := host.send(ctx, "advancePhase", struct{}{}); err != nil {
					return
				}
			}
		}
	}()

	// Wait for either gameEnded or context timeout.
	select {
	case end := <-ended:
		printResult(end)
		// The caller's deferred cancel will tear down the pumps; we
		// give bots a brief moment to drain in case any have buffered
		// frames in flight, then return.
		drainDone := make(chan struct{})
		go func() { wg.Wait(); close(drainDone) }()
		select {
		case <-drainDone:
		case <-time.After(time.Second):
		}
		return nil
	case <-ctx.Done():
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return errGameTimeout
		}
		return ctx.Err()
	}
}

// createRoom hits POST /api/rooms and returns the new room code.
func createRoom(ctx context.Context, addr string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, addr+"/api/rooms", nil)
	if err != nil {
		return "", err
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		return "", fmt.Errorf("server returned %s", res.Status)
	}
	var body struct {
		Code string `json:"code"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		return "", err
	}
	if body.Code == "" {
		return "", errors.New("server returned empty code")
	}
	return body.Code, nil
}

// printResult formats and prints the game outcome. We deliberately
// write to stdout (separate from the logger's stderr) so it's easy to
// pipe sim output for scripting: `task sim | jq` etc.
func printResult(end evGameEnded) {
	fmt.Println("=== GAME OVER ===")
	fmt.Printf("winner: %s\n", end.Winner)
	fmt.Println("final roles:")
	// Sort by player ID for stable output across runs.
	keys := make([]string, 0, len(end.FinalRoles))
	for k := range end.FinalRoles {
		keys = append(keys, k)
	}
	sortPlayerIDs(keys)
	var b bytes.Buffer
	for _, pid := range keys {
		fmt.Fprintf(&b, "  %s: %s\n", pid, end.FinalRoles[pid])
	}
	_, _ = os.Stdout.Write(b.Bytes())
}

func sortPlayerIDs(ids []string) {
	// Reuse the strategy package's numeric sort by inserting into a
	// map and back out via sortedAlive — cheap and avoids a duplicate
	// sort implementation.
	m := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		m[id] = struct{}{}
	}
	out := sortedAlive(m)
	copy(ids, out)
}

func httpToWS(addr string) string {
	if strings.HasPrefix(addr, "https://") {
		return "wss://" + strings.TrimPrefix(addr, "https://")
	}
	return "ws://" + strings.TrimPrefix(addr, "http://")
}

// signalContext returns a context that is cancelled when SIGINT/SIGTERM
// arrives, layered on top of parent.
func signalContext(parent context.Context) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(parent)
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		select {
		case <-ctx.Done():
		case <-ch:
			cancel()
		}
		signal.Stop(ch)
	}()
	return ctx, cancel
}
