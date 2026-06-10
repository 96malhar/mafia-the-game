package room

import (
	"context"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/96malhar/mafia-the-game/internal/game"
)

// These tests cover the in-memory panic-recovery supervision in recovery.go:
// a panic under the run loop must not crash the process, the room must
// rebuild a consistent engine + log from its command journal, the night must
// keep advancing afterward, and a deterministic re-panic must eventually
// close the one room instead of spinning.
//
// They run white-box (package room) and drive panics through the inTestHook
// seam, which carries a closure to the room goroutine via the inbox so it is
// race-free under -race.

// onLoop runs fn on the room goroutine and waits for it to finish. It lets a
// test read run-loop-only state (r.g, r.events, r.journal) or run engine
// queries race-free. fn must NOT call t.FailNow/require (that would Goexit the
// room goroutine) — capture into variables and assert on the test goroutine.
func onLoop(t *testing.T, r *Room, fn func(*Room)) {
	t.Helper()
	done := make(chan struct{})
	require.NoError(t, r.submit(context.Background(), inTestHook{fn: func(rr *Room) {
		defer close(done)
		fn(rr)
	}}))
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("onLoop hook did not run within deadline")
	}
}

// inducePanic submits a hook that panics on the room goroutine, exercising the
// recover path without a real engine bug.
func inducePanic(t *testing.T, r *Room) {
	t.Helper()
	require.NoError(t, r.submit(context.Background(), inTestHook{fn: func(*Room) {
		panic("induced panic for recovery test")
	}}))
}

// TestRoomRecovery_ReplayReproducesLog is the core invariant: replaying the
// command journal reproduces the live (engine, event-log) pair exactly. This
// is what makes restart-from-log correct — and incidentally proves the engine
// replays deterministically (same commands + seed → byte-identical events).
func TestRoomRecovery_ReplayReproducesLog(t *testing.T) {
	r, err := newRoom(context.Background(), "RPLY", Config{Logger: silentLogger()})
	require.NoError(t, err)
	go r.Run()
	t.Cleanup(func() { _ = r.Close(context.Background()) })

	subs := make([]*Subscriber, 5)
	acks := make([]OutJoined, 5)
	for i := range subs {
		subs[i], acks[i] = connect(t, r, string(rune('A'+i)))
	}
	// Drive into the night so the journal spans CreateGame, joins, host
	// injection, StartGame, BeginNight, and a stamped NightSubPhaseStarted.
	require.NoError(t, r.submit(context.Background(), inCommand{From: subs[0], Cmd: game.StartGame{}}))
	require.NoError(t, r.submit(context.Background(), inCommand{From: subs[0], Cmd: game.BeginNight{}}))

	var live, rebuilt []game.Event
	var liveProj, rebuiltProj []game.Event
	var replayErr error
	var journalLen int
	onLoop(t, r, func(rr *Room) {
		journalLen = len(rr.journal)
		live = append([]game.Event(nil), rr.events...)
		g2, ev2, err := rr.replayJournal()
		replayErr = err
		if err != nil {
			return
		}
		rebuilt = ev2
		// Projecting through both the live and the rebuilt (engine, log)
		// pairs must agree — this checks r.g.State() was reproduced too, not
		// just the raw log.
		liveProj = game.Project(acks[0].PlayerID, rr.events, rr.g.State())
		rebuiltProj = game.Project(acks[0].PlayerID, ev2, g2.State())
	})

	require.NoError(t, replayErr)
	require.Greater(t, journalLen, 7, "journal should span create + 5 joins + start + begin night")
	require.NotEmpty(t, live)
	require.Equal(t, live, rebuilt, "replayed log must equal the live log exactly (incl. stamped deadlines)")
	require.Equal(t, liveProj, rebuiltProj, "a projection over the rebuilt state must match the live one")

	// The night carries a stamped deadline; confirm the rebuild preserved it
	// (a regression where rebuild re-stamped would surface here).
	var sawStampedDeadline bool
	for _, e := range rebuilt {
		if sp, ok := e.(game.NightSubPhaseStarted); ok && sp.Deadline != 0 {
			sawStampedDeadline = true
		}
	}
	require.True(t, sawStampedDeadline, "rebuilt log must retain a stamped night deadline")
}

