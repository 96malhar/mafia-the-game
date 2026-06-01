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
		// while we're parked here, fire it, and move the phase past
		// DayDiscussion — so this also proves the day phase does NOT
		// auto-advance. The direct engine-state read is race-free.
		synctest.Wait()
		require.Equal(t, game.PhaseDayDiscussion, r.g.State().Phase(),
			"the night resolved to the host-driven day phase and sits there")
	})
}

// TestRoomSynctest_ConsortBlocksDoctor drives the full consort-blocks-
// doctor night on the REAL durations. Because the fake clock never
// advances while the test goroutine is active, it asserts on EXACT
// deadlines (rather than the tolerances a wall-clock test would need)
// and reads final game state directly after synctest.Wait() instead of
// inferring it from events. The blocked doctor gets NO act window: his
// turn is phantom (narrate -> ponder), timing-indistinguishable from a
// dead role, with the private Blocked notice arriving after his narrate.
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

		// Private Blocked notice arrives after the doctor's narrate (at
		// the cannot-act ponder).
		blk := awaitEvent[game.Blocked](t, doctorSub, time.Minute)
		require.Equal(t, doctorID, blk.PlayerID)

		// A client that bypasses the hidden picker is rejected: there's no
		// act window to submit into.
		submitNightAction(t, r, doctorSub, victimID)
		require.Equal(t, wire.ErrCodeNotYourTurn, awaitError(t, doctorSub, time.Minute).Code)

		// The shortened window times out and the night resolves: kill lands.
		killed := awaitEvent[game.PlayerKilled](t, watcher, time.Minute)
		require.Equal(t, victimID, killed.PlayerID)

		// With the room parked in the day phase, read final state directly.
		synctest.Wait()
		_, saved := batchHasEvent[game.PlayerSaved](drain(doctorSub, time.Second))
		require.False(t, saved, "a blocked doctor produces no save")
		st := r.g.State()
		require.Equal(t, game.PhaseDayDiscussion, st.Phase())
		for _, p := range st.Players() {
			switch p.ID() {
			case victimID:
				require.False(t, p.Alive(), "the mafia's victim is dead (save was blocked)")
			case doctorID, cast.idByRole[game.RoleConsort]:
				require.True(t, p.Alive())
			}
		}
	})
}
