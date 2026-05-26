package room

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/malhar/mafia-the-game/internal/game"
)

// inboxCapacity bounds the room's inbound queue. Larger than the
// per-subscriber outbound buffer because all subscribers funnel into
// this one channel.
const inboxCapacity = 128

// Room is one active game session plus its connected subscribers. It
// runs in its own goroutine started by Manager.CreateRoom and is the
// sole mutator of its fields. External code must not read or write
// these fields directly — talk to the room via Submit.
type Room struct {
	code string
	cfg  Config
	log  *slog.Logger

	// inbox is the single point of entry. All state changes flow
	// through here.
	inbox chan inbound

	// ctx is cancelled when the room is shutting down (e.g. via
	// Manager.Close or the lifetime reaper).
	ctx    context.Context
	cancel context.CancelFunc

	// done is closed after the run loop exits, so Manager can wait
	// for full shutdown.
	done chan struct{}

	// --- run-loop-only fields below (no concurrent access) ---

	g       *game.Game
	host    game.PlayerID
	players map[game.PlayerID]*playerSlot
	subs    map[*Subscriber]struct{} // currently-connected subscribers
	events  []game.Event             // full event log (truth, unredacted)

	// nextSeq grows by 1 with each PlayerJoined; we use it to mint
	// stable, human-readable PlayerIDs like "p1", "p2".
	nextSeq int

	// phaseTimer fires when the current phase's duration elapses,
	// causing the run loop to synthesize an AdvancePhase command. nil
	// when no phase-timeout is active (lobby, ended, or untimed phases).
	phaseTimer *time.Timer

	// createdAt is the wall-clock moment newRoom returned. Combined
	// with cfg.MaxLifetime it determines when the manager's sweeper
	// reaps this room. Immutable after construction; read-only from
	// any goroutine.
	createdAt time.Time
}

// playerSlot is the room-layer record of a player. It holds the rejoin
// secret, display name, and the currently-attached subscriber (nil if
// disconnected). The engine has no concept of a name beyond its
// PlayerJoined event payload; we keep our own copy here so rejoins can
// echo it back.
type playerSlot struct {
	id     game.PlayerID
	name   string
	secret string
	sub    *Subscriber
}

// newRoom constructs a Room and primes its engine with CreateGame. The
// caller (Manager) then calls Run in a goroutine.
func newRoom(parent context.Context, code string, cfg Config) (*Room, error) {
	cfg.applyDefaults()

	ctx, cancel := context.WithCancel(parent)
	r := &Room{
		code:      code,
		cfg:       cfg,
		log:       cfg.Logger.With("room", code),
		inbox:     make(chan inbound, inboxCapacity),
		ctx:       ctx,
		cancel:    cancel,
		done:      make(chan struct{}),
		g:         game.New(),
		players:   make(map[game.PlayerID]*playerSlot),
		subs:      make(map[*Subscriber]struct{}),
		createdAt: time.Now(),
	}

	gid := cfg.GameID
	if gid == "" {
		gid = game.GameID("game-" + code)
	}
	_, err := r.g.Apply(game.CreateGame{
		GameID:     gid,
		MinPlayers: cfg.MinPlayers,
		MaxPlayers: cfg.MaxPlayers,
		MafiaCount: cfg.MafiaCount,
		Seed:       cfg.Seed,
	})
	if err != nil {
		cancel()
		return nil, fmt.Errorf("room: CreateGame failed: %w", err)
	}
	// We DON'T append GameCreated to r.events: the event is purely a
	// room-construction artifact, not interesting to any player view.
	return r, nil
}

// Code returns the room's join code.
func (r *Room) Code() string { return r.code }

// SubmitJoin asks the room to attach a brand-new subscriber. The
// subscriber must have been created with NewSubscriber and not yet
// joined any room.
func (r *Room) SubmitJoin(ctx context.Context, sub *Subscriber, name string) error {
	return r.submit(ctx, inJoin{From: sub, Name: name})
}

