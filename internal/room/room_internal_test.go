package room

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/96malhar/mafia-the-game/internal/game"
)

// TestConfig_SubPhaseDuration pins the per-sub-phase duration logic.
// Durations resolve through c.subPhaseDuration from the Default*
// constants (the single source of wall-clock truth) unless the
// SubPhaseDurationOverride seam shadows them. This test verifies both
// the defaults and the override path.
func TestConfig_SubPhaseDuration(t *testing.T) {
	c := Config{}
	c.applyDefaults()

	sub := func(s game.NightSubPhase, role game.Role, day int, phantom bool) game.NightSubPhaseStarted {
		return game.NightSubPhaseStarted{Sub: s, Role: role, Day: day, Phantom: phantom}
	}

	t.Run("defaults route to package-level constants", func(t *testing.T) {
		// Opening: universal, day-independent.
		require.Equal(t, DefaultOpeningDuration,
			c.subPhaseDuration(sub(game.NightSubOpening, "", 0, false), false),
			"opening default")
		require.Equal(t, DefaultOpeningDuration,
			c.subPhaseDuration(sub(game.NightSubOpening, "", 3, false), false),
			"opening default is day-independent today")

		// Narrate: mafia has a Day-0 variant; everyone else is universal.
		mafiaNarrateDay0 := c.subPhaseDuration(sub(game.NightSubNarrate, game.RoleMafia, 0, false), false)
		mafiaNarrateDay1 := c.subPhaseDuration(sub(game.NightSubNarrate, game.RoleMafia, 1, false), false)
		require.Equal(t, DefaultMafiaNarrateDay0, mafiaNarrateDay0,
			"mafia Day 0 narrate uses the day-0 constant")
		require.Equal(t, DefaultMafiaNarrateDayN, mafiaNarrateDay1,
			"mafia Day N>0 narrate uses the later-night constant")
		require.Greater(t, mafiaNarrateDay0, mafiaNarrateDay1,
			"day-0 mafia narrate must be longer than later-night")

		// Detective and doctor use the universal default for every day.
		for _, r := range []game.Role{game.RoleDetective, game.RoleDoctor} {
			for _, day := range []int{0, 1, 5} {
				require.Equal(t, DefaultNarrateDuration,
					c.subPhaseDuration(sub(game.NightSubNarrate, r, day, false), false),
					"role %q day %d should use DefaultNarrateDuration", r, day)
			}
		}

		// Action: universal.
		require.Equal(t, DefaultActionDuration,
			c.subPhaseDuration(sub(game.NightSubAct, game.RoleMafia, 0, false), false))

		// Settle: universal.
		require.Equal(t, DefaultSettleDuration,
			c.subPhaseDuration(sub(game.NightSubSettle, game.RoleMafia, 0, false), false))

		// Sleep: every shipped role uses the universal default.
		for _, r := range []game.Role{game.RoleMafia, game.RoleDetective, game.RoleDoctor} {
			require.Equal(t, DefaultSleepDuration,
				c.subPhaseDuration(sub(game.NightSubSleep, r, 0, false), false),
				"role %q sleep should use DefaultSleepDuration", r)
		}
	})

	t.Run("ponder default - submitted real role is the short beat", func(t *testing.T) {
		require.Equal(t, DefaultPonderRealSubmit,
			c.subPhaseDuration(sub(game.NightSubPonder, game.RoleMafia, 0, false), true),
			"submitted non-detective: short post-submit beat")
	})

	t.Run("ponder default - detective gets a longer beat", func(t *testing.T) {
		require.Equal(t, DefaultPonderDetectiveSubmit,
			c.subPhaseDuration(sub(game.NightSubPonder, game.RoleDetective, 0, false), true),
			"detective ponder is sized for read-modal pause")
	})

	t.Run("ponder default - timeout matches submit (audio-cadence parity)", func(t *testing.T) {
		require.Equal(t, DefaultPonderRealSubmit,
			c.subPhaseDuration(sub(game.NightSubPonder, game.RoleDoctor, 0, false), false),
			"timeout uses the same beat as submit so observers can't distinguish them")
	})

	t.Run("ponder default - phantom is bounded random", func(t *testing.T) {
		// Repeat the draw so we exercise the randomness: every draw
		// must fall in [lo, hi].
		for range 64 {
			d := c.subPhaseDuration(sub(game.NightSubPonder, game.RoleDetective, 1, true), false)
			require.GreaterOrEqual(t, d, DefaultPhantomPonderMin,
				"phantom ponder must be >= DefaultPhantomPonderMin")
			require.LessOrEqual(t, d, DefaultPhantomPonderMax,
				"phantom ponder must be <= DefaultPhantomPonderMax")
		}
		// A phantom MAFIA turn is unreachable (the game ends as soon as
		// the last mafia dies; see beginNextNightTurn's doc), so we
		// don't assert phantom bounds for mafia.
	})

	t.Run("SubPhaseDurationOverride shadows specific sub-phases", func(t *testing.T) {
		oc := Config{
			SubPhaseDurationOverride: func(e game.NightSubPhaseStarted, _ bool) (time.Duration, bool) {
				switch e.Sub {
				case game.NightSubOpening:
					return 100 * time.Millisecond, true
				case game.NightSubSettle:
					return 50 * time.Millisecond, true
				}
				return 0, false // others fall through to defaults
			},
		}
		oc.applyDefaults()
		require.Equal(t, 100*time.Millisecond,
			oc.subPhaseDuration(sub(game.NightSubOpening, "", 0, false), false),
			"override pins opening")
		require.Equal(t, 50*time.Millisecond,
			oc.subPhaseDuration(sub(game.NightSubSettle, game.RoleDoctor, 0, false), false),
			"override pins settle")
		// A sub-phase the override declines falls back to the default.
		require.Equal(t, DefaultActionDuration,
			oc.subPhaseDuration(sub(game.NightSubAct, game.RoleMafia, 0, false), false),
			"declined sub-phase uses the built-in default")
	})
}

