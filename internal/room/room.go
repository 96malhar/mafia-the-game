package room

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"log/slog"
	"time"

	"github.com/96malhar/mafia-the-game/internal/game"
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

	// journal is the per-room command-recovery log: every successfully
	// applied command paired with the events it committed (see recovery.go).
	// The run loop replays it to rebuild the engine and event log after a
	// panic, so one bad command can't take down the process.
	journal []journalEntry

	// recoveries / recoveryWindowStart implement the crash-loop guard: how
	// many panics this room has recovered from in the current window. See
	// overRecoveryBudget.
	recoveries          int
	recoveryWindowStart time.Time

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

	// Default the shuffle seed to a fresh random value when the caller
	// didn't specify one (Seed == 0). This makes unpredictable deals the
	// room's own guarantee rather than relying on every caller to pass a
	// seed — a forgotten seed yields a randomized game, not a deterministic
	// one. Callers that DO set a non-zero seed (e.g. a future replay/debug
	// path) keep full control.
	if cfg.Seed == 0 {
		cfg.Seed = seedOrFallback()
	}

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
	createCmd := game.CreateGame{
		GameID:     gid,
		MinPlayers: cfg.MinPlayers,
		MaxPlayers: cfg.MaxPlayers,
		MafiaCount: cfg.MafiaCount,
		Seed:       cfg.Seed,
	}
	createdEvents, err := r.g.Apply(createCmd)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("room: CreateGame failed: %w", err)
	}
	// The GameCreated event (and any other events CreateGame emits)
	// must land in r.events so projections sent to joiners and
	// rejoiners include it. Without this, clients have no way to
	// learn the lobby's MinPlayers/MaxPlayers/MafiaCount — which
	// used to be masked by the client hardcoding the same defaults,
	// but breaks cleanly the moment the client stops doing that.
	//
	// We append directly rather than going through appendAndBroadcast
	// because no subscribers exist yet at this point in newRoom; the
	// first join's OutJoined will fan these events out via the
	// projected priorEvents slice.
	r.events = append(r.events, createdEvents...)
	// Record CreateGame as journal entry 0 so a post-panic rebuild starts
	// from the same construction the live room did (same GameID/config/seed).
	r.record(createCmd, createdEvents, false)
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
func (r *Room) SubmitRejoin(ctx context.Context, sub *Subscriber, pid game.PlayerID, secret string, since int) error {
	return r.submit(ctx, inRejoin{From: sub, PlayerID: pid, Secret: secret, Since: since})
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

// JoinStatus reports whether a fresh join would currently be accepted and, if
// not, a wire-stable code plus a player-facing message (see the JoinStatus
// type). It round-trips through the run loop so the engine read happens on the
// owning goroutine — the room needs no locks.
//
// ctx bounds the wait. A closed room (ErrRoomClosed) or a cancelled/timed-out
// ctx returns the error so the caller can fall back; CheckRoom treats any such
// failure as "assume joinable" and lets the WebSocket attempt surface the real
// rejection — no worse than before this probe reported joinability.
func (r *Room) JoinStatus(ctx context.Context) (JoinStatus, error) {
	// Buffered so handleJoinability's send never blocks the run loop even if
	// this caller has already given up (ctx cancelled) and stopped reading.
	reply := make(chan error, 1)
	if err := r.submit(ctx, inJoinability{reply: reply}); err != nil {
		return JoinStatus{}, err
	}
	select {
	case <-ctx.Done():
		return JoinStatus{}, ctx.Err()
	case reason := <-reply:
		return joinStatusFor(reason), nil
	}
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
		// The timer channel may be nil (timer inactive); a nil-channel
		// arm in a select blocks forever, which is what we want — it
		// disables the arm cleanly.
		var timerC <-chan time.Time
		if r.phaseTimer != nil {
			timerC = r.phaseTimer.C
		}

		// Each unit of work runs under guard so a panic anywhere beneath
		// the run loop (engine, projection, a handler) is caught and
		// converted into a per-room rebuild instead of crashing the whole
		// process and every other game. recoverFromPanic returns false when
		// the room is unrecoverable (crash-loop budget exhausted, or the
		// rebuild itself panicked) and has been cancelled — then we exit.
		select {
		case <-r.ctx.Done():
			return
		case msg, ok := <-r.inbox:
			if !ok {
				return
			}
			if p := guard(func() { r.dispatch(msg) }); p != nil {
				if !r.recoverFromPanic("dispatch", p) {
					return
				}
			}
		case <-timerC:
			if p := guard(r.handlePhaseTimer); p != nil {
				if !r.recoverFromPanic("phase-timer", p) {
					return
				}
			}
		}
	}
}