// SubmitRejoin asks the room to reattach a subscriber to an existing
// player slot using the rejoin secret. If auth fails the room sends an
// OutError to the subscriber (and SubmitRejoin still returns nil — the
// failure flows through the outbound channel, not the return value).
func (r *Room) SubmitRejoin(ctx context.Context, sub *Subscriber, pid game.PlayerID, secret string) error {
	return r.submit(ctx, inRejoin{From: sub, PlayerID: pid, Secret: secret})
}

// SubmitLeave detaches a subscriber from its player slot. The player
// remains in the game and can rejoin.
func (r *Room) SubmitLeave(ctx context.Context, sub *Subscriber) error {
	return r.submit(ctx, inLeave{From: sub})
}

// SubmitCommand applies an engine-level command on behalf of a
// subscriber. The room rewrites identity fields on the command to
// match the subscriber's authenticated PlayerID, so callers must not
// rely on Actor / Voter fields they set themselves.
func (r *Room) SubmitCommand(ctx context.Context, sub *Subscriber, cmd game.Command) error {
	return r.submit(ctx, inCommand{From: sub, Cmd: cmd})
}

// requestLifetimeCheck non-blockingly asks the room to self-evaluate
// its age against MaxLifetime. Used by Manager's sweeper goroutine.
// If the inbox is full (a busy room can wait one tick), we
// skip — the next sweep will retry.
//
// Package-private because only the manager should call it; we
// don't want HTTP handlers nudging rooms toward shutdown.
func (r *Room) requestLifetimeCheck() {
	select {
	case <-r.ctx.Done():
		return
	case r.inbox <- inLifetimeCheck{}:
	default:
		// Inbox full; next sweep will retry.
	}
}

// submit enqueues an inbound message for the run loop. It is the only
// internal path that touches r.inbox. The call blocks if the inbox is
// full (which means the room is overloaded or stuck); use ctx to bound
// the wait.
//
// Once the room is closed, submit always returns ErrRoomClosed (even
// if the inbox still has spare capacity). The fast-path check ahead of
// the select guarantees this — without it, a uniform-random select
// pick would let occasional sends through during shutdown.
func (r *Room) submit(ctx context.Context, msg inbound) error {
	select {
	case <-r.ctx.Done():
		return ErrRoomClosed
	default:
	}

	select {
	case <-r.ctx.Done():
		return ErrRoomClosed
	case <-ctx.Done():
		return ctx.Err()
	case r.inbox <- msg:
		return nil
	}
}

