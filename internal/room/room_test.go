package room

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/malhar/mafia-the-game/internal/game"
	"github.com/malhar/mafia-the-game/internal/wire"
)

// --- helpers --------------------------------------------------------------

// silentLogger discards all log output so tests don't pollute stdout.
func silentLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// recvTimeout is how long we'll wait for an outbound message before
// failing the test. Generous so flaky machines don't fail spuriously.
const recvTimeout = 2 * time.Second

// recv pulls one outbound message from a subscriber, failing the test
// if nothing arrives within recvTimeout.
func recv(t *testing.T, sub *Subscriber) Outbound {
	t.Helper()
	select {
	case msg, ok := <-sub.Outbound():
		require.True(t, ok, "subscriber's outbound channel closed unexpectedly")
		return msg
	case <-time.After(recvTimeout):
		t.Fatalf("timed out waiting for outbound message")
		return nil
	}
}

// recvType is recv with a type assertion: the next message must be of
// type T. Returns the typed value.
func recvType[T Outbound](t *testing.T, sub *Subscriber) T {
	t.Helper()
	msg := recv(t, sub)
	v, ok := msg.(T)
	if !ok {
		t.Fatalf("expected outbound %T, got %T (%+v)", *new(T), msg, msg)
	}
	return v
}

// drain reads any pending messages from sub, with a short deadline,
// returning them all. Used when a test wants to "ignore the broadcast
// that just happened" without asserting on each one.
func drain(sub *Subscriber, deadline time.Duration) []Outbound {
	timer := time.NewTimer(deadline)
	defer timer.Stop()
	var out []Outbound
	for {
		select {
		case msg, ok := <-sub.Outbound():
			if !ok {
				return out
			}
			out = append(out, msg)
		case <-timer.C:
			return out
		}
	}
}

// newTestManager creates a Manager scoped to the test's lifetime.
func newTestManager(t *testing.T) *Manager {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	m := NewManager(ctx, silentLogger())
	t.Cleanup(func() {
		shutdown, c := context.WithTimeout(context.Background(), time.Second)
		defer c()
		_ = m.Close(shutdown)
	})
	return m
}

// newTestRoom creates a manager and one room with default config.
func newTestRoom(t *testing.T) (*Manager, *Room) {
	t.Helper()
	m := newTestManager(t)
	r, err := m.CreateRoom(Config{Logger: silentLogger()})
	require.NoError(t, err)
	return m, r
}

// connect creates a subscriber, sends inJoin, and waits for OutJoined.
// Returns the subscriber and the join ack.
func connect(t *testing.T, r *Room, name string) (*Subscriber, OutJoined) {
	t.Helper()
	sub := NewSubscriber()
	require.NoError(t, r.submit(context.Background(), inJoin{From: sub, Name: name}))
	ack := recvType[OutJoined](t, sub)
	require.Equal(t, name, ack.Name, "join ack must echo the requested name")
	require.NotEmpty(t, ack.PlayerID)
	require.NotEmpty(t, ack.Secret)
	require.Equal(t, r.Code(), ack.RoomCode)
	return sub, ack
}

// --- Join projection includes GameCreated --------------------------------

func TestRoom_FirstJoinerSeesGameCreated(t *testing.T) {
	// Regression: newRoom calls r.g.Apply(CreateGame{...}) at room
	// construction time. The resulting GameCreated event must be
	// appended to r.events so it shows up in the projection sent in
	// OutJoined.Events to the first (and every subsequent) joiner.
	//
	// Without it, the web client has no way to learn the lobby's
	// MinPlayers / MaxPlayers / MafiaCount — which used to be masked
	// by the client hardcoding the same defaults, but breaks the
	// moment the client trusts the server as the single source of
	// truth.
	_, r := newTestRoom(t)
	_, ack := connect(t, r, "Alice")

	var gc *game.GameCreated
	for i := range ack.Events {
		if e, ok := ack.Events[i].(game.GameCreated); ok {
			gc = &e
			break
		}
	}
	require.NotNil(t, gc, "OutJoined.Events must include GameCreated")
	// Assert sanity of the fields, NOT specific numbers. The engine
	// owns the default values; pinning them here would re-create the
	// "two sources of truth" problem this regression is about.
	require.Greater(t, gc.MinPlayers, 0)
	require.GreaterOrEqual(t, gc.MaxPlayers, gc.MinPlayers)
	require.GreaterOrEqual(t, gc.MafiaCount, 1)
}

func TestRoom_LateJoinerSeesGameCreated(t *testing.T) {
	// Same invariant for a non-first joiner — they hit the same
	// projection path, so this is mostly belt-and-braces. If the
	// first joiner stops seeing it, so does everyone else.
	_, r := newTestRoom(t)
	_, _ = connect(t, r, "Alice")
	_, ackB := connect(t, r, "Bob")

	hasGC := false
	for _, e := range ackB.Events {
		if _, ok := e.(game.GameCreated); ok {
			hasGC = true
			break
		}
	}
	require.True(t, hasGC, "second joiner's OutJoined.Events must also include GameCreated")
}