// Phase-timer helpers (handlePhaseTimer / resetPhaseTimer /
// stopPhaseTimer / armSubPhaseTimer) live in timers.go.

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
	case inJoinability:
		r.handleJoinability(m)
	case inTestHook:
		// Test-only: run a closure on the room goroutine (see inbound.go).
		// Nothing in production constructs this, so the case is inert in a
		// real deployment.
		m.fn(r)
	default:
		r.log.Warn("unknown inbound", "type", fmt.Sprintf("%T", msg))
	}
}

// handleJoin creates a new player slot, attaches the subscriber, and
// applies AddPlayer to the engine. The subscriber is then notified of
// its assigned PlayerID and rejoin secret, and the engine event
// (PlayerJoined) is broadcast to everyone.
func (r *Room) handleJoin(m inJoin) {
	if !m.From.live() {
		return // defensive: nil, or a torn-down connection sent a stray frame
	}

	r.nextSeq++
	pid := game.PlayerID(fmt.Sprintf("p%d", r.nextSeq))
	secret, err := newSecret()
	if err != nil {
		// Terminal: a join that can't mint a credential can't proceed,
		// and the client tears the socket down on a join error anyway.
		r.rejectUnjoined(m.From, errorWithMsg(ErrInternal, "could not allocate identity"))
		return
	}

	addCmd := game.AddPlayer{PlayerID: pid, Name: m.Name}
	events, err := r.g.Apply(addCmd)
	if err != nil {
		// joinErrorFor gives lobby-closed errors a player-facing
		// message; all other codes pass through unchanged. The join
		// failed, so close the channel (the client closes the socket
		// on this error; closing our side too bounds the misbehaving-
		// client case).
		r.rejectUnjoined(m.From, joinErrorFor(err))
		return
	}

	// Read the canonical name back from the engine's PlayerJoined
	// event rather than reusing m.Name. The engine trims leading
	// /trailing whitespace and rejects whitespace-only names; using
	// m.Name here would let the room's playerSlot disagree with the
	// engine for a joiner who submitted "  Alice  " (engine sees
	// "Alice", room would see "  Alice  "), which then surfaces as
	// a mismatched name in OutRejoined on the next refresh.
	// PlayerJoined is guaranteed to be the first event from
	// applyAddPlayer on success.
	canonicalName := m.Name
	if pj, ok := events[0].(game.PlayerJoined); ok {
		canonicalName = pj.Name
	}

	slot := &playerSlot{id: pid, name: canonicalName, secret: secret, sub: m.From}
	r.players[pid] = slot
	r.attachSubscriber(m.From)
	m.From.setPlayerID(pid)

	// First player to join is the host. This is a room-level concept;
	// the engine doesn't care.
	isHost := r.host == ""
	if isHost {
		r.host = pid
		// Append a HostChanged AFTER the PlayerJoined so the
		// broadcast order is "p1 exists, then p1 is host". Clients
		// keying off HostChanged can rely on the referenced player
		// already being in their roster. HostChanged is Public so
		// it lands in every projection — including future
		// late-joiners' priorEvents and rejoin replays.
		events = append(events, game.HostChanged{PlayerID: pid})
	}

	// Project the PRIOR event log so the new player can see who's
	// already in the room. r.events at this point contains everything
	// that happened before this join; the new PlayerJoined (and the
	// host's HostChanged, if any) will be broadcast separately via
	// appendAndBroadcast below.
	priorEvents := game.Project(pid, r.events, r.g.State())

	r.sendOne(m.From, OutJoined{
		PlayerID: pid,
		Name:     canonicalName,
		Secret:   secret,
		RoomCode: r.code,
		IsHost:   isHost,
		// High-water at join time: r.events has not yet grown by this join's
		// own events (appended just below), so its length is the joiner's
		// starting cursor.
		LastSeq: len(r.events),
		Events:  priorEvents,
	})
	r.appendAndBroadcast(events)
	// appendAndBroadcast stamps deadlines in place, so `events` now holds the
	// final committed batch (PlayerJoined plus a first-joiner HostChanged) —
	// journal it against the AddPlayer command for panic recovery.
	r.record(addCmd, events, false)
}

