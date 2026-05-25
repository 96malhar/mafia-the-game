package ws

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"sync"

	"github.com/coder/websocket"
	"github.com/go-chi/chi/v5"

	"github.com/malhar/mafia-the-game/internal/game"
	"github.com/malhar/mafia-the-game/internal/room"
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
// Response: { "code": "ABCD" }
func (h *Handler) CreateRoom(w http.ResponseWriter, _ *http.Request) {
	r, err := h.mgr.CreateRoom(room.Config{Logger: h.logger})
	if err != nil {
		h.logger.Error("create room failed", "err", err)
		http.Error(w, "could not create room", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"code": r.Code()})
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

	// Per-connection context. Cancelled when either pump exits, when
	// the request is cancelled (client disconnects mid-handler), or
	// when the manager shuts down.
	ctx, cancel := context.WithCancel(req.Context())

	sub := room.NewSubscriber()

	// Decide between fresh join and rejoin based on query params.
	playerID := req.URL.Query().Get("playerId")
	secret := req.URL.Query().Get("secret")

	if playerID != "" && secret != "" {
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
	// {type:"join"} frame into r.SubmitJoin.

	// Run the two pumps. We deliberately give each one half the wait
	// group; both must exit before we tear down. WaitGroup is safer
	// than a single channel because we don't want partial cleanup if
	// only one pump panics.
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		readPump(ctx, cancel, h.logger.With("pump", "read", "room", code), conn, r, sub)
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