// --- Host visibility -----------------------------------------------------

func TestRoom_HostChangedBroadcastOnFirstJoin(t *testing.T) {
	// When the first player joins, the room sets r.host to their pid
	// and must emit a HostChanged event so every observer (including
	// the host themselves) learns who the host is. Without this, only
	// the host's own OutJoined.IsHost flag carries the info, and
	// other players can't render a Host badge.
	_, r := newTestRoom(t)
	subA, ackA := connect(t, r, "Alice")

	// After OutJoined, the host should receive a PlayerJoined
	// broadcast (their own) followed by a HostChanged broadcast.
	// Order matters: PlayerJoined must come first so HostChanged's
	// referenced player is already in the client roster.
	pj := recvType[OutEvent](t, subA)
	_, ok := pj.Event.(game.PlayerJoined)
	require.True(t, ok, "first broadcast after first join must be PlayerJoined")

	hc := recvType[OutEvent](t, subA)
	gotHC, ok := hc.Event.(game.HostChanged)
	require.True(t, ok, "second broadcast must be HostChanged")
	require.Equal(t, ackA.PlayerID, gotHC.PlayerID,
		"HostChanged.PlayerID must match the first joiner's pid")
}

func TestRoom_HostChangedNotReemittedOnSecondJoin(t *testing.T) {
	// HostChanged fires exactly once — when the host slot is
	// assigned, which is "first joiner ever". Subsequent joins
	// must NOT emit HostChanged (the host hasn't changed).
	_, r := newTestRoom(t)
	subA, _ := connect(t, r, "Alice")
	_ = drain(subA, 50*time.Millisecond)

	subB, _ := connect(t, r, "Bob")
	_ = drain(subB, 50*time.Millisecond)

	// Alice should see only Bob's PlayerJoined — no second
	// HostChanged.
	for _, msg := range drain(subA, 50*time.Millisecond) {
		ev, ok := msg.(OutEvent)
		if !ok {
			continue
		}
		_, isHC := ev.Event.(game.HostChanged)
		require.False(t, isHC, "no HostChanged should fire on a non-first join, got %#v", ev.Event)
	}
}

func TestRoom_LateJoinerReplayIncludesHostChanged(t *testing.T) {
	// Bob joins after Alice. Bob's OutJoined.Events replay must
	// include the HostChanged event that fired on Alice's join, so
	// Bob's client can render the Host badge next to Alice without
	// guessing from event order.
	_, r := newTestRoom(t)
	_, ackA := connect(t, r, "Alice")
	_, ackB := connect(t, r, "Bob")

	var gotHC *game.HostChanged
	for i := range ackB.Events {
		if e, ok := ackB.Events[i].(game.HostChanged); ok {
			gotHC = &e
			break
		}
	}
	require.NotNil(t, gotHC, "Bob's prior-events must include HostChanged")
	require.Equal(t, ackA.PlayerID, gotHC.PlayerID, "Host must be Alice")
}

// --- Manager basics -------------------------------------------------------

func TestManager_CreateAndGet(t *testing.T) {
	m := newTestManager(t)
	r, err := m.CreateRoom(Config{Logger: silentLogger()})
	require.NoError(t, err)
	require.NotEmpty(t, r.Code())
	require.Len(t, r.Code(), codeLength)

	got, err := m.Get(r.Code())
	require.NoError(t, err)
	require.Same(t, r, got)

	_, err = m.Get("ZZZZ")
	require.ErrorIs(t, err, ErrRoomNotFound)
}

func TestManager_UniqueCodes(t *testing.T) {
	m := newTestManager(t)
	seen := map[string]bool{}
	for i := 0; i < 20; i++ {
		r, err := m.CreateRoom(Config{Logger: silentLogger()})
		require.NoError(t, err)
		require.False(t, seen[r.Code()], "duplicate code %q", r.Code())
		seen[r.Code()] = true
	}
}

func TestManager_CloseShutsRoomsDown(t *testing.T) {
	m := newTestManager(t)
	r, err := m.CreateRoom(Config{Logger: silentLogger()})
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	require.NoError(t, m.Close(ctx))

	// Submit after Close must fail.
	err = r.submit(context.Background(), inJoin{From: NewSubscriber(), Name: "x"})
	require.Error(t, err)
}

// --- Join, identity, broadcast -------------------------------------------

func TestRoom_FirstJoinerBecomesHost(t *testing.T) {
	_, r := newTestRoom(t)

	subA, ackA := connect(t, r, "Alice")
	require.True(t, ackA.IsHost, "first joiner should be host")
	_ = subA

	subB, ackB := connect(t, r, "Bob")
	require.False(t, ackB.IsHost, "second joiner should not be host")
	_ = subB
}

