package room

import (
	"context"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/96malhar/mafia-the-game/internal/game"
	"github.com/96malhar/mafia-the-game/internal/wire"
)

// These run under testing/synctest's per-bubble fake clock: the night's
// real 7s/60s timers fire instantly relative to the wall clock (time
// only advances when every goroutine is durably blocked). So the night
// runs on the REAL production Default* durations — no shrinking seam —
// yet finishes in ~zero real time, and we can assert on EXACT deadlines
// and read final engine state directly after synctest.Wait().
//
// The room is built directly via newRoom (no Manager) so the Manager's
// lifetime-sweeper ticker isn't a goroutine in the bubble.

// TestRoomSynctest_NightAutoAdvancesWithRealDurations verifies that
// after StartGame + BeginNight the room walks all three night turns to a
// complete Night resolution with the host doing nothing — purely by
// per-turn timeouts on the real durations — and then SITS in
// DayDiscussion (daytime is host-driven, so no timer is armed and the
// phase must not auto-advance to DayVote).
func TestRoomSynctest_NightAutoAdvancesWithRealDurations(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		// No duration seam: real opening/narrate/act/ponder/sleep/settle
		// durations apply.
		r, err := newRoom(context.Background(), "SYNC", Config{Logger: silentLogger()})
		require.NoError(t, err)
		go r.Run()
		t.Cleanup(func() {
			// The bubble won't be considered settled until Run exits;
			// Close cancels the room ctx and waits for that.
			_ = r.Close(context.Background())
		})

		subs := make([]*Subscriber, 5)
		for i := range subs {
			subs[i], _ = connect(t, r, string(rune('A'+i)))
		}
		// Clear lobby join chatter so only the host's stream matters
		// below. drain's deadline is fake time, so this is free.
		for _, s := range subs {
			_ = drain(s, time.Second)
		}

		require.NoError(t, r.submit(context.Background(), inCommand{
			From: subs[0], Cmd: game.StartGame{},
		}))
		require.NoError(t, r.submit(context.Background(), inCommand{
			From: subs[0], Cmd: game.BeginNight{},
		}))

		// With no actions submitted, every sub-phase times out at its
		// real default and the night walks itself to DayDiscussion. The
		// 30-minute budget is FAKE time and costs no real wall-clock —
		// it just has to exceed the night's total scripted duration.
		wantSeq := []struct{ from, to game.Phase }{
			{game.PhaseLobby, game.PhaseNight},
			{game.PhaseNight, game.PhaseDayDiscussion},
		}
		idx := 0
		deadline := time.After(30 * time.Minute)
		for idx < len(wantSeq) {
			select {
			case msg, ok := <-subs[0].Outbound():
				require.True(t, ok, "subscriber channel closed early")
				ev, isEvent := msg.(OutEvent)
				if !isEvent {
					continue
				}
				pc, isPC := ev.Event.(game.PhaseChanged)
				if !isPC {
					continue
				}
				if pc.From == wantSeq[idx].from && pc.To == wantSeq[idx].to {
					idx++
				}
			case <-deadline:
				t.Fatalf("night stalled after %d/%d transitions (fake-clock budget exhausted)",
					idx, len(wantSeq))
			}
		}

		// Block until the room goroutine is durably blocked. If a day
		// timer were (incorrectly) armed, the fake clock would advance
		// while we're parked here, fire it, and emit a PhaseChanged out
		// of DayDiscussion. We already consumed the Night->DayDiscussion
		// transition above, so ANY further PhaseChanged now means the day
		// wrongly auto-advanced. Asserting on the broadcast stream keeps
		// the test on the room's public surface (no engine-state peek).
		synctest.Wait()
		for _, msg := range drain(subs[0], time.Second) {
			ev, ok := msg.(OutEvent)
			if !ok {
				continue
			}
			if pc, ok := ev.Event.(game.PhaseChanged); ok {
				t.Fatalf("day phase auto-advanced past DayDiscussion: %v -> %v", pc.From, pc.To)
			}
		}
	})
}