// TestRoomRecovery_SurvivesPanicInLobby proves the process survives a panic
// (the room goroutine keeps running) and that the rebuilt state is intact: a
// player can rejoin afterward and gets back exactly what they would have seen
// before. The lobby has no auto-advance timer, so the comparison is
// deterministic.
func TestRoomRecovery_SurvivesPanicInLobby(t *testing.T) {
	r, err := newRoom(context.Background(), "PANIC", Config{Logger: silentLogger()})
	require.NoError(t, err)
	go r.Run()
	t.Cleanup(func() { _ = r.Close(context.Background()) })

	subs := make([]*Subscriber, 3)
	acks := make([]OutJoined, 3)
	for i := range subs {
		subs[i], acks[i] = connect(t, r, string(rune('A'+i)))
	}

	// Snapshot what player 0 would see right now.
	var preProj []game.Event
	onLoop(t, r, func(rr *Room) {
		preProj = game.Project(acks[0].PlayerID, rr.events, rr.g.State())
	})
	require.NotEmpty(t, preProj)

	inducePanic(t, r)

	// The room must still be alive: a fresh rejoin of player 0 succeeds and
	// replays the same projected log (resync detached the old subscribers).
	newSub := NewSubscriber()
	require.NoError(t, r.SubmitRejoin(context.Background(), newSub, acks[0].PlayerID, acks[0].Secret, 0))
	rej := recvType[OutRejoined](t, newSub)
	require.Equal(t, acks[0].PlayerID, rej.PlayerID)
	require.True(t, rej.IsHost, "player 0 was the host; the rebuild must preserve that")
	require.Equal(t, preProj, rej.Events, "rejoin after recovery must reproduce the pre-panic projection")

	// And the room still accepts work: a joinability probe is answered.
	st, err := r.JoinStatus(context.Background())
	require.NoError(t, err)
	require.True(t, st.Joinable, "lobby with 3/20 players is still joinable after recovery")
}

// waitForPhaseChange blocks until sub receives an OutEvent carrying a
// PhaseChanged{from→to}. Used under synctest, where the deadline is fake time
// and costs no real wall-clock; it just has to exceed the scripted duration.
func waitForPhaseChange(t *testing.T, sub *Subscriber, from, to game.Phase) {
	t.Helper()
	deadline := time.After(30 * time.Minute)
	for {
		select {
		case msg, ok := <-sub.Outbound():
			if !ok {
				t.Fatalf("subscriber channel closed before PhaseChanged %v->%v", from, to)
			}
			ev, isEvent := msg.(OutEvent)
			if !isEvent {
				continue
			}
			if pc, ok := ev.Event.(game.PhaseChanged); ok && pc.From == from && pc.To == to {
				return
			}
		case <-deadline:
			t.Fatalf("timed out waiting for PhaseChanged %v->%v", from, to)
		}
	}
}

// TestRoomRecovery_KeepsAdvancingAfterMidNightPanic proves the night timer is
// re-armed on rebuild: a panic mid-night must not freeze the game. Under
// synctest the real night durations fire on the fake clock, so we can watch
// the night resolve to DayDiscussion AFTER the panic.
func TestRoomRecovery_KeepsAdvancingAfterMidNightPanic(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r, err := newRoom(context.Background(), "NIGHT", Config{Logger: silentLogger()})
		require.NoError(t, err)
		go r.Run()
		t.Cleanup(func() { _ = r.Close(context.Background()) })

		subs := make([]*Subscriber, 5)
		acks := make([]OutJoined, 5)
		for i := range subs {
			subs[i], acks[i] = connect(t, r, string(rune('A'+i)))
		}
		require.NoError(t, r.submit(context.Background(), inCommand{From: subs[0], Cmd: game.StartGame{}}))
		require.NoError(t, r.submit(context.Background(), inCommand{From: subs[0], Cmd: game.BeginNight{}}))

		// Wait until we're inside the night, then panic.
		waitForPhaseChange(t, subs[0], game.PhaseLobby, game.PhaseNight)
		inducePanic(t, r)
		synctest.Wait() // let the recovery complete and the timer re-arm

		// Old subscribers were detached by resync; rejoin the host to watch
		// the rest of the night on a fresh stream.
		host := NewSubscriber()
		require.NoError(t, r.SubmitRejoin(context.Background(), host, acks[0].PlayerID, acks[0].Secret, 0))
		_ = recvType[OutRejoined](t, host)

		// The night must still auto-advance to DayDiscussion — proving the
		// timer was re-armed by the rebuild rather than left dead.
		waitForPhaseChange(t, host, game.PhaseNight, game.PhaseDayDiscussion)
	})
}

// TestRoomRecovery_CrashLoopGuardClosesRoom proves the crash-loop guard: more
// than maxRecoveriesPerWindow panics in the window closes the one room (rather
// than spinning forever), isolating the blast radius.
func TestRoomRecovery_CrashLoopGuardClosesRoom(t *testing.T) {
	r, err := newRoom(context.Background(), "LOOP", Config{Logger: silentLogger()})
	require.NoError(t, err)
	go r.Run()
	t.Cleanup(func() { _ = r.Close(context.Background()) })

	connect(t, r, "A")

	// Trip the budget: maxRecoveriesPerWindow recoveries are tolerated; the
	// next one closes the room. Submits may start failing once it cancels, so
	// ignore submit errors past that point.
	for range maxRecoveriesPerWindow + 1 {
		if err := r.submit(context.Background(), inTestHook{fn: func(*Room) {
			panic("crash-loop induced panic")
		}}); err != nil {
			break // room already closed
		}
	}

	select {
	case <-r.done:
		// Room closed itself, as required.
	case <-time.After(2 * time.Second):
		t.Fatal("crash-loop guard did not close the room")
	}

	// A submit after close is rejected.
	require.ErrorIs(t, r.submit(context.Background(), inLeave{From: NewSubscriber()}), ErrRoomClosed)
}