func TestRoom_PlayerJoinedBroadcastsToOthers(t *testing.T) {
	_, r := newTestRoom(t)

	subA, ackA := connect(t, r, "Alice")

	// Drain the "PlayerJoined for Alice" event that subA itself receives.
	_ = drain(subA, 50*time.Millisecond)

	subB, ackB := connect(t, r, "Bob")

	// subA should see Bob's PlayerJoined.
	msg := recvType[OutEvent](t, subA)
	pj, ok := msg.Event.(game.PlayerJoined)
	require.True(t, ok)
	require.Equal(t, ackB.PlayerID, pj.PlayerID)
	require.Equal(t, "Bob", pj.Name)

	// subB also sees their own PlayerJoined.
	msg = recvType[OutEvent](t, subB)
	pj, ok = msg.Event.(game.PlayerJoined)
	require.True(t, ok)
	require.Equal(t, ackB.PlayerID, pj.PlayerID)

	_ = ackA
}

// TestRoom_JoinAckIncludesPriorRoster guards the symmetry between
// first-time join and rejoin: a late joiner must be able to discover
// who's already in the room from their OutJoined payload, not just
// from live events emitted after them. Without this, the second and
// later joiners would only see players who joined AFTER them.
//
// The leading GameCreated event (emitted by newRoom and stored in
// r.events) is also expected in every joiner's prior-events slice,
// so each client can learn the lobby's MinPlayers / MaxPlayers /
// MafiaCount without hardcoding defaults. TestRoom_FirstJoinerSees
// GameCreated covers that invariant directly; here we just assert
// the count is right.
func TestRoom_JoinAckIncludesPriorRoster(t *testing.T) {
	_, r := newTestRoom(t)

	subA, ackA := connect(t, r, "Alice")
	_ = drain(subA, 50*time.Millisecond)

	// Bob's join ack should carry GameCreated + Alice's PlayerJoined
	// + HostChanged so the new player can reconstruct lobby config,
	// existing roster, AND who the host is. Order in r.events is
	// preserved.
	subB := NewSubscriber()
	require.NoError(t, r.submit(context.Background(), inJoin{From: subB, Name: "Bob"}))
	ackB := recvType[OutJoined](t, subB)

	require.Len(t, ackB.Events, 3,
		"Bob's join ack should include exactly the events that happened before him: GameCreated, Alice's PlayerJoined, then HostChanged")
	_, ok := ackB.Events[0].(game.GameCreated)
	require.True(t, ok, "first prior event must be GameCreated")
	pj, ok := ackB.Events[1].(game.PlayerJoined)
	require.True(t, ok, "second prior event must be a PlayerJoined")
	require.Equal(t, ackA.PlayerID, pj.PlayerID, "should reference Alice")
	require.Equal(t, "Alice", pj.Name)
	hc, ok := ackB.Events[2].(game.HostChanged)
	require.True(t, ok, "third prior event must be HostChanged")
	require.Equal(t, ackA.PlayerID, hc.PlayerID, "host should be Alice")

	// Bob's OWN PlayerJoined should still arrive as a separate
	// broadcast event right after the ack — we did not bundle it
	// into Events to avoid double-delivery.
	msg := recvType[OutEvent](t, subB)
	own, ok := msg.Event.(game.PlayerJoined)
	require.True(t, ok)
	require.Equal(t, "Bob", own.Name)

	// The very first joiner's ack contains exactly the events that
	// existed before them: GameCreated and nothing else (no prior
	// PlayerJoineds).
	require.Len(t, ackA.Events, 1,
		"the first joiner's ack should contain only GameCreated")
	_, ok = ackA.Events[0].(game.GameCreated)
	require.True(t, ok, "first joiner's only prior event must be GameCreated")
}

// --- Rejoin ---------------------------------------------------------------

func TestRoom_RejoinAcceptsCorrectSecret(t *testing.T) {
	_, r := newTestRoom(t)
	subA, ackA := connect(t, r, "Alice")
	_ = drain(subA, 50*time.Millisecond)

	// New connection rejoins as Alice.
	subA2 := NewSubscriber()
	require.NoError(t, r.submit(context.Background(), inRejoin{
		From: subA2, PlayerID: ackA.PlayerID, Secret: ackA.Secret,
	}))

	re := recvType[OutRejoined](t, subA2)
	require.Equal(t, ackA.PlayerID, re.PlayerID)
	require.Equal(t, "Alice", re.Name, "rejoin ack must echo the player's name")
	require.True(t, re.IsHost)
	require.NotEmpty(t, re.Events, "rejoin should replay events")
}

func TestRoom_RejoinRejectsBadSecret(t *testing.T) {
	_, r := newTestRoom(t)
	_, ackA := connect(t, r, "Alice")

	bad := NewSubscriber()
	require.NoError(t, r.submit(context.Background(), inRejoin{
		From: bad, PlayerID: ackA.PlayerID, Secret: "definitely-wrong",
	}))

	errMsg := recvType[OutError](t, bad)
	require.Equal(t, wire.ErrCodeAuthFailed, errMsg.Code)
}