// handleRejoin reattaches a subscriber to an existing slot. If the
// secret doesn't match (or the player ID is unknown), we send outError
// and discard the subscriber without disturbing other state.
func (r *Room) handleRejoin(m inRejoin) {
	if !m.From.live() {
		return
	}
	slot, ok := r.players[m.PlayerID]
	if !ok || slot.secret != m.Secret {
		// Auth failed: the credentials don't match a known slot.
		// Close the channel so the write pump unwinds — unlike a
		// fresh join, a rejoin can't be retried on the same socket
		// (the creds ride the connect URL), so there's nothing to
		// keep the connection open for.
		r.rejectUnjoined(m.From, errorFor(ErrAuthFailed))
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

	// Cursor-driven resume: ship only the projected tail since the client's
	// cursor. A cursor of 0, or one past the current log length (e.g. the
	// client's pre-reset cursor after a GameReset rebaselined the log), falls
	// back to the full projected log with FromSeq=0 so the client rebuilds
	// from scratch.
	total := len(r.events)
	fromSeq := 0
	tail := r.events
	if m.Since > 0 && m.Since <= total {
		fromSeq = m.Since
		tail = r.events[m.Since:]
	}

	r.sendOne(m.From, OutRejoined{
		PlayerID: m.PlayerID,
		Name:     slot.name,
		RoomCode: r.code,
		IsHost:   m.PlayerID == r.host,
		FromSeq:  fromSeq,
		LastSeq:  total,
		Events:   game.Project(m.PlayerID, tail, r.g.State()),
	})
}

// handleLeave detaches a subscriber from its player slot but does not
// remove the player from the game. The player can rejoin with their
// secret; meanwhile they're treated as disconnected (no broadcasts).
func (r *Room) handleLeave(m inLeave) {
	if !m.From.live() {
		return // already torn down (e.g. slow-disconnect or rejected join)
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

// handleJoinability answers a pre-join probe (inJoinability) with the
// engine's current join-block reason — nil when a fresh join would be
// accepted. It is read-only: it touches r.g only through the pure
// JoinBlockedReason query and never appends events or broadcasts. The reply
// channel is buffered by the caller (JoinStatus), so the send never blocks the
// run loop.
func (r *Room) handleJoinability(m inJoinability) {
	m.reply <- r.g.JoinBlockedReason()
}

// handleCommand applies an engine command, rewriting any actor-identity
// fields on the command to match the originating subscriber. This is
// the auth boundary: clients cannot impersonate other players even by
// crafting the command with another PlayerID.
//
// Two additional gates beyond identity rewriting:
//
//  1. Host-only commands (StartGame, BeginNight, OpenVoting,
//     RevealVotes, ClearVotes, FinalizeVotes, SetMafiaCount) are
//     rejected from non-host subscribers with a "forbidden" error.
//  2. AdvancePhase is INTERNAL — it's the room's per-turn-timer
//     signal. Forwarding it from a client would let any player skip
//     the active night turn, so we reject those outright.
func (r *Room) handleCommand(m inCommand) {
	if !m.From.live() {
		// A subscriber whose channel is already closed (slow-
		// disconnect, leave, rejected join) must not be acted on:
		// the error-reply path below would otherwise send on a
		// closed channel and panic the room goroutine.
		return
	}
	pid := m.From.PlayerID()
	if pid == "" {
		r.sendOne(m.From, errorFor(ErrNotJoined))
		return
	}

	if _, isAdvance := m.Cmd.(game.AdvancePhase); isAdvance {
		// Both branches use ErrForbidden but with distinct messages —
		// the user benefits from knowing which privilege they lack.
		// errorWithMsg keeps the typed Code while setting a per-site
		// Message.
		r.sendOne(m.From, errorWithMsg(ErrForbidden, "advancePhase is server-internal"))
		return
	}

	if isHostOnly(m.Cmd) && pid != r.host {
		r.sendOne(m.From, errorWithMsg(ErrForbidden, "only the host can issue this command"))
		return
	}

	// ResetGame is host-only (gated above) but needs special fan-out: it
	// rebaselines the event log instead of appending to it, and the room —
	// not the client — supplies the fresh shuffle seed. Route it to its own
	// handler rather than the generic append-and-broadcast path.
	if _, isReset := m.Cmd.(game.ResetGame); isReset {
		r.handleReset(m.From)
		return
	}

	cmd := rewriteActor(m.Cmd, pid)
	events, err := r.g.Apply(cmd)
	if err != nil {
		r.sendOne(m.From, errorFor(err))
		return
	}
	r.appendAndBroadcast(events)
	// Journal the (post-rewrite, successfully applied) command paired with
	// its now-stamped events, so a panic anywhere downstream can rebuild this
	// exact history. Capturing the post-rewrite command means replay needs no
	// re-authorization.
	r.record(cmd, events, false)
}

// handleReset returns a finished game to a fresh lobby in the same room
// (the host-only ResetGame command). It differs from every other command
// path in two ways, which is why it doesn't go through appendAndBroadcast:
//
//  1. The room — not the client — mints a fresh shuffle seed so replaying
//     with the same roster doesn't redeal identical roles.
//  2. The resulting GameReset is a self-contained lobby snapshot, so the
//     room REPLACES its event log with it (plus a reaffirming HostChanged)
//     rather than appending. The previous game's events are deliberately
//     dropped — nothing from the finished game is replayed to future
//     joiners or rejoiners.
//
// requester is the host's subscriber, used only to deliver an error if the
// engine rejects the reset (e.g. the game isn't actually ended).
func (r *Room) handleReset(requester *Subscriber) {
	// Capture the exact reset command (with its freshly-minted seed) so a
	// post-panic replay redeals identically — calling seedOrFallback again
	// during rebuild would diverge.
	resetCmd := game.ResetGame{Seed: seedOrFallback()}
	events, err := r.g.Apply(resetCmd)
	if err != nil {
		r.sendOne(requester, errorFor(err))
		return
	}

	// Reaffirm the host in the fresh baseline so clients (which reset their
	// local host state on GameReset) re-learn it, and so a post-reset joiner
	// reconstructing from the log alone knows who the host is. The host is a
	// room-level concept and is unchanged by the reset.
	if r.host != "" {
		events = append(events, game.HostChanged{PlayerID: r.host})
	}

	// Any lingering night/phase timer from the finished game must not leak
	// into the new lobby. (PhaseEnded carries no active timer today, but
	// this keeps the invariant explicit and future-proof.)
	r.resetPhaseTimer()

	// Rebaseline the log: the GameReset snapshot is the new beginning of
	// time for this room. broadcastToSubs then projects it to every
	// connected subscriber (both events are Public, so all see them).
	r.events = events
	// Journal the reset as a rebaseline entry so a rebuild replays the full
	// command history (reproducing the engine's post-reset lobby) and then
	// replaces its log with this snapshot, exactly as here.
	r.record(resetCmd, events, true)
	r.broadcastToSubs(events)
}

// isHostOnly reports whether the command requires the host privilege.
// Player actions (NightAction, DayVote) are excluded — those go through
// rewriteActor and the engine's own role/turn checks.
func isHostOnly(cmd game.Command) bool {
	switch cmd.(type) {
	case game.StartGame,
		game.BeginNight,
		game.OpenVoting,
		game.RevealVotes,
		game.ClearVotes,
		game.FinalizeVotes,
		game.SetMafiaCount,
		game.SetConsort,
		game.SetVigilante,
		game.SetYakuza,
		game.SetTracker,
		game.ResetGame:
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
	case game.NightPass:
		c.Actor = pid
		return c
	case game.Recruit:
		c.Actor = pid
		return c
	case game.DayVote:
		c.Voter = pid
		return c
	case game.DayAbstain:
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

// newSecret mints a rejoin credential from crypto/rand. crypto/rand's
// Reader is safe for concurrent use, and newSecret is only ever called
// from the single room goroutine (handleJoin) anyway, so it needs no
// locking of its own.
func newSecret() (string, error) {
	buf := make([]byte, secretBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// newSeed mints a fresh int64 shuffle seed from crypto/rand. The engine's
// deal is deterministic in the seed, so a constant seed would redeal
// identical roles to the same join positions every game; a fresh random
// seed per game (at room creation and on every reset) keeps deals
// unpredictable. crypto/rand is the source so a seed isn't guessable from
// the room code or join order.
func newSeed() (int64, error) {
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return 0, err
	}
	return int64(binary.LittleEndian.Uint64(buf[:])), nil
}

// seedOrFallback returns a fresh random seed, falling back to a time-based
// value if the OS entropy source fails (near-impossible in practice, but we
// never want room creation or a reset to hard-fail over a shuffle seed).
func seedOrFallback() int64 {
	if s, err := newSeed(); err == nil {
		return s
	}
	return time.Now().UnixNano()
}