// Close requests shutdown and waits up to ctx for the run loop to exit.
// All subscribers' outbound channels are closed by the run loop on
// the way out.
func (r *Room) Close(ctx context.Context) error {
	r.cancel()
	select {
	case <-r.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Run is the room's main loop. It exits when the context is cancelled
// or Close is called. Run blocks; callers typically `go r.Run()`.
//
// The select arms in priority order:
//  1. ctx.Done — shutdown signal.
//  2. inbox    — subscriber messages and commands.
//  3. timer.C  — phase auto-advance fires.
//
// Go's select picks among ready arms uniformly at random, so we don't
// guarantee priority on ties; the listed order is just narrative.
func (r *Room) Run() {
	defer close(r.done)
	defer r.shutdownSubscribers()
	defer r.stopPhaseTimer()

	for {
		// The timer channel may be nil (no active phase timer); a
		// nil-channel arm in a select blocks forever, which is what we
		// want — it disables the arm cleanly.
		var timerC <-chan time.Time
		if r.phaseTimer != nil {
			timerC = r.phaseTimer.C
		}

		select {
		case <-r.ctx.Done():
			return
		case msg, ok := <-r.inbox:
			if !ok {
				return
			}
			r.dispatch(msg)
		case <-timerC:
			r.handlePhaseTimer()
		}
	}
}

// Phase-timer helpers (handlePhaseTimer / resetPhaseTimer /
// stopPhaseTimer / armDetectivePauseTimer / resetNightTurnTimer)
// live in timers.go.

// dispatch handles one inbound message. Each branch is a small focused
// helper for readability.
func (r *Room) dispatch(msg inbound) {
	switch m := msg.(type) {
	case inJoin:
		r.handleJoin(m)
	case inRejoin:
		r.handleRejoin(m)
	case inLeave:
		r.handleLeave(m)
	case inCommand:
		r.handleCommand(m)
	case inLifetimeCheck:
		r.handleLifetimeCheck()
	default:
		r.log.Warn("unknown inbound", "type", fmt.Sprintf("%T", msg))
	}
}

// handleJoin creates a new player slot, attaches the subscriber, and
// applies AddPlayer to the engine. The subscriber is then notified of
// its assigned PlayerID and rejoin secret, and the engine event
// (PlayerJoined) is broadcast to everyone.
func (r *Room) handleJoin(m inJoin) {
	if m.From == nil {
		return // defensive: shouldn't happen
	}

	r.nextSeq++
	pid := game.PlayerID(fmt.Sprintf("p%d", r.nextSeq))
	secret, err := newSecret()
	if err != nil {
		r.sendOne(m.From, OutError{Code: "internal", Message: "could not allocate identity"})
		return
	}

	events, err := r.g.Apply(game.AddPlayer{PlayerID: pid, Name: m.Name})
	if err != nil {
		// joinErrorFor gives lobby-closed errors a player-facing
		// message; all other codes pass through unchanged.
		r.sendOne(m.From, joinErrorFor(err))
		return
	}

	slot := &playerSlot{id: pid, name: m.Name, secret: secret, sub: m.From}
	r.players[pid] = slot
	r.attachSubscriber(m.From)
	m.From.setPlayerID(pid)

	// First player to join is the host. This is a room-level concept;
	// the engine doesn't care.
	isHost := r.host == ""
	if isHost {
		r.host = pid
	}

	// Project the PRIOR event log so the new player can see who's
	// already in the room. r.events at this point contains everything
	// that happened before this join; the new PlayerJoined will be
	// broadcast separately via appendAndBroadcast below.
	priorEvents := game.Project(pid, r.events, r.g.State())

	r.sendOne(m.From, OutJoined{
		PlayerID: pid,
		Name:     m.Name,
		Secret:   secret,
		RoomCode: r.code,
		IsHost:   isHost,
		Events:   priorEvents,
	})
	r.appendAndBroadcast(events)
}

// handleRejoin reattaches a subscriber to an existing slot. If the
// secret doesn't match (or the player ID is unknown), we send outError
// and discard the subscriber without disturbing other state.
func (r *Room) handleRejoin(m inRejoin) {
	if m.From == nil {
		return
	}
	slot, ok := r.players[m.PlayerID]
	if !ok || slot.secret != m.Secret {
		r.sendOne(m.From, OutError{Code: "auth_failed", Message: "unknown player or bad secret"})
		return
	}

	// If a previous subscriber is still attached, evict it. The most
	// common cause is a tab reload that hasn't yet sent inLeave.
	// Route through detachSubscriber so the close/delete semantics
	// live in one place (matches handleLeave and disconnectSlow).
	if slot.sub != nil && slot.sub != m.From {
		r.detachSubscriber(slot.sub)
	}
	slot.sub = m.From
	r.attachSubscriber(m.From)
	m.From.setPlayerID(m.PlayerID)

	r.sendOne(m.From, OutRejoined{
		PlayerID: m.PlayerID,
		Name:     slot.name,
		RoomCode: r.code,
		IsHost:   m.PlayerID == r.host,
		Events:   game.Project(m.PlayerID, r.events, r.g.State()),
	})
}

// handleLeave detaches a subscriber from its player slot but does not
// remove the player from the game. The player can rejoin with their
// secret; meanwhile they're treated as disconnected (no broadcasts).
func (r *Room) handleLeave(m inLeave) {
	if m.From == nil {
		return
	}
	pid := m.From.PlayerID()
	if slot, ok := r.players[pid]; ok && slot.sub == m.From {
		slot.sub = nil
	}
	r.detachSubscriber(m.From)
}

// handleLifetimeCheck evaluates whether the room has exceeded its
// hard lifetime cap and, if so, self-cancels (which causes Run to
// exit and the manager's reapWhenDone goroutine to drop it from
// the registry).
//
// Policy: time.Since(createdAt) > cfg.MaxLifetime. That is the only
// criterion. Subscriber count and game phase are NOT consulted —
// active games approaching the cap get force-closed too, which is a
// deliberate tradeoff for predictable resource bounds.
//
// MaxLifetime <= 0 disables reaping. Useful for tests and as a
// future deployment knob.
func (r *Room) handleLifetimeCheck() {
	if r.cfg.MaxLifetime <= 0 {
		return
	}
	if time.Since(r.createdAt) < r.cfg.MaxLifetime {
		return
	}
	r.log.Info("reaping room past max lifetime",
		"created_at", r.createdAt,
		"max_lifetime", r.cfg.MaxLifetime)
	r.cancel()
}

// handleCommand applies an engine command, rewriting any actor-identity
// fields on the command to match the originating subscriber. This is
// the auth boundary: clients cannot impersonate other players even by
// crafting the command with another PlayerID.
//
// Two additional gates beyond identity rewriting:
//
//  1. Host-only commands (StartGame, BeginNight, OpenVoting,
//     ClearVotes, FinalizeVotes, SetMafiaCount) are rejected from
//     non-host subscribers with a "forbidden" error.
//  2. AdvancePhase is INTERNAL — it's the room's per-turn-timer
//     signal. Forwarding it from a client would let any player skip
//     the active night turn, so we reject those outright.
func (r *Room) handleCommand(m inCommand) {
	if m.From == nil {
		return
	}
	pid := m.From.PlayerID()
	if pid == "" {
		r.sendOne(m.From, OutError{Code: "not_joined", Message: "join first"})
		return
	}

	if _, isAdvance := m.Cmd.(game.AdvancePhase); isAdvance {
		r.sendOne(m.From, OutError{
			Code:    "forbidden",
			Message: "advancePhase is server-internal",
		})
		return
	}

	if isHostOnly(m.Cmd) && pid != r.host {
		r.sendOne(m.From, OutError{
			Code:    "forbidden",
			Message: "only the host can issue this command",
		})
		return
	}

	cmd := rewriteActor(m.Cmd, pid)
	events, err := r.g.Apply(cmd)
	if err != nil {
		r.sendOne(m.From, errorFor(err))
		return
	}
	r.appendAndBroadcast(events)
}

// isHostOnly reports whether the command requires the host privilege.
// Player actions (NightAction, DayVote) are excluded — those go through
// rewriteActor and the engine's own role/turn checks.
func isHostOnly(cmd game.Command) bool {
	switch cmd.(type) {
	case game.StartGame,
		game.BeginNight,
		game.OpenVoting,
		game.ClearVotes,
		game.FinalizeVotes,
		game.SetMafiaCount:
		return true
	}
	return false
}

// Event fan-out (appendAndBroadcast and its helpers), per-subscriber
// send / disconnect / attach / detach, and shutdownSubscribers all
// live in broadcast.go.

// --- Helpers --------------------------------------------------------------

// rewriteActor overwrites the actor-identity fields on a command to
// match the authenticated PlayerID. This is the room's authorization
// layer: a client cannot send NightAction{Actor: someoneElse, ...}
// because the room rewrites Actor to its own subscriber's pid.
func rewriteActor(cmd game.Command, pid game.PlayerID) game.Command {
	switch c := cmd.(type) {
	case game.NightAction:
		c.Actor = pid
		return c
	case game.DayVote:
		c.Voter = pid
		return c
	// Commands without an actor field (StartGame, BeginNight, etc.)
	// pass through unchanged. Host-only authorization is checked in
	// handleCommand via isHostOnly before this function runs.
	default:
		return cmd
	}
}

// errorFor (engine-sentinel → OutError mapping) lives in errors.go.

// secret length in bytes; base64-encoded length will be ~22 chars.
const secretBytes = 16

// secretEntropy guards newSecret with a mutex so tests can override
// the randomness source. We don't expose this; it's package-private.
var secretEntropy = struct {
	sync.Mutex
}{}

func newSecret() (string, error) {
	secretEntropy.Lock()
	defer secretEntropy.Unlock()
	buf := make([]byte, secretBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}
