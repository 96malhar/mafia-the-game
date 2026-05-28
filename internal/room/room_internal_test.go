package room

import (
	"context"
	"io"
	"log/slog"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/malhar/mafia-the-game/internal/game"
)

// TestConfig_SubPhaseDuration pins the per-sub-phase duration map.
// All six sub-phases resolve through c.NightSubPhases, which is the
// single source of wall-clock truth (no engine fallback). This test
// verifies the routing AND that defaults match the constants
// documented in config.go.
func TestConfig_SubPhaseDuration(t *testing.T) {
	t.Run("defaults route to package-level constants", func(t *testing.T) {
		c := Config{}
		c.applyDefaults()

		// Opening: universal, day-independent.
		require.Equal(t, DefaultOpeningDuration,
			c.subPhaseDuration(game.NightOpeningStarted{Day: 0}, false),
			"opening default")
		require.Equal(t, DefaultOpeningDuration,
			c.subPhaseDuration(game.NightOpeningStarted{Day: 3}, false),
			"opening default is day-independent today")

		// Narrate: mafia has a Day-0 variant; everyone else is universal.
		mafiaNarrateDay0 := c.subPhaseDuration(
			game.NightNarrationStarted{Role: game.RoleMafia, Day: 0}, false)
		mafiaNarrateDay1 := c.subPhaseDuration(
			game.NightNarrationStarted{Role: game.RoleMafia, Day: 1}, false)
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
					c.subPhaseDuration(game.NightNarrationStarted{Role: r, Day: day}, false),
					"role %q day %d should use DefaultNarrateDuration", r, day)
			}
		}

		// Action: universal.
		require.Equal(t, DefaultActionDuration,
			c.subPhaseDuration(game.NightActionStarted{Role: game.RoleMafia, Day: 0}, false))

		// Settle: universal.
		require.Equal(t, DefaultSettleDuration,
			c.subPhaseDuration(game.NightSettleStarted{Role: game.RoleMafia, Day: 0}, false))

		// Sleep: every shipped role uses the universal default.
		for _, r := range []game.Role{game.RoleMafia, game.RoleDetective, game.RoleDoctor} {
			require.Equal(t, DefaultSleepDuration,
				c.subPhaseDuration(game.NightSleepStarted{Role: r, Day: 0}, false),
				"role %q sleep should use DefaultSleepDuration", r)
		}
	})

	t.Run("ponder default - submitted real role is the short beat", func(t *testing.T) {
		c := Config{}
		c.applyDefaults()
		dur := c.subPhaseDuration(
			game.NightPonderStarted{Role: game.RoleMafia, Day: 0, Phantom: false},
			true)
		require.Equal(t, DefaultPonderRealSubmit, dur,
			"submitted non-detective: short post-submit beat")
	})

	t.Run("ponder default - detective gets a longer beat", func(t *testing.T) {
		c := Config{}
		c.applyDefaults()
		dur := c.subPhaseDuration(
			game.NightPonderStarted{Role: game.RoleDetective, Day: 0, Phantom: false},
			true)
		require.Equal(t, DefaultPonderDetectiveSubmit, dur,
			"detective ponder is sized for read-modal pause")
	})

	t.Run("ponder default - timeout matches submit (audio-cadence parity)", func(t *testing.T) {
		c := Config{}
		c.applyDefaults()
		dur := c.subPhaseDuration(
			game.NightPonderStarted{Role: game.RoleDoctor, Day: 0, Phantom: false},
			false)
		require.Equal(t, DefaultPonderRealSubmit, dur,
			"timeout uses the same beat as submit so observers can't distinguish them")
	})

	t.Run("ponder default - phantom is bounded random", func(t *testing.T) {
		c := Config{}
		c.applyDefaults()
		// Repeat the draw so we exercise the randomness: every draw
		// must fall in [lo, hi].
		for i := 0; i < 64; i++ {
			d := c.subPhaseDuration(
				game.NightPonderStarted{Role: game.RoleDetective, Day: 1, Phantom: true},
				false)
			require.GreaterOrEqual(t, d, DefaultPhantomPonderMin,
				"phantom ponder must be >= DefaultPhantomPonderMin")
			require.LessOrEqual(t, d, DefaultPhantomPonderMax,
				"phantom ponder must be <= DefaultPhantomPonderMax")
		}
		// A phantom MAFIA turn is unreachable (the game ends as
		// soon as the last mafia dies; see beginNextNightTurn's
		// doc), so we don't assert phantom bounds for mafia — the
		// function would still return a value if called, but no
		// real engine path reaches it.
	})

	t.Run("custom Opening function is respected", func(t *testing.T) {
		c := Config{
			NightSubPhases: NightSubPhaseDurations{
				Opening: func() time.Duration { return 100 * time.Millisecond },
			},
		}
		c.applyDefaults()
		require.Equal(t, 100*time.Millisecond,
			c.subPhaseDuration(game.NightOpeningStarted{Day: 0}, false))
	})

	t.Run("custom Ponder function is respected", func(t *testing.T) {
		c := Config{
			NightSubPhases: NightSubPhaseDurations{
				Ponder: func(_ game.Role, _, _ bool) time.Duration {
					return 250 * time.Millisecond
				},
			},
		}
		c.applyDefaults()
		require.Equal(t, 250*time.Millisecond,
			c.subPhaseDuration(
				game.NightPonderStarted{Role: game.RoleMafia, Day: 0, Phantom: false},
				true))
	})

	t.Run("custom Settle function is respected", func(t *testing.T) {
		c := Config{
			NightSubPhases: NightSubPhaseDurations{
				Settle: func() time.Duration { return 50 * time.Millisecond },
			},
		}
		c.applyDefaults()
		require.Equal(t, 50*time.Millisecond,
			c.subPhaseDuration(game.NightSettleStarted{Role: game.RoleDoctor, Day: 0}, false))
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
	go func() {
		for range fast.Outbound() { //nolint:revive // intentional drain
		}
	}()

	// A public event so both subscribers receive it.
	makeEvent := func() game.Event {
		return game.PlayerJoined{PlayerID: "x", Name: "x"}
	}

	// Push events into the room until slow is disconnected. We yield
	// after each broadcast so the fast subscriber's drainer goroutine
	// gets a chance to run — without this the test loop dominates the
	// CPU and the drainer falls behind, causing FAST to fill its
	// buffer and be incorrectly disconnected too.
	disconnected := false
	for i := 0; i < outboundChanCapacity*2; i++ {
		r.appendAndBroadcast([]game.Event{makeEvent()})
		runtime.Gosched()
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
