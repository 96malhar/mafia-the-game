package room

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/96malhar/mafia-the-game/internal/game"
)

// Shared infrastructure for the full-stack room tests (a real Room, real
// Subscribers, real per-sub-phase timers, and commands submitted exactly
// the way the transport layer submits them — inCommand{From: sub}). The
// synctest suite (room_synctest_test.go) drives whole nights through
// these helpers and asserts on the concrete events individual players
// receive, closing the gap the unit/projection tests leave open: that
// the room correctly threads engine night-state (the roleblock flag)
// into its timer sizing.

// nightCast is the discovered roster of a started game. Single-holder
// roles (mafia/consort/detective/doctor) map to the one subscriber and
// player id that hold them; villagers (who receive no private night
// events) are collected separately so a test can pick a "silent"
// watcher to sequence the night off of.
type nightCast struct {
	subByRole   map[game.Role]*Subscriber
	idByRole    map[game.Role]game.PlayerID
	villagers   []*Subscriber
	villagerIDs []game.PlayerID
}

// discoverNightRoles drains each subscriber's StartGame batch and reads
// its private RoleAssigned to build the cast. Must be called after
// StartGame and before BeginNight.
func discoverNightRoles(t *testing.T, subs []*Subscriber, acks []OutJoined) nightCast {
	t.Helper()
	c := nightCast{
		subByRole: map[game.Role]*Subscriber{},
		idByRole:  map[game.Role]game.PlayerID{},
	}
	for i, s := range subs {
		role, ok := roleFromBatch(drain(s, 200*time.Millisecond))
		require.Truef(t, ok, "subscriber %d never received its private RoleAssigned", i)
		if role == game.RoleVillager {
			c.villagers = append(c.villagers, s)
			c.villagerIDs = append(c.villagerIDs, acks[i].PlayerID)
			continue
		}
		require.NotContainsf(t, c.subByRole, role,
			"two holders of single-holder role %s — unexpected roster", role)
		c.subByRole[role] = s
		c.idByRole[role] = acks[i].PlayerID
	}
	return c
}

// roleFromBatch finds the (single) RoleAssigned in a drained batch.
func roleFromBatch(msgs []Outbound) (game.Role, bool) {
	for _, m := range msgs {
		if ev, ok := m.(OutEvent); ok {
			if ra, ok := ev.Event.(game.RoleAssigned); ok {
				return ra.Role, true
			}
		}
	}
	return "", false
}

// submitNightAction submits a NightAction from `from` at `target`. The
// room rewrites the actor to the sender, so only the target is given.
func submitNightAction(t *testing.T, r *Room, from *Subscriber, target game.PlayerID) {
	t.Helper()
	require.NoError(t, r.submit(context.Background(), inCommand{
		From: from, Cmd: game.NightAction{Target: target},
	}))
}

// awaitActWindow reads watcher's stream until the next act window opens
// (a NightSubPhaseStarted with Sub == NightSubAct), returning that event
// and the wall-clock instant it was observed (for deadline math).
// Intervening messages are discarded, so the watcher MUST be a player
// that receives no private night events (a villager).
func awaitActWindow(t *testing.T, watcher *Subscriber) (game.NightSubPhaseStarted, time.Time) {
	t.Helper()
	deadline := time.After(15 * time.Second)
	for {
		select {
		case msg, ok := <-watcher.Outbound():
			require.True(t, ok, "watcher channel closed before an act window opened")
			ev, isEv := msg.(OutEvent)
			if !isEv {
				continue
			}
			if ns, ok := ev.Event.(game.NightSubPhaseStarted); ok && ns.Sub == game.NightSubAct {
				return ns, time.Now()
			}
		case <-deadline:
			t.Fatal("timed out waiting for an act window to open")
		}
	}
}

// awaitNightSub reads watcher's stream until a NightSubPhaseStarted with
// the given role and sub arrives, returning that event and the wall-clock
// instant it was observed (for deadline math). Used to assert on phantom
// turns, which (unlike awaitActWindow) never open an act window. The
// watcher MUST be a player that receives no private night events.
func awaitNightSub(t *testing.T, watcher *Subscriber, role game.Role, sub game.NightSubPhase) (game.NightSubPhaseStarted, time.Time) {
	t.Helper()
	deadline := time.After(15 * time.Second)
	for {
		select {
		case msg, ok := <-watcher.Outbound():
			require.Truef(t, ok, "watcher channel closed before %s %s", role, sub)
			ev, isEv := msg.(OutEvent)
			if !isEv {
				continue
			}
			if ns, ok := ev.Event.(game.NightSubPhaseStarted); ok && ns.Role == role && ns.Sub == sub {
				return ns, time.Now()
			}
		case <-deadline:
			t.Fatalf("timed out waiting for %s %s", role, sub)
		}
	}
}

// awaitPhaseChanged reads watcher's stream until a PhaseChanged{from→to}
// arrives, discarding intervening messages. Lets a test sequence past a
// whole self-advancing night without spelling out every sub-phase. The
// watcher MUST be a player that receives no private night events.
func awaitPhaseChanged(t *testing.T, watcher *Subscriber, from, to game.Phase) {
	t.Helper()
	// Fake-clock budget: a full self-advancing night (two 60s act-window
	// timeouts plus every beat) far exceeds 30s of virtual time, so use a
	// generous budget — it costs no real wall-clock under synctest.
	deadline := time.After(30 * time.Minute)
	for {
		select {
		case msg, ok := <-watcher.Outbound():
			require.Truef(t, ok, "watcher channel closed before PhaseChanged %s→%s", from, to)
			if ev, isEv := msg.(OutEvent); isEv {
				if pc, ok := ev.Event.(game.PhaseChanged); ok && pc.From == from && pc.To == to {
					return
				}
			}
		case <-deadline:
			t.Fatalf("timed out waiting for PhaseChanged %s→%s", from, to)
		}
	}
}

// awaitError reads s until an OutError arrives, returning it. Buffered
// OutEvents are discarded (the caller has already sequenced past them
// via the watcher).
func awaitError(t *testing.T, s *Subscriber, within time.Duration) OutError {
	t.Helper()
	deadline := time.After(within)
	for {
		select {
		case msg, ok := <-s.Outbound():
			require.True(t, ok, "channel closed while awaiting OutError")
			if oe, isErr := msg.(OutError); isErr {
				return oe
			}
		case <-deadline:
			t.Fatal("timed out awaiting OutError")
		}
	}
}
