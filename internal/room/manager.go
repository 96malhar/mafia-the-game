package room

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// codeAlphabet is the Crockford-style set used for room codes:
// uppercase letters minus I, L, O, U (visually confusable / accidentally
// rude). 22 letters; 22^4 = 234,256 codes — plenty for our scale.
const codeAlphabet = "ABCDEFGHJKMNPQRSTVWXYZ"

const codeLength = 4

// maxCodeAttempts bounds the retry loop when generating a unique code.
// Even with 100 active rooms, the collision probability is ~0.04% per
// attempt — 100 retries gives a vanishing failure rate.
const maxCodeAttempts = 100

// DefaultSweepInterval is how often the manager polls every room to
// see if it has exceeded MaxLifetime. The sweep is cheap (one non-
// blocking inbox push per room) so a tight cadence is fine, but the
// lifetime cap is hours-scale — minute granularity is plenty.
const DefaultSweepInterval = 60 * time.Second

// Sentinel errors returned by the room/manager API.
var (
	// ErrRoomNotFound is returned by Manager.Get when the code is unknown.
	ErrRoomNotFound = errors.New("room: not found")

	// ErrRoomClosed is returned by Room.Submit after the room's
	// context has been cancelled (typically by Manager.Close).
	ErrRoomClosed = errors.New("room: closed")
)

// Manager is the in-memory registry of active rooms. It is safe for
// concurrent use from HTTP handlers.
//
// Rooms own their own goroutines; Manager only tracks the set, mints
// codes, provides lookup, and runs the periodic lifetime-sweeper
// that reaps rooms past their MaxLifetime cap.
type Manager struct {
	mu     sync.RWMutex
	rooms  map[string]*Room
	logger *slog.Logger

	// ctx is the parent context passed to every new Room. Cancelling
	// it via Close shuts down every room in the registry.
	ctx    context.Context
	cancel context.CancelFunc

	// sweepInterval is how often runSweeper polls each room to
	// check whether it has exceeded MaxLifetime. Defaults to
	// DefaultSweepInterval; tests override to a sub-second cadence
	// so they don't have to sleep for hours.
	sweepInterval time.Duration
}

// ManagerOption tunes a Manager at construction. We use the functional-
// options pattern (rather than another big config struct) because
// these knobs are nearly always defaulted and only exist for tests.
type ManagerOption func(*Manager)

// WithSweepInterval overrides the periodic lifetime-sweep cadence.
// Primarily for tests that need fast reaping; production code
// should leave the default.
func WithSweepInterval(d time.Duration) ManagerOption {
	return func(m *Manager) { m.sweepInterval = d }
}

// NewManager constructs a Manager and starts its background
// lifetime-sweeper goroutine. The parent context governs the
// lifetime of every room it owns AND the sweeper; cancelling it (or
// calling Close) shuts down everything.
func NewManager(parent context.Context, logger *slog.Logger, opts ...ManagerOption) *Manager {
	if logger == nil {
		logger = slog.Default()
	}
	ctx, cancel := context.WithCancel(parent)
	m := &Manager{
		rooms:         make(map[string]*Room),
		logger:        logger,
		ctx:           ctx,
		cancel:        cancel,
		sweepInterval: DefaultSweepInterval,
	}
	for _, opt := range opts {
		opt(m)
	}
	go m.runSweeper()
	return m
}

// CreateRoom mints a unique code, constructs a Room, and starts its
// run loop. The returned Room is ready to accept Submit calls.
func (m *Manager) CreateRoom(cfg Config) (*Room, error) {
	if cfg.Logger == nil {
		cfg.Logger = m.logger
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	code, err := m.allocCodeLocked()
	if err != nil {
		return nil, err
	}

	r, err := newRoom(m.ctx, code, cfg)
	if err != nil {
		return nil, err
	}
	m.rooms[code] = r

	go r.Run()
	go m.reapWhenDone(r)

	m.logger.Info("room created", "code", code)
	return r, nil
}

// Get looks up a room by code, returning ErrRoomNotFound if absent.
func (m *Manager) Get(code string) (*Room, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	r, ok := m.rooms[code]
	if !ok {
		return nil, ErrRoomNotFound
	}
	return r, nil
}

// Close shuts down the manager and every room it owns, waiting for
// each room's run loop to exit. Bounded by ctx's deadline.
func (m *Manager) Close(ctx context.Context) error {
	m.cancel()

	m.mu.Lock()
	rooms := make([]*Room, 0, len(m.rooms))
	for _, r := range m.rooms {
		rooms = append(rooms, r)
	}
	m.mu.Unlock()

	for _, r := range rooms {
		if err := r.Close(ctx); err != nil {
			return err
		}
	}
	return nil
}

// allocCodeLocked finds a free room code. Caller must hold m.mu.
func (m *Manager) allocCodeLocked() (string, error) {
	for i := 0; i < maxCodeAttempts; i++ {
		code, err := randomCode()
		if err != nil {
			return "", err
		}
		if _, taken := m.rooms[code]; !taken {
			return code, nil
		}
	}
	return "", fmt.Errorf("room: could not allocate a unique code after %d attempts", maxCodeAttempts)
}

// reapWhenDone removes the room from the registry once its run loop
// exits. Runs in its own goroutine started by CreateRoom.
func (m *Manager) reapWhenDone(r *Room) {
	<-r.done
	m.mu.Lock()
	delete(m.rooms, r.code)
	m.mu.Unlock()
	m.logger.Info("room reaped", "code", r.code)
}

// runSweeper periodically asks every registered room to evaluate
// its age against MaxLifetime. The actual reap decision is made
// inside the room's run loop (see Room.handleLifetimeCheck); the
// sweeper just nudges each room on a ticker. This keeps state
// mutation single-threaded per room and avoids the manager taking
// locks on room internals.
//
// Exits when the manager's context is cancelled (Manager.Close).
func (m *Manager) runSweeper() {
	if m.sweepInterval <= 0 {
		return
	}
	t := time.NewTicker(m.sweepInterval)
	defer t.Stop()
	for {
		select {
		case <-m.ctx.Done():
			return
		case <-t.C:
			m.sweepOnce()
		}
	}
}

// sweepOnce nudges every room to evaluate its lifetime. Splits the
// work in two passes (snapshot under read lock, then push without
// holding the lock) so a slow inbox can't block the registry.
func (m *Manager) sweepOnce() {
	m.mu.RLock()
	rooms := make([]*Room, 0, len(m.rooms))
	for _, r := range m.rooms {
		rooms = append(rooms, r)
	}
	m.mu.RUnlock()
	for _, r := range rooms {
		r.requestLifetimeCheck()
	}
}

// randomCode generates a codeLength-character code using
// cryptographically secure randomness. It's overkill for the use case
// (we just need unpredictability), but the crypto/rand path is
// straightforward and avoids any "did we seed math/rand?" risk.
func randomCode() (string, error) {
	buf := make([]byte, codeLength)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	out := make([]byte, codeLength)
	for i, b := range buf {
		out[i] = codeAlphabet[int(b)%len(codeAlphabet)]
	}
	return string(out), nil
}
