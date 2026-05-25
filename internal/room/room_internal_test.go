package room

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/malhar/mafia-the-game/internal/game"
)

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
		GameID: "g",
		Roles:  []game.Role{game.RoleMafia, game.RoleVillager, game.RoleVillager},
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

	// Push events into the room until slow is disconnected. We use a
	// loop bound generously larger than the buffer to allow for any
	// per-call accounting noise.
	disconnected := false
	for i := 0; i < outboundChanCapacity*2; i++ {
		r.appendAndBroadcast([]game.Event{makeEvent()})
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