func TestRoom_RejoinEvictsOldSubscriber(t *testing.T) {
	_, r := newTestRoom(t)
	subA, ackA := connect(t, r, "Alice")
	_ = drain(subA, 50*time.Millisecond)

	subA2 := NewSubscriber()
	require.NoError(t, r.submit(context.Background(), inRejoin{
		From: subA2, PlayerID: ackA.PlayerID, Secret: ackA.Secret,
	}))
	_ = recvType[OutRejoined](t, subA2)

	// Old subscriber's outbound channel should now be closed.
	select {
	case _, ok := <-subA.Outbound():
		require.False(t, ok, "old subscriber's channel should be closed")
	case <-time.After(time.Second):
		t.Fatal("old subscriber's channel was not closed in time")
	}
}

// --- Actor rewriting (auth boundary) -------------------------------------

func TestRoom_CommandsRewriteActorToSender(t *testing.T) {
	_, r := newTestRoom(t)

	// Fill 5 players (default roster size).
	subs := make([]*Subscriber, 5)
	acks := make([]OutJoined, 5)
	for i := range subs {
		subs[i], acks[i] = connect(t, r, string(rune('A'+i)))
	}
	// Drain join broadcasts so the channels are quiet.
	for _, s := range subs {
		_ = drain(s, 50*time.Millisecond)
	}

	// Start the game. subs[0] is the host (first to join in this
	// helper), so this is the legal path; non-host StartGame is
	// rejected elsewhere with ErrNotHost.
	require.NoError(t, r.submit(context.Background(), inCommand{
		From: subs[0], Cmd: game.StartGame{},
	}))

	// Drain GameStarted/RoleAssigned/PhaseChanged events.
	for _, s := range subs {
		_ = drain(s, 100*time.Millisecond)
	}

	// Now: pretend subs[0] sends a NightAction claiming to be subs[1].
	// The room must rewrite Actor to subs[0].PlayerID.
	bogus := game.NightAction{Actor: acks[1].PlayerID, Target: acks[2].PlayerID}
	require.NoError(t, r.submit(context.Background(), inCommand{
		From: subs[0], Cmd: bogus,
	}))

	// Whatever the result, subs[0] (the sender) is what the engine sees.
	// Since subs[0]'s role is random, we may get NightActionRecorded
	// (if subs[0] happens to have a night-acting role) or OutError. In
	// EITHER case, no spoofing happened. We just need a message back
	// to subs[0].
	got := recv(t, subs[0])
	switch v := got.(type) {
	case OutError:
		// Most likely: subs[0] is a villager or invalid target. That's
		// fine — proves the command went through with Actor=subs[0].
		t.Logf("rejected as expected: %s", v.Message)
	case OutEvent:
		nar, ok := v.Event.(game.NightActionRecorded)
		if ok {
			require.Equal(t, acks[0].PlayerID, nar.Actor,
				"Actor must be rewritten to sender's PlayerID")
		}
	}
}

// --- Leave ----------------------------------------------------------------

func TestRoom_LeaveClosesChannelButKeepsPlayer(t *testing.T) {
	_, r := newTestRoom(t)
	subA, ackA := connect(t, r, "Alice")
	_ = drain(subA, 50*time.Millisecond)

	require.NoError(t, r.submit(context.Background(), inLeave{From: subA}))

	// Channel closes.
	select {
	case _, ok := <-subA.Outbound():
		require.False(t, ok)
	case <-time.After(time.Second):
		t.Fatal("channel not closed after leave")
	}

	// Player can rejoin with the same secret.
	subA2 := NewSubscriber()
	require.NoError(t, r.submit(context.Background(), inRejoin{
		From: subA2, PlayerID: ackA.PlayerID, Secret: ackA.Secret,
	}))
	_ = recvType[OutRejoined](t, subA2)
}

// --- Engine / room name agreement ----------------------------------------

// TestRoom_HandleJoinStoresEngineTrimmedName pins the contract that
// the room's playerSlot.name MATCHES whatever the engine stored
// (i.e. the trimmed form, see applyAddPlayer). Without that, a
// rejoin would echo the un-trimmed input back in OutRejoined.Name
// while everyone else's roster shows the trimmed form, and the
// rejoining player would see a different version of their own
// name than the room sees.
func TestRoom_HandleJoinStoresEngineTrimmedName(t *testing.T) {
	_, r := newTestRoom(t)
	sub := NewSubscriber()
	require.NoError(t, r.submit(context.Background(),
		inJoin{From: sub, Name: "  Alice  "}))

	ack := recvType[OutJoined](t, sub)
	require.Equal(t, "Alice", ack.Name,
		"OutJoined.Name must be the engine-trimmed form")

	// Drive a rejoin to verify the room's stored slot.name is also
	// the trimmed form. We can't peek into r.players directly from
	// the test goroutine (it's run-loop-private), but OutRejoined
	// echoes slot.name back to us, so the rejoin path observes it.
	rsub := NewSubscriber()
	require.NoError(t, r.submit(context.Background(),
		inRejoin{From: rsub, PlayerID: ack.PlayerID, Secret: ack.Secret}))

	rack := recvType[OutRejoined](t, rsub)
	require.Equal(t, "Alice", rack.Name,
		"OutRejoined.Name must be the trimmed form too (engine and "+
			"room must agree on a player's canonical name)")
}

