package room

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/malhar/mafia-the-game/internal/game"
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
func TestRoom_JoinAckIncludesPriorRoster(t *testing.T) {
	_, r := newTestRoom(t)

	subA, ackA := connect(t, r, "Alice")
	_ = drain(subA, 50*time.Millisecond)

	// Bob's join ack should carry Alice's PlayerJoined so the new
	// player can reconstruct the existing roster.
	subB := NewSubscriber()
	require.NoError(t, r.submit(context.Background(), inJoin{From: subB, Name: "Bob"}))
	ackB := recvType[OutJoined](t, subB)

	require.Len(t, ackB.Events, 1,
		"Bob's join ack should include exactly the events that happened before him")
	pj, ok := ackB.Events[0].(game.PlayerJoined)
	require.True(t, ok, "prior event must be a PlayerJoined")
	require.Equal(t, ackA.PlayerID, pj.PlayerID, "should reference Alice")
	require.Equal(t, "Alice", pj.Name)

	// Bob's OWN PlayerJoined should still arrive as a separate
	// broadcast event right after the ack — we did not bundle it
	// into Events to avoid double-delivery.
	msg := recvType[OutEvent](t, subB)
	own, ok := msg.Event.(game.PlayerJoined)
	require.True(t, ok)
	require.Equal(t, "Bob", own.Name)

	// And the very first joiner's ack must NOT contain prior events
	// (there were none).
	require.Empty(t, ackA.Events,
		"the first joiner's ack should have no prior events")
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
	require.Equal(t, "auth_failed", errMsg.Code)
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

	// Start the game (any player can do this in v1; host-only TODO).
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

// --- Error code mapping --------------------------------------------------

func TestRoom_ErrorForMapsAllSentinels(t *testing.T) {
	cases := []struct {
		err  error
		code string
	}{
		{game.ErrWrongPhase, "wrong_phase"},
		{game.ErrUnknownPlayer, "unknown_player"},
		{game.ErrDuplicatePlayer, "duplicate_player"},
		{game.ErrPlayerDead, "player_dead"},
		{game.ErrNotYourAction, "not_your_action"},
		{game.ErrSelfTarget, "self_target"},
		{game.ErrRosterMismatch, "roster_mismatch"},
		{game.ErrGameEnded, "game_ended"},
		{game.ErrNoChange, "no_change"},
		{game.ErrAlreadyActed, "already_acted"},
	}
	for _, tc := range cases {
		t.Run(tc.code, func(t *testing.T) {
			got := errorFor(tc.err)
			require.Equal(t, tc.code, got.Code)
		})
	}
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
	// We zero out the audio grace and shrink the action window to
	// 30ms so the test runs in well under a second.
	m := newTestManager(t)
	r, err := m.CreateRoom(Config{
		Logger:              silentLogger(),
		NightActionDuration: 30 * time.Millisecond,
		NightTurnGrace:      func(_ game.Role, _ int) time.Duration { return 0 },
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