// TestRoomSynctest_ConsortBlocksDoctor is an INTEGRATION test for the
// room's timer wiring on a consort-block night. The block SEMANTICS — the
// turn goes phantom, a private Blocked notice is sent, the save is
// suppressed and the victim dies — are unit-tested at the engine level in
// internal/game/consort_test.go; this test asserts only what the LIVE
// room adds on top, on the REAL production durations (synctest's fake
// clock fires them instantly, so the deadlines are exact, not toleranced):
//
//   - an unblocked actor's act window is stamped with the EXACT
//     DefaultActionDuration deadline;
//   - a blocked actor's turn is broadcast as phantom and its ponder
//     deadline lands in the randomized [min, max] phantom range — never
//     the 60s act window; and
//   - a client that bypasses the hidden picker is rejected at the wire
//     boundary with ErrCodeNotYourTurn.
func TestRoomSynctest_ConsortBlocksDoctor(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r, err := newRoom(context.Background(), "SYNC", Config{
			Logger:     silentLogger(),
			MafiaCount: 1,
		})
		require.NoError(t, err)
		go r.Run()
		t.Cleanup(func() { _ = r.Close(context.Background()) })

		names := []string{"Alice", "Bob", "Cara", "Dan", "Eve", "Finn"}
		subs := make([]*Subscriber, len(names))
		acks := make([]OutJoined, len(names))
		for i, n := range names {
			subs[i], acks[i] = connect(t, r, n)
		}
		require.NoError(t, r.submit(context.Background(), inCommand{
			From: subs[0], Cmd: game.SetConsort{Enabled: true},
		}))
		for _, s := range subs {
			_ = drain(s, time.Second)
		}
		require.NoError(t, r.submit(context.Background(), inCommand{
			From: subs[0], Cmd: game.StartGame{},
		}))

		cast := discoverNightRoles(t, subs, acks)
		require.Len(t, cast.villagers, 2)
		mafiaSub := cast.subByRole[game.RoleMafia]
		consortSub := cast.subByRole[game.RoleConsort]
		detSub := cast.subByRole[game.RoleDetective]
		doctorSub := cast.subByRole[game.RoleDoctor]
		require.NotNil(t, consortSub)
		require.NotNil(t, doctorSub)
		doctorID := cast.idByRole[game.RoleDoctor]
		mafiaID := cast.idByRole[game.RoleMafia]
		watcher := cast.villagers[0]
		victimID := cast.villagerIDs[1]

		require.NoError(t, r.submit(context.Background(), inCommand{
			From: subs[0], Cmd: game.BeginNight{},
		}))

		// Mafia turn: exact full action window (fake clock => no slop).
		mafiaAct, _ := awaitActWindow(t, watcher)
		require.Equal(t, game.RoleMafia, mafiaAct.Role)
		require.Equal(t, DefaultActionDuration.Milliseconds(),
			mafiaAct.Deadline-time.Now().UnixMilli(),
			"unblocked actor gets the exact full act window")
		submitNightAction(t, r, mafiaSub, victimID)

		// Consort turn: block the doctor.
		consortAct, _ := awaitActWindow(t, watcher)
		require.Equal(t, game.RoleConsort, consortAct.Role)
		submitNightAction(t, r, consortSub, doctorID)

		// Detective turn: submit to advance instantly.
		detAct, _ := awaitActWindow(t, watcher)
		require.Equal(t, game.RoleDetective, detAct.Role)
		submitNightAction(t, r, detSub, mafiaID)

		// Doctor turn: blocked => NO act window. The turn is a phantom
		// cannot-act ponder, sized by the randomized phantom range — never
		// the 60s act window.
		docNarrate, _ := awaitNightSub(t, watcher, game.RoleDoctor, game.NightSubNarrate)
		require.True(t, docNarrate.Phantom, "a blocked doctor's turn is phantom (no act window)")
		docPonder, obs := awaitNightSub(t, watcher, game.RoleDoctor, game.NightSubPonder)
		remaining := docPonder.Deadline - obs.UnixMilli()
		require.GreaterOrEqual(t, remaining, DefaultPhantomPonderMin.Milliseconds(),
			"a blocked doctor's ponder uses the randomized phantom beat")
		require.LessOrEqual(t, remaining, DefaultPhantomPonderMax.Milliseconds(),
			"a blocked doctor's ponder uses the randomized phantom beat")

		// Room-unique: a client that bypasses the hidden picker is rejected
		// at the WIRE boundary (the engine's ErrNotYourTurn mapped to its
		// wire code). There's no act window to submit into. (That the block
		// itself produces no save / kills the victim is covered by
		// consort_test.go; we don't re-assert resolution here.)
		submitNightAction(t, r, doctorSub, victimID)
		require.Equal(t, wire.ErrCodeNotYourTurn, awaitError(t, doctorSub, time.Minute).Code)
	})
}