// --- Error code mapping --------------------------------------------------

func TestRoom_ErrorForMapsAllSentinels(t *testing.T) {
	// Hand-enumerated so adding a sentinel without listing it here is
	// a test failure, not a silent fallthrough to ErrCodeInternal.
	// We assert both directions of the mapping: every sentinel
	// produces the expected wire code, and conversely
	// TestErrorCodes_Registry below asserts every wire code has a
	// sentinel.
	cases := []struct {
		err  error
		code wire.ErrorCode
	}{
		// Engine sentinels.
		{game.ErrWrongPhase, wire.ErrCodeWrongPhase},
		{game.ErrUnknownPlayer, wire.ErrCodeUnknownPlayer},
		{game.ErrDuplicatePlayer, wire.ErrCodeDuplicatePlayer},
		{game.ErrDuplicateName, wire.ErrCodeDuplicateName},
		{game.ErrPlayerDead, wire.ErrCodePlayerDead},
		{game.ErrNotYourAction, wire.ErrCodeNotYourAction},
		{game.ErrNotYourTurn, wire.ErrCodeNotYourTurn},
		{game.ErrSelfTarget, wire.ErrCodeSelfTarget},
		{game.ErrRosterMismatch, wire.ErrCodeRosterMismatch},
		{game.ErrLobbyFull, wire.ErrCodeLobbyFull},
		{game.ErrGameEnded, wire.ErrCodeGameEnded},
		{game.ErrNoChange, wire.ErrCodeNoChange},
		{game.ErrAlreadyActed, wire.ErrCodeAlreadyActed},

		// Room / transport sentinels.
		{ErrAuthFailed, wire.ErrCodeAuthFailed},
		{ErrNotJoined, wire.ErrCodeNotJoined},
		{ErrForbidden, wire.ErrCodeForbidden},
		{ErrBadFrame, wire.ErrCodeBadFrame},
		{ErrBadMessage, wire.ErrCodeBadMessage},
		{ErrInternal, wire.ErrCodeInternal},
	}
	for _, tc := range cases {
		t.Run(string(tc.code), func(t *testing.T) {
			got := errorFor(tc.err)
			require.Equal(t, tc.code, got.Code)
		})
	}
}

func TestRoom_ErrorForUnknownErrorFallsBackToInternal(t *testing.T) {
	// Genuinely unknown errors (e.g. an unwrapped fmt.Errorf from
	// some future code path) must not panic or lose the message;
	// they collapse onto ErrCodeInternal and surface the raw text.
	got := errorFor(io.EOF)
	require.Equal(t, wire.ErrCodeInternal, got.Code)
	require.Equal(t, io.EOF.Error(), got.Message)
}

func TestErrorCodes_Registry(t *testing.T) {
	// Whole-package drift guard: every constant in wire.ErrorCodes
	// must have a matching entry in room.sentinelCodes (and thus a
	// known sentinel that produces it). If this fails, someone
	// added a wire.ErrCode* without wiring it up — fix by extending
	// sentinelCodes (and the corresponding sentinel package).
	produced := make(map[wire.ErrorCode]bool, len(sentinelCodes))
	for _, m := range sentinelCodes {
		produced[m.code] = true
	}
	// ErrCodeInternal is the default branch in errorFor and may not
	// have a dedicated sentinel mapped (ErrInternal does in fact
	// map to it, but we don't want this test to depend on that).
	produced[wire.ErrCodeInternal] = true

	for _, code := range wire.ErrorCodes {
		require.Truef(t, produced[code],
			"wire.ErrorCode %q has no sentinel entry in sentinelCodes; "+
				"add a mapping in internal/room/errors.go",
			code)
	}
}

func TestRoom_JoinErrorForRewritesLobbyClosedMessages(t *testing.T) {
	// The Code is the wire contract; that must not change. The
	// Message is what the player sees and SHOULD be friendlier for
	// the three "this room can't accept you" cases that show up
	// during a join handshake.
	cases := []struct {
		name        string
		err         error
		wantCode    wire.ErrorCode
		wantMessage string
	}{
		{
			name:        "wrong_phase becomes a join-friendly message",
			err:         game.ErrWrongPhase,
			wantCode:    wire.ErrCodeWrongPhase,
			wantMessage: "This game is already in progress. Create a new room to play.",
		},
		{
			name:        "lobby_full becomes a join-friendly message",
			err:         game.ErrLobbyFull,
			wantCode:    wire.ErrCodeLobbyFull,
			wantMessage: "This room is full. Create a new room to play.",
		},
		{
			name:        "game_ended becomes a join-friendly message",
			err:         game.ErrGameEnded,
			wantCode:    wire.ErrCodeGameEnded,
			wantMessage: "This game has already ended. Create a new room to play.",
		},
		{
			name:        "duplicate_name becomes a join-friendly message",
			err:         game.ErrDuplicateName,
			wantCode:    wire.ErrCodeDuplicateName,
			wantMessage: "That name is already taken. Pick a different name.",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := joinErrorFor(tc.err)
			require.Equal(t, tc.wantCode, got.Code)
			require.Equal(t, tc.wantMessage, got.Message)
		})
	}
}

