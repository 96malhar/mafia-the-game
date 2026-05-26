package room

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	mrand "math/rand/v2"
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

// Config tunes a room. All fields are optional; zero values get safe
// defaults. We keep this minimal — only knobs we actually need today.
type Config struct {
	// GameID is the engine's game identifier; if empty, the room
	// generates one from its code.
	GameID game.GameID

	// MinPlayers, MaxPlayers, MafiaCount describe the variable lobby
	// configuration the engine will use. Zero values fall back to
	// engine defaults (currently 5..20 players, ~maxPlayers/3 mafia).
	MinPlayers int
	MaxPlayers int
	MafiaCount int

	// Seed is the deterministic shuffle seed passed to the engine.
	// 0 (the default) is a valid seed.
	Seed int64

	// PhaseDurations controls per-phase auto-advance timers. With the
	// host-driven daytime flow (BeginNight / OpenVoting / ClearVotes /
	// FinalizeVotes), Day phases NO LONGER auto-advance by default —
	// the host paces them. Tests can still set durations here to drive
	// auto-advance behavior, but the production default is empty.
	//
	// PhaseLobby and PhaseEnded are intentionally untimed.
	//
	// NOTE: PhaseNight is turn-ordered. Use NightActionDuration +
	// NightTurnGrace + PhantomTurnDuration to control per-role timing;
	// a PhaseNight entry here has no effect.
	//
	// A duration of 0 disables the timer for that phase — useful for
	// tests that drive things manually.
	PhaseDurations map[game.Phase]time.Duration

	// NightActionDuration is the time a role has to ACT on its night
	// turn, measured from the moment the spoken narration prompt
	// finishes. Default 10 seconds.
	//
	// The total per-turn deadline broadcast to clients is
	//   NightTurnGrace(role, day) + NightActionDuration
	// so that the actor still gets their full action window even when
	// the prompt audio (or its visible-card fallback) is mid-playback
	// when the turn starts.
	NightActionDuration time.Duration

	// NightTurnGrace returns the audio-grace period for the given role
	// on the given day (engine's state.day, 0 for the first night).
	// It's added BEFORE NightActionDuration to form the per-turn
	// deadline. Default returns DefaultNightTurnGrace.
	//
	// Day 0 mafia gets a longer grace because the narration includes
	// the "look around and recognize each other" beat.
	NightTurnGrace func(role game.Role, day int) time.Duration

	// PhantomTurnDuration returns the wall-clock duration of a phantom
	// night turn — a turn for a role with no living holder. The
	// narrator audio still plays so the room can't deduce a role is
	// dead from the missing cues; but no action will be accepted, so
	// we shorten the timer to something plausible (a fast-acting
	// actor's range) rather than the full grace + 45s.
	//
	// Default DefaultPhantomTurnDuration returns a uniformly-random
	// duration in [8s, 20s]. The room owns the randomness so the
	// engine stays deterministic.
	PhantomTurnDuration func(role game.Role, day int) time.Duration

	// DetectivePauseDuration is the wall-clock pause inserted AFTER a
	// detective records a night action and BEFORE the next role's
	// turn begins. The engine emits DetectiveResult at action time
	// (so the detective's modal pops immediately) but stops short of
	// starting the doctor's turn — this pause is what gives the
	// detective a beat to read the result before audio narration for
	// the doctor kicks in. Default DefaultDetectivePauseDuration.
	DetectivePauseDuration time.Duration

	// MaxLifetime is the hard upper bound on a room's wall-clock age.
	// Once a room has existed for this long the manager's sweeper
	// closes it unconditionally — no matter how many subscribers are
	// connected, no matter whether the game has ended. This is the
	// ONLY reap policy: rooms with active connections, idle lobbies,
	// completed games, and abandoned-but-attached zombies all live
	// up to this cap and then get cleared.
	//
	// Counts from CreateRoom. Default DefaultMaxLifetime. Zero or
	// negative disables reaping (useful for tests / future
	// deployments).
	MaxLifetime time.Duration

	// Logger is used for room-lifetime events. Defaults to slog.Default().
	Logger *slog.Logger
}