// TestRoomSynctest_DeadSpectatorReceivesNightActionFeed is the end-to-end
// proof of the dead-spectator feature: a villager killed on night 1 is, on
// night 2, delivered the graveyard-only SpectatorNightAction over the real
// room → projection → broadcast path — while a LIVING villager never
// receives it. The engine emission + projection visibility are unit-tested
// in internal/game/spectator_test.go; this asserts the room actually fans
// the event out to the right (dead) subscriber and withholds it from the
// living.
func TestRoomSynctest_DeadSpectatorReceivesNightActionFeed(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r, err := newRoom(context.Background(), "SYNC", Config{
			Logger:     silentLogger(),
			MafiaCount: 1,
		})
		require.NoError(t, err)
		go r.Run()
		t.Cleanup(func() { _ = r.Close(context.Background()) })

		// 6 players → mafia + detective + doctor + 3 villagers, so one
		// villager can die night 1 and the game still has ample living
		// players for night 2 (no premature win).
		names := []string{"Alice", "Bob", "Cara", "Dan", "Eve", "Finn"}
		subs := make([]*Subscriber, len(names))
		acks := make([]OutJoined, len(names))
		for i, n := range names {
			subs[i], acks[i] = connect(t, r, n)
		}
		host := subs[0]
		for _, s := range subs {
			_ = drain(s, time.Second)
		}

		require.NoError(t, r.submit(context.Background(), inCommand{From: host, Cmd: game.StartGame{}}))
		cast := discoverNightRoles(t, subs, acks)
		mafiaSub := cast.subByRole[game.RoleMafia]
		mafiaID := cast.idByRole[game.RoleMafia]
		doctorID := cast.idByRole[game.RoleDoctor]
		require.NotNil(t, mafiaSub)

		// The victim-to-be must NOT be the host (the host drives the
		// day/night transitions, so keep them alive). A second, distinct
		// living villager sequences the act windows on night 2 — and proves
		// the feed is withheld from the living.
		var deadSub, liveWatcher *Subscriber
		var deadID game.PlayerID
		for i, vs := range cast.villagers {
			if vs == host {
				continue
			}
			switch {
			case deadSub == nil:
				deadSub, deadID = vs, cast.villagerIDs[i]
			case liveWatcher == nil:
				liveWatcher = vs
			}
		}
		require.NotNil(t, deadSub, "need a non-host villager to kill")
		require.NotNil(t, liveWatcher, "need a living villager to sequence night 2")

		// --- Night 1: mafia kills the villager; nobody saves. ---
		require.NoError(t, r.submit(context.Background(), inCommand{From: host, Cmd: game.BeginNight{}}))
		awaitActWindow(t, liveWatcher) // mafia act window
		submitNightAction(t, r, mafiaSub, deadID)
		// Detective + doctor time out; the night resolves itself to day.
		awaitPhaseChanged(t, liveWatcher, game.PhaseNight, game.PhaseDayDiscussion)

		// Clear all night-1 chatter (incl. the victim's graveyard
		// RosterRevealed) so only night-2 events remain to assert on.
		synctest.Wait()
		for _, s := range subs {
			_ = drain(s, time.Second)
		}

		// --- Day 1 → Night 2: no lynch, then the next night opens. ---
		require.NoError(t, r.submit(context.Background(), inCommand{From: host, Cmd: game.OpenVoting{}}))
		require.NoError(t, r.submit(context.Background(), inCommand{From: host, Cmd: game.FinalizeVotes{}}))
		require.NoError(t, r.submit(context.Background(), inCommand{From: host, Cmd: game.BeginNight{}}))

		// Night 2: mafia targets the (living) doctor.
		awaitActWindow(t, liveWatcher) // night-2 mafia act window
		submitNightAction(t, r, mafiaSub, doctorID)
		synctest.Wait()

		// The DEAD villager spectates the feed: actor + target with both
		// roles, projected only to the graveyard.
		sa, ok := drainFirstEvent[game.SpectatorNightAction](deadSub, time.Second)
		require.True(t, ok, "the dead villager must receive the spectator night feed")
		require.Equal(t, mafiaID, sa.Actor)
		require.Equal(t, game.RoleMafia, sa.ActorRole)
		require.Equal(t, doctorID, sa.Target)
		require.Equal(t, game.RoleDoctor, sa.TargetRole)

		// A LIVING villager must never receive it — the feed would otherwise
		// leak cross-role night targeting to the table.
		requireNoEvent[game.SpectatorNightAction](t, liveWatcher, time.Second,
			"a living player must never receive the spectator night feed")
	})
}