func TestRoom_JoinErrorForPassesUnrelatedCodesThrough(t *testing.T) {
	// joinErrorFor must not touch codes it doesn't recognize. If a
	// new engine sentinel ever fires during a join, we'd rather show
	// the raw text than silently swallow it.
	got := joinErrorFor(game.ErrDuplicatePlayer)
	require.Equal(t, wire.ErrCodeDuplicatePlayer, got.Code)
	require.Equal(t, game.ErrDuplicatePlayer.Error(), got.Message)
}

// Slow-subscriber disconnect is tested in room_internal_test.go where
// we can drive appendAndBroadcast directly. External tests have an
// unwinnable race with the room goroutine: by the time the test's
// drain loop starts, the room is still emitting broadcasts and the
// drainer keeps the buffer below capacity forever.

// --- Phase timers --------------------------------------------------------

func TestRoom_NightTurnTimersAutoAdvance(t *testing.T) {
	// Daytime is host-driven, so the only timer the room arms in
	// production is the Night per-turn timer. This test verifies that
	// after StartGame + BeginNight, the room walks the three night
	// turns to a complete Night resolution without the host doing
	// anything — purely by per-turn timeouts.
	//
	// We shrink every night sub-phase to a few milliseconds so the
	// test runs in well under a second. All six sub-phases (opening +
	// narrate + act + ponder + sleep + settle) × 3 roles × 1 night
	// dominate the wall-clock here; tiny durations turn this from a
	// 10s-plus integration into a sub-second unit test.
	//
	// Shrink every night sub-phase to 1ms via the test-only override
	// seam; production leaves SubPhaseDurationOverride nil and reads
	// the Default* constants.
	m := newTestManager(t)
	r, err := m.CreateRoom(Config{
		Logger: silentLogger(),
		SubPhaseDurationOverride: func(game.NightSubPhaseStarted, bool) (time.Duration, bool) {
			return time.Millisecond, true
		},
	})
	require.NoError(t, err)

	subs := make([]*Subscriber, 5)
	for i := range subs {
		subs[i], _ = connect(t, r, string(rune('A'+i)))
	}
	for _, s := range subs {
		_ = drain(s, 20*time.Millisecond)
	}

	// Host (subs[0]) deals roles and begins night.
	require.NoError(t, r.submit(context.Background(), inCommand{
		From: subs[0], Cmd: game.StartGame{},
	}))
	require.NoError(t, r.submit(context.Background(), inCommand{
		From: subs[0], Cmd: game.BeginNight{},
	}))

	// Expect: Lobby → Night (BeginNight) then Night → DayDiscussion
	// (after all three turn timers fire). No further auto-advance.
	wantSeq := []struct {
		from game.Phase
		to   game.Phase
	}{
		{from: game.PhaseLobby, to: game.PhaseNight},
		{from: game.PhaseNight, to: game.PhaseDayDiscussion},
	}
	idx := 0
	deadline := time.After(2 * time.Second)
	for idx < len(wantSeq) {
		select {
		case msg, ok := <-subs[0].Outbound():
			if !ok {
				t.Fatal("subscriber channel closed early")
			}
			ev, isEvent := msg.(OutEvent)
			if !isEvent {
				continue
			}
			pc, isPC := ev.Event.(game.PhaseChanged)
			if !isPC {
				continue
			}
			want := wantSeq[idx]
			if pc.From == want.from && pc.To == want.to {
				idx++
			}
		case <-deadline:
			t.Fatalf("night timer stalled after %d/%d transitions (next want %v→%v)",
				idx, len(wantSeq), wantSeq[idx].from, wantSeq[idx].to)
		}
	}

	// Confirm the room SITS in DayDiscussion (no auto-advance to
	// DayVote). We drain briefly and verify no PhaseChanged out.
	noMore := drain(subs[0], 200*time.Millisecond)
	for _, msg := range noMore {
		ev, ok := msg.(OutEvent)
		if !ok {
			continue
		}
		if _, isPC := ev.Event.(game.PhaseChanged); isPC {
			t.Fatalf("day_discussion auto-advanced; host-driven flow violated: %#v", ev.Event)
		}
	}
}

// --- Lifetime reaping ----------------------------------------------------

// newTestManagerWithSweep is newTestManager with a sub-second sweeper
// cadence so tests can observe reaping without real-time waits.
func newTestManagerWithSweep(t *testing.T, interval time.Duration) *Manager {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	m := NewManager(ctx, silentLogger(), WithSweepInterval(interval))
	t.Cleanup(func() {
		shutdown, c := context.WithTimeout(context.Background(), time.Second)
		defer c()
		_ = m.Close(shutdown)
	})
	return m
}

