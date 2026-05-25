package room

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
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
	// Manager.Close or a future idle reaper).
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
}

// playerSlot is the room-layer record of a player. It holds the rejoin
// secret and the currently-attached subscriber (nil if disconnected).
type playerSlot struct {
	id     game.PlayerID
	secret string
	sub    *Subscriber
}

// Config tunes a room. All fields are optional; zero values get safe
// defaults. We keep this minimal — only knobs we actually need today.
type Config struct {
	// GameID is the engine's game identifier; if empty, the room
	// generates one from its code.
	GameID game.GameID

	// Roles is the multiset of roles for the upcoming game. The room
	// requires len(players) == len(Roles) before StartGame succeeds.
	// If empty, defaults to a 5-player roster.
	Roles []game.Role

	// Seed is the deterministic shuffle seed passed to the engine.
	// 0 (the default) is a valid seed.
	Seed int64

	// PhaseDurations controls how long each timed phase lasts before
	// the room auto-advances. Phases not in the map have no automatic
	// timeout (PhaseLobby and PhaseEnded are intentionally untimed).
	// If nil, defaults are applied: Night 30s, Discussion 60s, Vote 30s.
	//
	// A duration of 0 (e.g. for a phase you want to disable) means no
	// timer — useful for tests that drive AdvancePhase manually.
	PhaseDurations map[game.Phase]time.Duration

	// Logger is used for room-lifetime events. Defaults to slog.Default().
	Logger *slog.Logger
}

// DefaultPhaseDurations are the per-phase auto-advance timers a room
// uses if Config.PhaseDurations is nil. Exposed for callers that want
// to override one entry without re-declaring the rest.
var DefaultPhaseDurations = map[game.Phase]time.Duration{
	game.PhaseNight:         30 * time.Second,
	game.PhaseDayDiscussion: 60 * time.Second,
	game.PhaseDayVote:       30 * time.Second,
}

func (c *Config) applyDefaults() {
	if len(c.Roles) == 0 {
		c.Roles = []game.Role{
			game.RoleMafia, game.RoleDetective, game.RoleDoctor,
			game.RoleVillager, game.RoleVillager,
		}
	}
	if c.PhaseDurations == nil {
		c.PhaseDurations = DefaultPhaseDurations
	}
	if c.Logger == nil {
		c.Logger = slog.Default()
	}
}