// TestRoomSynctest_ResetGameReturnsRoomToFreshLobby is the full-stack reset
// test: it plays a 5-player game to a town win (lynch the lone mafioso),
// then the host issues ResetGame and we assert the room-layer behavior that
// the engine tests can't see —
//
//   - the event log is REBASELINED to exactly [GameReset, HostChanged]: the
//     finished game's events are dropped, not carried into the new game; and
//   - every connected subscriber receives the GameReset snapshot so each
//     client drops back to the lobby.
func TestRoomSynctest_ResetGameReturnsRoomToFreshLobby(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx := context.Background()
		r, err := newRoom(ctx, "RSET", Config{Logger: silentLogger(), MafiaCount: 1})
		require.NoError(t, err)
		go r.Run()
		t.Cleanup(func() { _ = r.Close(ctx) })

		// 5 players → 1 mafia + detective + doctor + 2 villagers.
		subs := make([]*Subscriber, 5)
		acks := make([]OutJoined, 5)
		for i := range subs {
			subs[i], acks[i] = connect(t, r, string(rune('A'+i)))
		}
		host := subs[0]
		drainAll := func() {
			for _, s := range subs {
				_ = drain(s, time.Second)
			}
		}
		drainAll()

		// Start and discover the lone mafioso.
		require.NoError(t, r.submit(ctx, inCommand{From: host, Cmd: game.StartGame{}}))
		cast := discoverNightRoles(t, subs, acks)
		mafiaSub := cast.subByRole[game.RoleMafia]
		mafiaID := cast.idByRole[game.RoleMafia]
		require.NotNil(t, mafiaSub)
		require.NotEmpty(t, cast.villagerIDs, "need a town target for the mafia's own vote")

		// --- Night 1: nobody acts; the night auto-resolves to day with
		// everyone still alive (the mafia declines to kill). ---
		require.NoError(t, r.submit(ctx, inCommand{From: host, Cmd: game.BeginNight{}}))
		awaitPhaseChanged(t, host, game.PhaseNight, game.PhaseDayDiscussion)
		drainAll()

		// --- Day 1: the four townsfolk all vote the mafioso; the mafioso
		// votes a townie. Plurality is the mafioso (4 votes), so finalizing
		// lynches them — mafia-aligned living count hits 0 → town wins. ---
		require.NoError(t, r.submit(ctx, inCommand{From: host, Cmd: game.OpenVoting{}}))
		awaitPhaseChanged(t, host, game.PhaseDayDiscussion, game.PhaseDayVote)
		drainAll()
		for _, s := range subs {
			target := mafiaID
			if s == mafiaSub {
				target = cast.villagerIDs[0] // mafia can't decline by voting itself
			}
			require.NoError(t, r.submit(ctx, inCommand{From: s, Cmd: game.DayVote{Target: target}}))
		}
		synctest.Wait() // all votes recorded before we finalize
		require.NoError(t, r.submit(ctx, inCommand{From: host, Cmd: game.FinalizeVotes{}}))
		awaitPhaseChanged(t, host, game.PhaseDayVote, game.PhaseEnded)
		drainAll()

		synctest.Wait()
		require.Greater(t, len(r.events), 2, "a played-out game should have a long event log")

		// --- The host starts a new game in the same room. ---
		require.NoError(t, r.submit(ctx, inCommand{From: host, Cmd: game.ResetGame{}}))
		synctest.Wait()

		// The log is rebaselined to exactly [GameReset, HostChanged]: the
		// finished game's events are gone, and the host is reaffirmed so a
		// post-reset (re)joiner reconstructs the lobby from the log alone.
		require.Len(t, r.events, 2, "reset must replace the log with a fresh baseline")
		reset, ok := r.events[0].(game.GameReset)
		require.True(t, ok, "first baseline event must be GameReset, got %T", r.events[0])
		require.Len(t, reset.Players, 5)
		hc, ok := r.events[1].(game.HostChanged)
		require.True(t, ok, "second baseline event must be HostChanged, got %T", r.events[1])
		require.Equal(t, acks[0].PlayerID, hc.PlayerID, "host must be reaffirmed as the original host")

		// Every connected subscriber saw the GameReset snapshot.
		for i, s := range subs {
			ev, ok := drainFirstEvent[game.GameReset](s, time.Second)
			require.Truef(t, ok, "subscriber %d never received GameReset", i)
			require.Len(t, ev.Players, 5)
		}
	})
}
