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

// TestConfig_NightTurnDuration pins the per-role-per-day duration
// formula: NightTurnGrace(role, day) + NightActionDuration for real
// turns; PhantomTurnDuration(role, day) for phantom turns. Defaults
// (see DefaultNightTurnGrace) account for the shared "City, go to
// sleep" + 5s settle beat that opens every night, plus the role's
// own audio. So day-0 mafia gets 10s, day>=1 mafia gets 7s, and
// other roles (detective, doctor) get 2.5s.
func TestConfig_NightTurnDuration(t *testing.T) {
	t.Run("real-turn defaults", func(t *testing.T) {
		c := Config{}
		c.applyDefaults()
		require.Equal(t, 45*time.Second, c.NightActionDuration)
		require.Equal(t, 55*time.Second, c.nightTurnDuration(game.RoleMafia, 0, false),
			"day-0 mafia: 10s grace + 45s action (city sleep + look-around beat)")
		require.Equal(t, 52*time.Second, c.nightTurnDuration(game.RoleMafia, 1, false),
			"day>=1 mafia: 7s grace + 45s action (city sleep + single wake cue)")
		require.Equal(t, 47500*time.Millisecond, c.nightTurnDuration(game.RoleDetective, 0, false))
		require.Equal(t, 47500*time.Millisecond, c.nightTurnDuration(game.RoleDoctor, 0, false))
	})

	t.Run("phantom-turn defaults are bounded", func(t *testing.T) {
		c := Config{}
		c.applyDefaults()
		// Repeat the draw so we exercise the randomness: every draw
		// must fall in [lo, hi] for the role/day.
		for i := 0; i < 64; i++ {
			d := c.nightTurnDuration(game.RoleDetective, 1, true)
			require.GreaterOrEqual(t, d, PhantomTurnMin,
				"phantom turn must be >= PhantomTurnMin")
			require.LessOrEqual(t, d, PhantomTurnMax,
				"phantom turn must be <= PhantomTurnMax")
		}
		// A phantom MAFIA turn is unreachable (see
		// DefaultPhantomTurnDuration's doc comment for the
		// invariant): the game ends as soon as the last mafia
		// dies, so beginNightTurns can only emit
		// NightTurnStarted{Role: mafia, Phantom: false}. We
		// therefore don't assert on phantom mafia bounds — the
		// function would still return a value if called, but no
		// real engine path reaches it.
	})

	t.Run("custom grace function", func(t *testing.T) {
		c := Config{
			NightActionDuration: 4 * time.Second,
			NightTurnGrace: func(_ game.Role, _ int) time.Duration {
				return time.Second
			},
		}
		c.applyDefaults()
		require.Equal(t, 5*time.Second, c.nightTurnDuration(game.RoleMafia, 0, false))
	})

	t.Run("custom phantom function", func(t *testing.T) {
		c := Config{
			PhantomTurnDuration: func(_ game.Role, _ int) time.Duration {
				return 100 * time.Millisecond
			},
		}
		c.applyDefaults()
		require.Equal(t, 100*time.Millisecond,
			c.nightTurnDuration(game.RoleDetective, 0, true))
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