// DefaultPhaseDurations is the empty map used when Config.PhaseDurations
// is nil — daytime pacing is host-driven, so there's nothing to auto-
// advance by default. Tests can pass explicit non-zero durations to
// keep their loops short.
var DefaultPhaseDurations = map[game.Phase]time.Duration{}

// DefaultNightActionDuration is the action window every role gets on
// its night turn, measured from when the narration prompt finishes.
const DefaultNightActionDuration = 45 * time.Second

// DefaultDetectivePauseDuration is how long the room waits after a
// detective records a night action before kicking off the next
// queued role's turn. Tuned to comfortably cover "read the modal +
// click Got it" without dragging the night out.
const DefaultDetectivePauseDuration = 3 * time.Second

// DefaultMaxLifetime caps how long any room may live before the
// manager forcibly closes it. Long enough that no real game session
// — including breaks, rules debates, and the social postgame — will
// brush up against it; short enough that abandoned rooms (any
// flavor: empty, full of zombies, ended) don't accumulate forever
// on a long-running server.
const DefaultMaxLifetime = 10 * time.Hour

// DefaultNightTurnGrace returns the per-role audio grace used when
// Config.NightTurnGrace is nil. The values are tuned to match the
// narrator script in web/index.html — kept in sync intentionally so
// the action window starts roughly when the spoken prompt ends.
//
// Day 0 mafia spans two utterances with a beat between them:
//
//	"Mafia, wake up. Look around and recognize each other." (~2.5s)
//	[3.6s pause]
//	"Mafia, choose your target." (~1.5s)
//
// So the grace there is ~5.5s. Other roles play a single short prompt
// (~2s) and get a small buffer to round out to 2.5s.
func DefaultNightTurnGrace(role game.Role, day int) time.Duration {
	if role == game.RoleMafia && day == 0 {
		return 5500 * time.Millisecond
	}
	return 2500 * time.Millisecond
}

// PhantomTurnMin and PhantomTurnMax bound the randomized duration of a
// phantom night turn. The values are tuned so a phantom turn feels like
// a real turn where the actor decided quickly: long enough to be
// plausible, short enough not to drag the night out per dead role.
const (
	PhantomTurnMin = 8 * time.Second
	PhantomTurnMax = 20 * time.Second
)

// DefaultPhantomTurnDuration returns a uniformly-random wall-clock
// duration in [PhantomTurnMin, PhantomTurnMax] for a phantom (dead-
// role) night turn. We bake in the audio grace for Day 0 mafia so the
// "look around" beat has time to play; for other roles the lower
// bound already comfortably covers the ~2.5s prompt.
func DefaultPhantomTurnDuration(role game.Role, day int) time.Duration {
	lo, hi := PhantomTurnMin, PhantomTurnMax
	if role == game.RoleMafia && day == 0 {
		// The Day-0 mafia narration takes ~5.5s on its own; bump the
		// floor so the phantom turn never undercuts the audio.
		if lo < 7*time.Second {
			lo = 7 * time.Second
		}
	}
	span := hi - lo
	if span <= 0 {
		return lo
	}
	return lo + time.Duration(mrand.Int64N(int64(span)+1))
}

func (c *Config) applyDefaults() {
	// Zero values for MinPlayers/MaxPlayers/MafiaCount are intentionally
	// left as-is so the engine's CreateGame can apply its own defaults
	// (keeping the "what's the default lobby?" answer in one place).
	if c.PhaseDurations == nil {
		c.PhaseDurations = DefaultPhaseDurations
	}
	if c.NightActionDuration == 0 {
		c.NightActionDuration = DefaultNightActionDuration
	}
	if c.NightTurnGrace == nil {
		c.NightTurnGrace = DefaultNightTurnGrace
	}
	if c.PhantomTurnDuration == nil {
		c.PhantomTurnDuration = DefaultPhantomTurnDuration
	}
	if c.DetectivePauseDuration == 0 {
		c.DetectivePauseDuration = DefaultDetectivePauseDuration
	}
	if c.MaxLifetime == 0 {
		c.MaxLifetime = DefaultMaxLifetime
	}
	if c.Logger == nil {
		c.Logger = slog.Default()
	}
}

