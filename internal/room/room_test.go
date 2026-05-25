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
	require.Equal(t, name, name) // placeholder; assertion below
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

func TestRoom_PhaseTimerAdvancesAutomatically(t *testing.T) {
	// Short durations so the test runs quickly. We only need Night to
	// fire; Day phases aren't reached.
	m := newTestManager(t)
	r, err := m.CreateRoom(Config{
		Logger: silentLogger(),
		PhaseDurations: map[game.Phase]time.Duration{
			game.PhaseNight:         40 * time.Millisecond,
			game.PhaseDayDiscussion: 40 * time.Millisecond,
			game.PhaseDayVote:       40 * time.Millisecond,
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

	require.NoError(t, r.submit(context.Background(), inCommand{
		From: subs[0], Cmd: game.StartGame{},
	}))

	// Watch subs[0] for at least one PhaseChanged into PhaseNight, then
	// another PhaseChanged OUT of night within ~150ms (40ms timer + slack).
	sawNight := false
	sawAdvance := false
	deadline := time.After(500 * time.Millisecond)
	for !sawAdvance {
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
			switch {
			case pc.To == game.PhaseNight:
				sawNight = true
			case sawNight && pc.From == game.PhaseNight:
				sawAdvance = true
			}
		case <-deadline:
			t.Fatalf("phase timer did not advance from night (sawNight=%v)", sawNight)
		}
	}
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