// TestRoom_DisconnectSlowSubscriber verifies that when a subscriber's
// outbound buffer fills, appendAndBroadcast drops it and closes its
// channel. We drive appendAndBroadcast directly so the assertion is
// deterministic — no goroutine race with the test's drainer.
func TestRoom_DisconnectSlowSubscriber(t *testing.T) {
	// Build a minimal room WITHOUT starting its run loop. We need
	// direct access to r.subs and r.events.
	r := &Room{
		code:    "TEST",
		cfg:     Config{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))},
		log:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		inbox:   make(chan inbound, 1),
		g:       game.New(),
		players: make(map[game.PlayerID]*playerSlot),
		subs:    make(map[*Subscriber]struct{}),
	}
	r.ctx, r.cancel = context.WithCancel(context.Background())
	t.Cleanup(r.cancel)
	// Engine needs to be in a state where Project can run. CreateGame
	// gets us into the lobby phase with at least one role configured.
	_, err := r.g.Apply(game.CreateGame{
		GameID: "g", MinPlayers: 5, MaxPlayers: 20, MafiaCount: 1,
	})
	require.NoError(t, err)

	// Slow subscriber: never read its channel.
	slow := NewSubscriber()
	slow.setPlayerID("slow")
	r.subs[slow] = struct{}{}
	r.players["slow"] = &playerSlot{id: "slow", sub: slow}

	// Fast subscriber: a control to confirm broadcasts continue after
	// the slow one is disconnected. We actively drain its channel from
	// a goroutine so the room's sends never see a full buffer for it.
	fast := NewSubscriber()
	fast.setPlayerID("fast")
	r.subs[fast] = struct{}{}
	r.players["fast"] = &playerSlot{id: "fast", sub: fast}

	// A public event so both subscribers receive it.
	makeEvent := func() game.Event {
		return game.PlayerJoined{PlayerID: "x", Name: "x"}
	}

	// Drain whatever is currently buffered for fast. Broadcasts here are
	// synchronous (we call appendAndBroadcast directly), so doing this in
	// the test goroutine keeps fast's channel empty deterministically —
	// no scheduling race. The earlier background-drainer goroutine could
	// fall behind under load (e.g. CI under -race) and let fast fill up
	// and be disconnected too, flaking this test.
	drainFast := func() {
		for {
			select {
			case <-fast.Outbound():
			default:
				return
			}
		}
	}

	// Push events until slow fills its buffer and is disconnected,
	// draining fast after each broadcast so only slow ever backs up.
	disconnected := false
	for range outboundChanCapacity * 2 {
		r.appendAndBroadcast([]game.Event{makeEvent()})
		drainFast()
		if _, stillSubscribed := r.subs[slow]; !stillSubscribed {
			disconnected = true
			break
		}
	}
	require.True(t, disconnected, "slow subscriber should have been disconnected")

	// slow's channel must be closed (no further sends will ever arrive).
	select {
	case _, ok := <-slow.Outbound():
		// Drain leftover buffered messages until we see closed.
		for ok {
			_, ok = <-slow.Outbound()
		}
	default:
		// Already empty AND closed; receive yields zero/false.
		_, ok := <-slow.Outbound()
		require.False(t, ok, "slow subscriber's channel should be closed")
	}

	// fast subscriber's player slot is untouched.
	require.Contains(t, r.subs, fast)
	// slow subscriber's player slot still exists (can rejoin), but
	// has no attached subscriber.
	slot, ok := r.players["slow"]
	require.True(t, ok, "slow player slot must remain")
	require.Nil(t, slot.sub, "slow's subscriber field must be cleared")
}
