package ws

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/go-chi/chi/v5"

	"github.com/96malhar/mafia-the-game/internal/game"
	"github.com/96malhar/mafia-the-game/internal/room"
)

// Handler bundles the WebSocket upgrade endpoint and the small JSON
// HTTP endpoints around it (create-room). It holds a reference to the
// shared room.Manager and applies sensible defaults for production use.
type Handler struct {
	mgr    *room.Manager
	logger *slog.Logger
	cfg    HandlerConfig
}

// HandlerConfig tunes the WebSocket handler. Zero values get safe
// defaults.
type HandlerConfig struct {
	// AllowedOrigins is the list of origins permitted for WebSocket
	// upgrades. If nil, the handler trusts the request's Host header
	// (suitable for local dev and single-origin deploys).
	//
	// In production set this explicitly to the public origin(s).
	AllowedOrigins []string

	// InsecureSkipOriginCheck disables origin checking entirely.
	// Useful for local development with a separate frontend dev
	// server. NEVER set this in production.
	InsecureSkipOriginCheck bool

	// JoinDeadline bounds how long a fresh (non-rejoin) connection may
	// stay open before completing a join. Zero uses defaultJoinDeadline.
	// Primarily a test seam for exercising the reaper without a 30s wait.
	JoinDeadline time.Duration
}

// NewHandler constructs a Handler. The manager must outlive the
// handler; cancelling the manager's parent context (via its Close)
// will shut down all rooms and break any active WebSocket pumps.
func NewHandler(mgr *room.Manager, logger *slog.Logger, cfg HandlerConfig) *Handler {
	if logger == nil {
		logger = slog.Default()
	}
	return &Handler{mgr: mgr, logger: logger, cfg: cfg}
}

// CreateRoom handles POST /api/rooms. The body is currently empty; we
// reserve it for future room-config knobs (custom roster, seed, etc.).
//
// Each room gets a fresh crypto-random shuffle seed here. This is the
// production entrypoint, so it's the right place to inject entropy: the
// engine and tests stay deterministic-by-seed (Config.Seed defaults to
// 0), while real games shuffle roles independently. Without this every
// game shared Seed=0, making role assignment a fixed function of join
// order — predictable and exploitable.
//
// Response: { "code": "ABCD" }
func (h *Handler) CreateRoom(w http.ResponseWriter, _ *http.Request) {
	r, err := h.mgr.CreateRoom(room.Config{Logger: h.logger, Seed: randSeed()})
	if err != nil {
		// At-capacity is an expected, transient condition (the server
		// is full, not broken) — 503 lets clients/proxies treat it as
		// retryable and distinguishes it from a genuine 500.
		if errors.Is(err, room.ErrAtCapacity) {
			http.Error(w, "server is at capacity, try again shortly", http.StatusServiceUnavailable)
			return
		}
		h.logger.Error("create room failed", "err", err)
		http.Error(w, "could not create room", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"code": r.Code()})
}

// randSeed returns 8 bytes of OS entropy as an int64, used as the
// per-room role-shuffle seed. crypto/rand is the source so seeds aren't
// guessable from a known PRNG sequence. On the practically-impossible
// event the OS RNG is unavailable it falls back to the wall clock —
// far weaker, but a never-fail path matters more here than seed quality,
// since failing room creation over a shuffle seed would be absurd.
func randSeed() int64 {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return time.Now().UnixNano()
	}
	return int64(binary.BigEndian.Uint64(b[:]))
}

// Connect handles GET /ws/{code}. It upgrades the connection and runs
// the read/write pumps until either side disconnects.
//
// Authentication:
//   - Fresh join: no query params. The first client message MUST be a
//     {type:"join", data:{name:"..."}}.
//   - Rejoin: ?playerId=p1&secret=xxx. The handler issues the rejoin
//     itself (no first-message dance) so reconnects are instant.
func (h *Handler) Connect(w http.ResponseWriter, req *http.Request) {
	code := chi.URLParam(req, "code")
	r, err := h.mgr.Get(code)
	if err != nil {
		http.Error(w, "room not found", http.StatusNotFound)
		return
	}

	conn, err := websocket.Accept(w, req, &websocket.AcceptOptions{
		OriginPatterns:     h.cfg.AllowedOrigins,
		InsecureSkipVerify: h.cfg.InsecureSkipOriginCheck,
	})
	if err != nil {
		// Accept already wrote the HTTP error response on its own.
		h.logger.Info("ws upgrade failed", "err", err, "remote", req.RemoteAddr)
		return
	}

	// Track the live connection for the lifetime of the handler (both
	// pumps run below and we block on wg.Wait, so the defer fires on
	// disconnect).
	recordConnOpen()
	defer recordConnClose()

	// Per-connection context. Cancelled when either pump exits, when
	// the request is cancelled (client disconnects mid-handler), or
	// when the manager shuts down.
	ctx, cancel := context.WithCancel(req.Context())

	sub := room.NewSubscriber()

	// Decide between fresh join and rejoin based on query params.
	playerID := req.URL.Query().Get("playerId")
	secret := req.URL.Query().Get("secret")

	isRejoin := playerID != "" && secret != ""
	if isRejoin {
		// Rejoin path: submit the rejoin immediately so the client
		// receives the snapshot on its first read.
		if err := r.SubmitRejoin(ctx, sub, game.PlayerID(playerID), secret); err != nil {
			h.logger.Info("rejoin submit failed", "err", err)
			_ = conn.Close(websocket.StatusInternalError, "rejoin failed")
			cancel()
			return
		}
	}
	// Fresh-join path: the readPump will translate the client's first
	// {type:"join"} frame into r.SubmitJoin. A join deadline bounds
	// that first authentication so a connection that never joins can't
	// leak goroutines (see defaultJoinDeadline). Rejoins are already
	// authenticated above, so they're exempt (deadline 0 = no reaper).
	joinDeadline := time.Duration(0)
	if !isRejoin {
		joinDeadline = h.cfg.JoinDeadline
		if joinDeadline <= 0 {
			joinDeadline = defaultJoinDeadline
		}
	}

	// Run the two pumps. We deliberately give each one half the wait
	// group; both must exit before we tear down. WaitGroup is safer
	// than a single channel because we don't want partial cleanup if
	// only one pump panics.
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		readPump(ctx, cancel, h.logger.With("pump", "read", "room", code), conn, r, sub, joinDeadline)
	}()
	go func() {
		defer wg.Done()
		writePump(ctx, cancel, h.logger.With("pump", "write", "room", code), conn, sub)
	}()
	wg.Wait()

	// At this point both pumps have exited. Tell the room the
	// subscriber is gone so it can clear the playerSlot.sub link. A
	// bounded ctx prevents indefinite blocking if the room is jammed.
	leaveCtx, leaveCancel := context.WithCancel(context.Background())
	defer leaveCancel()
	if err := r.SubmitLeave(leaveCtx, sub); err != nil && !errors.Is(err, room.ErrRoomClosed) {
		h.logger.Info("leave submit failed", "err", err)
	}

	// Be tidy about the underlying socket. If either pump already
	// closed it this is a no-op.
	_ = conn.CloseNow()
}