// newRoom constructs a Room and primes its engine with CreateGame. The
// caller (Manager) then calls Run in a goroutine.
func newRoom(parent context.Context, code string, cfg Config) (*Room, error) {
	cfg.applyDefaults()

	ctx, cancel := context.WithCancel(parent)
	r := &Room{
		code:    code,
		cfg:     cfg,
		log:     cfg.Logger.With("room", code),
		inbox:   make(chan inbound, inboxCapacity),
		ctx:     ctx,
		cancel:  cancel,
		done:    make(chan struct{}),
		g:       game.New(),
		players: make(map[game.PlayerID]*playerSlot),
		subs:    make(map[*Subscriber]struct{}),
	}

	gid := cfg.GameID
	if gid == "" {
		gid = game.GameID("game-" + code)
	}
	_, err := r.g.Apply(game.CreateGame{GameID: gid, Roles: cfg.Roles, Seed: cfg.Seed})
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

// handlePhaseTimer is invoked when the current phase's duration
// elapses. It synthesizes an AdvancePhase as if a host had clicked
// "next phase" — the engine validates the transition itself, so a
// stale fire (e.g. phase already advanced) just becomes a no-op error
// that we drop.
func (r *Room) handlePhaseTimer() {
	r.phaseTimer = nil // consumed; resetPhaseTimer below will recreate
	events, err := r.g.Apply(game.AdvancePhase{})
	if err != nil {
		// AdvancePhase can fail in PhaseEnded, etc. Not an error from
		// the timer's perspective.
		r.log.Debug("phase timer advance rejected", "err", err)
		r.resetPhaseTimer()
		return
	}
	r.appendAndBroadcast(events)
	r.resetPhaseTimer()
}

// resetPhaseTimer sets phaseTimer for the current phase based on
// cfg.PhaseDurations. If the current phase has no configured duration
// (or duration is 0), the timer is cleared.
func (r *Room) resetPhaseTimer() {
	r.stopPhaseTimer()

	phase := r.g.State().Phase()
	dur := r.cfg.PhaseDurations[phase]
	if dur <= 0 {
		return
	}
	r.phaseTimer = time.NewTimer(dur)
}

// stopPhaseTimer cleanly stops phaseTimer if it is running. Safe to
// call repeatedly. Necessary on phase changes (so the new timer
// doesn't double up) and on shutdown.
func (r *Room) stopPhaseTimer() {
	if r.phaseTimer == nil {
		return
	}
	// Stop returns false if the timer has already fired or been
	// stopped. In either case we don't need to drain — the run loop
	// only reads timer.C inside the same goroutine, so there's no
	// pending receive to worry about.
	r.phaseTimer.Stop()
	r.phaseTimer = nil
}

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
		r.sendOne(m.From, errorFor(err))
		return
	}

	slot := &playerSlot{id: pid, secret: secret, sub: m.From}
	r.players[pid] = slot
	r.subs[m.From] = struct{}{}
	m.From.setPlayerID(pid)

	// First player to join is the host. This is a room-level concept;
	// the engine doesn't care.
	isHost := r.host == ""
	if isHost {
		r.host = pid
	}

	r.sendOne(m.From, OutJoined{
		PlayerID: pid,
		Secret:   secret,
		RoomCode: r.code,
		IsHost:   isHost,
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
	if slot.sub != nil && slot.sub != m.From {
		delete(r.subs, slot.sub)
		close(slot.sub.out)
	}
	slot.sub = m.From
	r.subs[m.From] = struct{}{}
	m.From.setPlayerID(m.PlayerID)

	r.sendOne(m.From, OutRejoined{
		PlayerID: m.PlayerID,
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
	if _, ok := r.subs[m.From]; ok {
		delete(r.subs, m.From)
		close(m.From.out)
	}
}

// handleCommand applies an engine command, rewriting any actor-identity
// fields on the command to match the originating subscriber. This is
// the auth boundary: clients cannot impersonate other players even by
// crafting the command with another PlayerID.
func (r *Room) handleCommand(m inCommand) {
	if m.From == nil {
		return
	}
	pid := m.From.PlayerID()
	if pid == "" {
		r.sendOne(m.From, OutError{Code: "not_joined", Message: "join first"})
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

// appendAndBroadcast records events into the room's log and sends each
// one (after per-player projection) to every connected subscriber.
//
// If a subscriber's outbound buffer is full, we consider them too slow
// and disconnect them; the room continues. This is a hard "fail closed"
// stance — better to drop a flaky connection than to back-pressure the
// whole room.
func (r *Room) appendAndBroadcast(events []game.Event) {
	if len(events) == 0 {
		return
	}
	r.events = append(r.events, events...)

	phaseChanged := false
	for _, e := range events {
		if _, ok := e.(game.PhaseChanged); ok {
			phaseChanged = true
			break
		}
	}

	for sub := range r.subs {
		viewer := sub.PlayerID()
		filtered := game.Project(viewer, events, r.g.State())
		for _, e := range filtered {
			if !r.sendOne(sub, OutEvent{Event: e}) {
				// sendOne returns false on a full channel.
				// Disconnect: drop from subs and close their channel.
				r.disconnectSlow(sub)
				break
			}
		}
	}

	if phaseChanged {
		r.resetPhaseTimer()
	}
}

// sendOne attempts a non-blocking send to a subscriber. Returns true on
// success, false if the channel is full (subscriber too slow).
func (r *Room) sendOne(sub *Subscriber, msg Outbound) bool {
	select {
	case sub.out <- msg:
		return true
	default:
		return false
	}
}

// disconnectSlow drops a slow subscriber from the room and closes its
// outbound channel. The player slot is NOT removed — they can rejoin.
func (r *Room) disconnectSlow(sub *Subscriber) {
	r.log.Warn("disconnecting slow subscriber", "player", sub.PlayerID())
	pid := sub.PlayerID()
	if slot, ok := r.players[pid]; ok && slot.sub == sub {
		slot.sub = nil
	}
	delete(r.subs, sub)
	close(sub.out)
}

// shutdownSubscribers closes every connected subscriber's channel on
// room exit. Called via defer in Run.
func (r *Room) shutdownSubscribers() {
	for sub := range r.subs {
		close(sub.out)
		delete(r.subs, sub)
	}
}

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
	// Commands without an actor field (StartGame, AdvancePhase) pass
	// through unchanged. Host-only enforcement is a future addition;
	// see TODO below.
	// TODO(host-only): reject StartGame / AdvancePhase from non-host
	// subscribers once we model that policy.
	default:
		return cmd
	}
}

// errorFor maps an engine sentinel into an outError with a stable
// machine-readable Code.
func errorFor(err error) OutError {
	switch {
	case errors.Is(err, game.ErrWrongPhase):
		return OutError{Code: "wrong_phase", Message: err.Error()}
	case errors.Is(err, game.ErrUnknownPlayer):
		return OutError{Code: "unknown_player", Message: err.Error()}
	case errors.Is(err, game.ErrDuplicatePlayer):
		return OutError{Code: "duplicate_player", Message: err.Error()}
	case errors.Is(err, game.ErrPlayerDead):
		return OutError{Code: "player_dead", Message: err.Error()}
	case errors.Is(err, game.ErrNotYourAction):
		return OutError{Code: "not_your_action", Message: err.Error()}
	case errors.Is(err, game.ErrSelfTarget):
		return OutError{Code: "self_target", Message: err.Error()}
	case errors.Is(err, game.ErrRosterMismatch):
		return OutError{Code: "roster_mismatch", Message: err.Error()}
	case errors.Is(err, game.ErrGameEnded):
		return OutError{Code: "game_ended", Message: err.Error()}
	case errors.Is(err, game.ErrNoChange):
		return OutError{Code: "no_change", Message: err.Error()}
	case errors.Is(err, game.ErrAlreadyActed):
		return OutError{Code: "already_acted", Message: err.Error()}
	default:
		return OutError{Code: "internal", Message: err.Error()}
	}
}

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