// waitGone polls the manager's registry until the given code is
// missing, or fails the test after deadline. We poll (rather than
// hook into r.done) because the manager-side reapWhenDone is what
// removes the entry from the map; observing that race-free from
// outside requires polling.
func waitGone(t *testing.T, m *Manager, code string, deadline time.Duration) {
	t.Helper()
	end := time.Now().Add(deadline)
	for time.Now().Before(end) {
		if _, err := m.Get(code); err == ErrRoomNotFound {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("room %q was not reaped within %s", code, deadline)
}

// TestRoom_LifetimeReaperClosesOldRoom verifies the headline case: a
// room that has existed for longer than its MaxLifetime is reaped
// on the next sweep, regardless of whether it ever had subscribers.
func TestRoom_LifetimeReaperClosesOldRoom(t *testing.T) {
	m := newTestManagerWithSweep(t, 20*time.Millisecond)
	r, err := m.CreateRoom(Config{
		Logger:      silentLogger(),
		MaxLifetime: 50 * time.Millisecond,
	})
	require.NoError(t, err)
	waitGone(t, m, r.Code(), time.Second)
}

// TestRoom_LifetimeReaperIgnoresSubscribers verifies that an active
// room — full of connected subscribers — gets reaped just the same
// once it crosses MaxLifetime. This is the intentional tradeoff:
// predictable resource bounds over "wait for everyone to leave".
func TestRoom_LifetimeReaperIgnoresSubscribers(t *testing.T) {
	m := newTestManagerWithSweep(t, 20*time.Millisecond)
	r, err := m.CreateRoom(Config{
		Logger:      silentLogger(),
		MaxLifetime: 80 * time.Millisecond,
	})
	require.NoError(t, err)

	// Connect a player so the room is "active" by any reasonable
	// definition. Lifetime cap should still reap it.
	subA, _ := connect(t, r, "Alice")
	_ = drain(subA, 30*time.Millisecond)

	waitGone(t, m, r.Code(), time.Second)
}

// TestRoom_LifetimeReaperRespectsCap verifies the inverse: a room
// younger than its cap is NOT reaped, even with no subscribers.
func TestRoom_LifetimeReaperRespectsCap(t *testing.T) {
	m := newTestManagerWithSweep(t, 20*time.Millisecond)
	r, err := m.CreateRoom(Config{
		Logger:      silentLogger(),
		MaxLifetime: time.Hour, // far longer than any test
	})
	require.NoError(t, err)

	// Run several sweeps; the room must stay registered.
	time.Sleep(200 * time.Millisecond)
	_, err = m.Get(r.Code())
	require.NoError(t, err, "room younger than MaxLifetime must not be reaped")
}

// TestRoom_LifetimeReaperDisabledByZero verifies that MaxLifetime<=0
// disables reaping entirely.
func TestRoom_LifetimeReaperDisabledByZero(t *testing.T) {
	m := newTestManagerWithSweep(t, 20*time.Millisecond)
	r, err := m.CreateRoom(Config{
		Logger:      silentLogger(),
		MaxLifetime: -1,
	})
	require.NoError(t, err)

	time.Sleep(200 * time.Millisecond)
	_, err = m.Get(r.Code())
	require.NoError(t, err, "MaxLifetime<=0 must disable reaping")
}

// --- Projection: RoleAssigned is private --------------------------------

func TestRoom_RoleAssignedOnlyVisibleToSubject(t *testing.T) {
	_, r := newTestRoom(t)

	subs := make([]*Subscriber, 5)
	acks := make([]OutJoined, 5)
	for i := range subs {
		subs[i], acks[i] = connect(t, r, string(rune('A'+i)))
	}
	for _, s := range subs {
		_ = drain(s, 50*time.Millisecond)
	}

	require.NoError(t, r.submit(context.Background(), inCommand{
		From: subs[0], Cmd: game.StartGame{},
	}))

	// Each subscriber should receive at most one RoleAssigned, addressed
	// to themselves. They should NEVER receive someone else's.
	for i, sub := range subs {
		myID := acks[i].PlayerID
		msgs := drain(sub, 200*time.Millisecond)
		seenOwn := false
		for _, m := range msgs {
			ev, ok := m.(OutEvent)
			if !ok {
				continue
			}
			ra, ok := ev.Event.(game.RoleAssigned)
			if !ok {
				continue
			}
			require.Equal(t, myID, ra.PlayerID,
				"subscriber %s saw a RoleAssigned for someone else", myID)
			require.False(t, seenOwn, "duplicate RoleAssigned for %s", myID)
			seenOwn = true
		}
		require.True(t, seenOwn, "subscriber %s never received their own RoleAssigned", myID)
	}
}

// --- Room capacity cap ----------------------------------------------------

func TestManager_CreateRoom_AtCapacity(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	m := NewManager(ctx, silentLogger(), WithMaxRooms(2))
	t.Cleanup(func() {
		sd, c := context.WithTimeout(context.Background(), time.Second)
		defer c()
		_ = m.Close(sd)
	})

	_, err := m.CreateRoom(Config{Logger: silentLogger()})
	require.NoError(t, err)
	_, err = m.CreateRoom(Config{Logger: silentLogger()})
	require.NoError(t, err)

	// Third room exceeds the cap.
	_, err = m.CreateRoom(Config{Logger: silentLogger()})
	require.ErrorIs(t, err, ErrAtCapacity)
}

// --- Rejected join/rejoin closes the subscriber channel -------------------

func TestRoom_RejectedJoinClosesChannel(t *testing.T) {
	// A failed join (here: duplicate name) must send the error AND
	// close the subscriber's outbound channel, so the transport's
	// write pump unwinds instead of parking on an empty channel.
	_, r := newTestRoom(t)
	_, _ = connect(t, r, "alice")

	sub := NewSubscriber()
	require.NoError(t, r.submit(context.Background(), inJoin{From: sub, Name: "alice"}))

	// First the error frame...
	oe := recvType[OutError](t, sub)
	require.Equal(t, wire.ErrCodeDuplicateName, oe.Code)

	// ...then the channel closes.
	select {
	case _, ok := <-sub.Outbound():
		require.False(t, ok, "channel must be closed after a rejected join")
	case <-time.After(recvTimeout):
		t.Fatal("expected channel close after rejected join")
	}
}

func TestRoom_RejectedRejoinClosesChannel(t *testing.T) {
	_, r := newTestRoom(t)

	sub := NewSubscriber()
	require.NoError(t, r.submit(context.Background(),
		inRejoin{From: sub, PlayerID: "p1", Secret: "bogus"}))

	oe := recvType[OutError](t, sub)
	require.Equal(t, wire.ErrCodeAuthFailed, oe.Code)

	select {
	case _, ok := <-sub.Outbound():
		require.False(t, ok, "channel must be closed after a rejected rejoin")
	case <-time.After(recvTimeout):
		t.Fatal("expected channel close after rejected rejoin")
	}
}

// --- Host migration -------------------------------------------------------

func TestRoom_HostMigratesAfterGrace(t *testing.T) {
	// When the host's connection drops and doesn't return within the
	// grace window, the room promotes the oldest connected player and
	// broadcasts HostChanged so the game stays progressable.
	m := newTestManager(t)
	r, err := m.CreateRoom(Config{
		Logger:          silentLogger(),
		HostGracePeriod: 40 * time.Millisecond,
	})
	require.NoError(t, err)

	host, hostAck := connect(t, r, "host")
	p2, p2Ack := connect(t, r, "p2")
	require.True(t, hostAck.IsHost)
	require.False(t, p2Ack.IsHost)

	// Clear the PlayerJoined fan-out so the only thing we wait for is
	// the migration event.
	drain(p2, 50*time.Millisecond)

	// Host drops.
	require.NoError(t, r.submit(context.Background(), inLeave{From: host}))

	// p2 should receive HostChanged promoting itself.
	deadline := time.After(2 * time.Second)
	for {
		select {
		case msg, ok := <-p2.Outbound():
			require.True(t, ok, "p2 channel closed before migration")
			ev, isEvent := msg.(OutEvent)
			if !isEvent {
				continue
			}
			hc, isHC := ev.Event.(game.HostChanged)
			if !isHC {
				continue
			}
			require.Equal(t, p2Ack.PlayerID, hc.PlayerID)
			return
		case <-deadline:
			t.Fatal("expected HostChanged promoting p2 after host grace elapsed")
		}
	}
}

func TestRoom_HostRejoinWithinGraceKeepsHost(t *testing.T) {
	// A host tab refresh (leave then rejoin within the grace window)
	// must NOT trigger migration: the host badge stays put.
	m := newTestManager(t)
	r, err := m.CreateRoom(Config{
		Logger:          silentLogger(),
		HostGracePeriod: 200 * time.Millisecond,
	})
	require.NoError(t, err)

	host, hostAck := connect(t, r, "host")
	p2, _ := connect(t, r, "p2")
	drain(p2, 50*time.Millisecond)

	// Host drops then immediately rejoins (simulated refresh).
	require.NoError(t, r.submit(context.Background(), inLeave{From: host}))
	rejoinSub := NewSubscriber()
	require.NoError(t, r.submit(context.Background(),
		inRejoin{From: rejoinSub, PlayerID: hostAck.PlayerID, Secret: hostAck.Secret}))
	rejoined := recvType[OutRejoined](t, rejoinSub)
	require.True(t, rejoined.IsHost, "host keeps host on a within-grace rejoin")

	// Wait past the grace window; p2 must NOT receive a HostChanged.
	for _, msg := range drain(p2, 300*time.Millisecond) {
		if ev, ok := msg.(OutEvent); ok {
			_, isHC := ev.Event.(game.HostChanged)
			require.False(t, isHC, "no migration should occur on a within-grace rejoin")
		}
	}
}