// nightTurnDuration returns the total per-turn deadline for the given
// role on the given day. Real turns use NightTurnGrace +
// NightActionDuration; phantom turns (no living holder of role) use
// the shorter PhantomTurnDuration to avoid stalling the night while
// still letting the audio cue play.
func (c *Config) nightTurnDuration(role game.Role, day int, phantom bool) time.Duration {
	if phantom {
		return c.PhantomTurnDuration(role, day)
	}
	return c.NightTurnGrace(role, day) + c.NightActionDuration
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

// handlePhaseTimer is invoked when the current phase's (or night
// turn's) duration elapses. It synthesizes an AdvancePhase, which the
// engine interprets context-sensitively:
//   - In a day phase, it transitions to the next phase.
//   - In a night, it ends the current role's turn.
//
// appendAndBroadcast handles arming the next timer in either case
// (per-turn during Night, per-phase during day phases), so we don't
// re-arm here.
func (r *Room) handlePhaseTimer() {
	r.phaseTimer = nil
	events, err := r.g.Apply(game.AdvancePhase{})
	if err != nil {
		// AdvancePhase fails in PhaseEnded; not a timer-level error.
		r.log.Debug("phase timer advance rejected", "err", err)
		return
	}
	r.appendAndBroadcast(events)
}

// resetPhaseTimer sets phaseTimer for the current phase based on
// cfg.PhaseDurations. If the current phase has no configured duration
// (or duration is 0), the timer is cleared.
//
// Note: PhaseNight is intentionally skipped here — Night timing is
// driven by per-turn timers (resetNightTurnTimer), keyed off
// NightTurnStarted events rather than the phase entry itself.
func (r *Room) resetPhaseTimer() {
	r.stopPhaseTimer()

	phase := r.g.State().Phase()
	if phase == game.PhaseNight {
		// Per-turn timer takes over; the NightTurnStarted that fires
		// alongside this PhaseChanged will arm it via resetNightTurnTimer.
		return
	}
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
		r.sendOne(m.From, errorFor(err))
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
	if slot.sub != nil && slot.sub != m.From {
		delete(r.subs, slot.sub)
		close(slot.sub.out)
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
	// Pre-process: stamp wall-clock deadlines on NightTurnStarted (the
	// engine is timeless and emits Deadline=0). We do this BEFORE
	// appending so the log retains the real deadlines — late joiners
	// reconstructing state from a projected event stream see the same
	// timing the original viewers saw.
	// State.Day() at this point reflects the night currently in
	// progress: Day 0 for the first night, Day 1 for the second, etc.
	// We pass it to nightTurnDuration so the grace can scale (Night 1
	// mafia gets a longer audio grace for the "look around" beat).
	day := r.g.State().Day()
	// Capture the most recent NightTurnStarted in this batch so the
	// timer-arming pass below knows whether the just-started turn is
	// phantom (shortened wall-clock window, no action accepted).
	var lastNightTurnPhantom bool
	for i := range events {
		if ts, ok := events[i].(game.NightTurnStarted); ok {
			if ts.Deadline == 0 {
				dur := r.cfg.nightTurnDuration(ts.Role, day, ts.Phantom)
				ts.Deadline = time.Now().Add(dur).UnixMilli()
				events[i] = ts
			}
			lastNightTurnPhantom = ts.Phantom
		}
	}
	r.events = append(r.events, events...)

	// Scan for phase / turn transitions so we can (re)arm timers AFTER
	// all subscribers have been notified.
	phaseChanged := false
	nightTurnStarted := false
	nightTurnEnded := false
	detectiveResult := false
	for _, e := range events {
		switch e.(type) {
		case game.PhaseChanged:
			phaseChanged = true
		case game.NightTurnStarted:
			nightTurnStarted = true
		case game.NightTurnEnded:
			nightTurnEnded = true
		case game.DetectiveResult:
			detectiveResult = true
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

	// Timer management. Daytime pacing is host-driven, so the only
	// auto-advance is the Night per-turn timer. We still call
	// resetPhaseTimer on PhaseChanged in case PhaseDurations was set
	// (tests), but in production it'll be a no-op for day phases.
	if phaseChanged {
		r.resetPhaseTimer()
	}
	if nightTurnStarted {
		r.resetNightTurnTimer(lastNightTurnPhantom)
	}
	if nightTurnEnded && !nightTurnStarted && !phaseChanged {
		// NightTurnEnded with no immediate NightTurnStarted is the
		// engine signalling a deliberate pause — currently this only
		// happens after a detective action. We arm a short timer
		// here; when it fires, handlePhaseTimer sends AdvancePhase
		// which pops the next queued role (engine's advanceFromNight
		// handles the "currentNightRole=='' but queue non-empty"
		// case as "start next turn"). Without this timer the night
		// would silently hang.
		//
		// If something else ever produces NightTurnEnded without
		// NightTurnStarted, double-check this branch — for now,
		// detectiveResult is the only known producer.
		if detectiveResult {
			r.armDetectivePauseTimer()
		} else {
			r.stopPhaseTimer()
		}
	}
}

// armDetectivePauseTimer schedules the next-turn kickoff that the
// engine intentionally didn't issue inside applyNightAction (see
// rules_night.go's detective branch). The timer fires AdvancePhase
// via handlePhaseTimer, which pops the next queued night role.
func (r *Room) armDetectivePauseTimer() {
	r.stopPhaseTimer()
	dur := r.cfg.DetectivePauseDuration
	if dur <= 0 {
		// Misconfigured to zero — fall back to immediate advance
		// rather than hanging the night.
		dur = time.Millisecond
	}
	r.phaseTimer = time.NewTimer(dur)
}

// resetNightTurnTimer arms the per-turn timer for the role that has
// just become active. The duration matches the deadline we stamped
// onto the outbound NightTurnStarted event a moment ago, so the
// server and clients agree on when this turn ends.
//
// We read the role and day from engine state rather than the event
// because by this point in appendAndBroadcast the engine has already
// applied them and CurrentNightRole() is the freshly-started role.
// The phantom flag is passed in by appendAndBroadcast (sourced from
// the NightTurnStarted event) — phantom turns use the shorter
// PhantomTurnDuration instead of grace + action.
func (r *Room) resetNightTurnTimer(phantom bool) {
	r.stopPhaseTimer()
	role := r.g.State().CurrentNightRole()
	day := r.g.State().Day()
	dur := r.cfg.nightTurnDuration(role, day, phantom)
	if dur <= 0 {
		return
	}
	r.phaseTimer = time.NewTimer(dur)
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
	r.detachSubscriber(sub)
}

// attachSubscriber adds a subscriber to r.subs. Helper exists for
// symmetry with detachSubscriber and as the obvious extension point
// if we ever bring back subscriber-based reap policies.
func (r *Room) attachSubscriber(sub *Subscriber) {
	r.subs[sub] = struct{}{}
}

// detachSubscriber removes a subscriber from r.subs and closes its
// outbound channel. The close() is safe to call once and only once
// per subscriber — both call sites (handleLeave, disconnectSlow)
// gate on r.subs membership to enforce that.
func (r *Room) detachSubscriber(sub *Subscriber) {
	if _, ok := r.subs[sub]; !ok {
		return
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
	// Commands without an actor field (StartGame, BeginNight, etc.)
	// pass through unchanged. Host-only authorization is checked in
	// handleCommand via isHostOnly before this function runs.
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
	case errors.Is(err, game.ErrLobbyFull):
		return OutError{Code: "lobby_full", Message: err.Error()}
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
