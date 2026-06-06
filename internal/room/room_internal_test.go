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
// constants — the single source of wall-clock truth.
func TestConfig_SubPhaseDuration(t *testing.T) {
	c := Config{}
	c.applyDefaults()

	sub := func(s game.NightSubPhase, role game.Role, day int, phantom bool) game.NightSubPhaseStarted {
		return game.NightSubPhaseStarted{Sub: s, Role: role, Day: day, Phantom: phantom}
	}

	t.Run("defaults route to package-level constants", func(t *testing.T) {
		// Opening: universal, day-independent.
		require.Equal(t, DefaultOpeningDuration,
			c.subPhaseDuration(sub(game.NightSubOpening, "", 0, false)),
			"opening default")
		require.Equal(t, DefaultOpeningDuration,
			c.subPhaseDuration(sub(game.NightSubOpening, "", 3, false)),
			"opening default is day-independent today")

		// Narrate: mafia has a Day-0 variant; everyone else is universal.
		mafiaNarrateDay0 := c.subPhaseDuration(sub(game.NightSubNarrate, game.RoleMafia, 0, false))
		mafiaNarrateDay1 := c.subPhaseDuration(sub(game.NightSubNarrate, game.RoleMafia, 1, false))
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
					c.subPhaseDuration(sub(game.NightSubNarrate, r, day, false)),
					"role %q day %d should use DefaultNarrateDuration", r, day)
			}
		}

		// Action: universal. A role that can't act doesn't reach the act
		// sub-phase at all (its turn is phantom — narrate -> ponder), so
		// there's no blocked/shortened act variant to size here.
		require.Equal(t, DefaultActionDuration,
			c.subPhaseDuration(sub(game.NightSubAct, game.RoleMafia, 0, false)))

		// Settle: universal.
		require.Equal(t, DefaultSettleDuration,
			c.subPhaseDuration(sub(game.NightSubSettle, game.RoleMafia, 0, false)))

		// Sleep: every shipped role uses the universal default.
		for _, r := range []game.Role{game.RoleMafia, game.RoleDetective, game.RoleDoctor} {
			require.Equal(t, DefaultSleepDuration,
				c.subPhaseDuration(sub(game.NightSubSleep, r, 0, false)),
				"role %q sleep should use DefaultSleepDuration", r)
		}
	})

	t.Run("ponder default - non-detective real role uses the real-submit beat", func(t *testing.T) {
		// Submit vs timeout is intentionally indistinguishable, so the
		// ponder beat depends only on the role.
		require.Equal(t, DefaultPonderRealSubmit,
			c.subPhaseDuration(sub(game.NightSubPonder, game.RoleMafia, 0, false)),
			"non-detective real ponder uses the real-submit beat")
		require.Equal(t, DefaultPonderRealSubmit,
			c.subPhaseDuration(sub(game.NightSubPonder, game.RoleDoctor, 0, false)),
			"doctor too")
	})

	t.Run("ponder default - result-modal roles use the result-submit beat", func(t *testing.T) {
		// The detective and the tracker each pop a private result modal at
		// action time, so both get the longer read-the-modal ponder.
		require.Equal(t, DefaultPonderResultSubmit,
			c.subPhaseDuration(sub(game.NightSubPonder, game.RoleDetective, 0, false)),
			"detective ponder is sized for the read-modal pause")
		require.Equal(t, DefaultPonderResultSubmit,
			c.subPhaseDuration(sub(game.NightSubPonder, game.RoleTracker, 0, false)),
			"tracker ponder is sized for the read-modal pause too")
	})

	t.Run("ponder default - phantom is bounded random", func(t *testing.T) {
		// A phantom turn (dead / spent / blocked role) gets a randomized
		// beat. Repeat the draw so we exercise the randomness: every draw
		// must fall in [lo, hi].
		for range 64 {
			d := c.subPhaseDuration(sub(game.NightSubPonder, game.RoleDetective, 1, true))
			require.GreaterOrEqual(t, d, DefaultPhantomPonderMin,
				"phantom ponder must be >= DefaultPhantomPonderMin")
			require.LessOrEqual(t, d, DefaultPhantomPonderMax,
				"phantom ponder must be <= DefaultPhantomPonderMax")
		}
		// A phantom MAFIA turn is unreachable (the game ends as soon as
		// the last mafia dies; see beginNextNightTurn's doc), so we
		// don't assert phantom bounds for mafia.
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

// timerTestRoom builds a Room with only what the timer/deadline helpers
// touch: a defaulted Config (the source of sub-phase durations) and a
// nil phaseTimer. No run loop, engine, or logger is needed — these
// helpers never reach for them.
func timerTestRoom() *Room {
	cfg := Config{}
	cfg.applyDefaults()
	return &Room{cfg: cfg}
}

// TestRoom_StampNightDeadlines pins the deadline-stamping side of the
// timer machinery in isolation (the integration tests only observe it
// end-to-end). It asserts that night sub-phase events get a wall-clock
// deadline of now+subPhaseDuration, that the randomized phantom-ponder
// window lands inside its [min,max] band, and that non-sub-phase events
// pass through untouched.
func TestRoom_StampNightDeadlines(t *testing.T) {
	r := timerTestRoom()

	act := game.NightSubPhaseStarted{Sub: game.NightSubAct, Role: game.RoleMafia, Day: 0}
	phantomPonder := game.NightSubPhaseStarted{Sub: game.NightSubPonder, Role: game.RoleDoctor, Day: 1, Phantom: true}
	other := game.PlayerKilled{PlayerID: "x"}
	batch := []game.Event{act, other, phantomPonder}

	// stampNightDeadlines reads time.Now() once for the whole batch;
	// bracket it so the asserted bounds account for that single read.
	before := time.Now()
	r.stampNightDeadlines(batch)
	after := time.Now()

	// Non-sub-phase events are never stamped.
	require.Equal(t, other, batch[1], "non-sub-phase events must pass through unchanged")

	// Fixed sub-phase: deadline == now + DefaultActionDuration.
	gotAct, ok := batch[0].(game.NightSubPhaseStarted)
	require.True(t, ok)
	require.GreaterOrEqual(t, gotAct.Deadline, before.Add(DefaultActionDuration).UnixMilli())
	require.LessOrEqual(t, gotAct.Deadline, after.Add(DefaultActionDuration).UnixMilli())

	// Randomized phantom-ponder window: deadline lands in [min,max].
	gotPonder, ok := batch[2].(game.NightSubPhaseStarted)
	require.True(t, ok)
	require.GreaterOrEqual(t, gotPonder.Deadline, before.Add(DefaultPhantomPonderMin).UnixMilli())
	require.LessOrEqual(t, gotPonder.Deadline, after.Add(DefaultPhantomPonderMax).UnixMilli())
}

// TestRoom_ScheduleTimers pins the arming side of the timer machinery:
// night sub-phases arm the single phaseTimer, transitions into a
// host-driven day phase clear any inherited timer, and day phases never
// arm one. These are exercised end-to-end by the synctest integration
// tests; here we assert the wiring directly without a fake clock.
func TestRoom_ScheduleTimers(t *testing.T) {
	t.Run("night sub-phase arms the phase timer", func(t *testing.T) {
		r := timerTestRoom()
		t.Cleanup(r.stopPhaseTimer)
		r.scheduleTimers([]game.Event{
			game.NightSubPhaseStarted{Sub: game.NightSubAct, Role: game.RoleMafia, Day: 0},
		})
		require.NotNil(t, r.phaseTimer, "a night sub-phase must arm the auto-advance timer")
	})

	t.Run("entering night arms the opening timer", func(t *testing.T) {
		r := timerTestRoom()
		t.Cleanup(r.stopPhaseTimer)
		r.scheduleTimers([]game.Event{
			game.PhaseChanged{From: game.PhaseLobby, To: game.PhaseNight},
			game.NightSubPhaseStarted{Sub: game.NightSubOpening, Day: 0},
		})
		require.NotNil(t, r.phaseTimer, "the opening sub-phase must arm a timer even alongside PhaseChanged")
	})

	t.Run("transition to a day phase clears an inherited timer", func(t *testing.T) {
		r := timerTestRoom()
		// Pretend a night sub-phase timer is still armed when we cross
		// into the day; scheduleTimers must stop it so it can't leak.
		r.phaseTimer = time.NewTimer(time.Hour)
		r.scheduleTimers([]game.Event{
			game.PhaseChanged{From: game.PhaseNight, To: game.PhaseDayDiscussion},
		})
		require.Nil(t, r.phaseTimer, "day phases are host-driven — the night timer must be cleared")
	})

	t.Run("a day phase arms no timer", func(t *testing.T) {
		r := timerTestRoom()
		r.scheduleTimers([]game.Event{
			game.PhaseChanged{From: game.PhaseDayDiscussion, To: game.PhaseDayVote},
		})
		require.Nil(t, r.phaseTimer, "day vote is host-driven — no timer should be armed")
	})
}
