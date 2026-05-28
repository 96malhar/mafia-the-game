package game_test

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/malhar/mafia-the-game/internal/game"
)

// Use the package-external test idiom (game_test) so we exercise only the
// public API — the same surface real callers will see.

// standardCreate returns a CreateGame command suitable for most tests:
// 5..20 player range, 1 mafia (so the default 5-player smoke games are
// valid). Seed defaults to 0; pass nonzero to vary deterministically.
func standardCreate(id game.GameID, seed int64) game.CreateGame {
	return game.CreateGame{
		GameID:     id,
		MinPlayers: 5,
		MaxPlayers: 20,
		MafiaCount: 1,
		Seed:       seed,
	}
}

// findEvent returns the first event matching the given concrete type, or
// nil. Used by tests that assert on a specific event.
func findEvent[T game.Event](events []game.Event) (T, bool) {
	var zero T
	for _, e := range events {
		if v, ok := e.(T); ok {
			return v, true
		}
	}
	return zero, false
}

func TestCreateGame(t *testing.T) {
	t.Run("happy path emits GameCreated and sets state", func(t *testing.T) {
		g := game.New()
		evts, err := g.Apply(standardCreate("g1", 42))
		require.NoError(t, err)
		require.Len(t, evts, 1, "exactly one event")

		created, ok := findEvent[game.GameCreated](evts)
		require.True(t, ok, "GameCreated event present")
		require.Equal(t, game.GameID("g1"), created.GameID)
		require.Equal(t, int64(42), created.Seed)
		require.Equal(t, 5, created.MinPlayers)
		require.Equal(t, 20, created.MaxPlayers)
		require.Equal(t, 1, created.MafiaCount)

		require.Equal(t, game.PhaseLobby, g.State().Phase())
		require.Equal(t, game.GameID("g1"), g.State().ID())
		require.Equal(t, 5, g.State().MinPlayers())
		require.Equal(t, 20, g.State().MaxPlayers())
		require.Equal(t, 1, g.State().MafiaCount())
	})

	t.Run("zero MinPlayers/MaxPlayers fall back to defaults", func(t *testing.T) {
		g := game.New()
		evts, err := g.Apply(game.CreateGame{GameID: "g1"})
		require.NoError(t, err)
		created, _ := findEvent[game.GameCreated](evts)
		require.GreaterOrEqual(t, created.MaxPlayers, 5)
		require.GreaterOrEqual(t, created.MafiaCount, 1)
	})

	t.Run("rejects duplicate CreateGame", func(t *testing.T) {
		g := game.New()
		_, err := g.Apply(standardCreate("g1", 0))
		require.NoError(t, err)

		_, err = g.Apply(standardCreate("g2", 0))
		require.ErrorIs(t, err, game.ErrWrongPhase)
	})

	t.Run("validation errors", func(t *testing.T) {
		cases := []struct {
			name string
			cmd  game.CreateGame
		}{
			{
				name: "empty GameID",
				cmd:  game.CreateGame{MinPlayers: 5, MaxPlayers: 20, MafiaCount: 1},
			},
			{
				name: "MinPlayers too low",
				cmd:  game.CreateGame{GameID: "g1", MinPlayers: 3, MaxPlayers: 20, MafiaCount: 1},
			},
			{
				name: "MaxPlayers < MinPlayers",
				cmd:  game.CreateGame{GameID: "g1", MinPlayers: 10, MaxPlayers: 5, MafiaCount: 1},
			},
			{
				name: "mafia count zero is replaced by default, not an error",
				cmd:  game.CreateGame{GameID: "g1", MinPlayers: 5, MaxPlayers: 20, MafiaCount: 0},
				// This one should NOT error — we encode the "default" path.
			},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				g := game.New()
				_, err := g.Apply(tc.cmd)
				if tc.name == "mafia count zero is replaced by default, not an error" {
					require.NoError(t, err)
				} else {
					require.Error(t, err)
				}
			})
		}
	})

	t.Run("mafia count out of range is rejected", func(t *testing.T) {
		g := game.New()
		_, err := g.Apply(game.CreateGame{
			GameID:     "g1",
			MinPlayers: 5,
			MaxPlayers: 5,
			MafiaCount: 3, // > 5 - 2 - 1 = 2
		})
		require.Error(t, err)
	})
}

func TestAddPlayer(t *testing.T) {
	newWithCreated := func(t *testing.T) *game.Game {
		t.Helper()
		g := game.New()
		_, err := g.Apply(standardCreate("g1", 0))
		require.NoError(t, err)
		return g
	}

	t.Run("happy path emits PlayerJoined and registers player", func(t *testing.T) {
		g := newWithCreated(t)
		evts, err := g.Apply(game.AddPlayer{PlayerID: "p1", Name: "Alice"})
		require.NoError(t, err)
		require.Len(t, evts, 1)

		joined, ok := findEvent[game.PlayerJoined](evts)
		require.True(t, ok)
		require.Equal(t, game.PlayerID("p1"), joined.PlayerID)
		require.Equal(t, "Alice", joined.Name)

		require.Equal(t, 1, g.State().PlayerCount())
	})

	t.Run("preserves join order across multiple players", func(t *testing.T) {
		g := newWithCreated(t)
		for _, p := range []struct {
			id, name string
		}{
			{"p1", "Alice"}, {"p2", "Bob"}, {"p3", "Carol"},
		} {
			_, err := g.Apply(game.AddPlayer{PlayerID: game.PlayerID(p.id), Name: p.name})
			require.NoError(t, err)
		}
		players := g.State().Players()
		require.Equal(t, []game.PlayerID{"p1", "p2", "p3"},
			[]game.PlayerID{players[0].ID(), players[1].ID(), players[2].ID()})
	})

	t.Run("rejects duplicate PlayerID", func(t *testing.T) {
		g := newWithCreated(t)
		_, err := g.Apply(game.AddPlayer{PlayerID: "p1", Name: "Alice"})
		require.NoError(t, err)

		_, err = g.Apply(game.AddPlayer{PlayerID: "p1", Name: "Alice2"})
		require.ErrorIs(t, err, game.ErrDuplicatePlayer)
	})

	t.Run("rejects duplicate name (case + whitespace insensitive)", func(t *testing.T) {
		g := newWithCreated(t)
		_, err := g.Apply(game.AddPlayer{PlayerID: "p1", Name: "Alice"})
		require.NoError(t, err)

		// Exact match.
		_, err = g.Apply(game.AddPlayer{PlayerID: "p2", Name: "Alice"})
		require.ErrorIs(t, err, game.ErrDuplicateName, "exact match must collide")

		// Case-folded match.
		_, err = g.Apply(game.AddPlayer{PlayerID: "p3", Name: "alice"})
		require.ErrorIs(t, err, game.ErrDuplicateName, "case-insensitive collision")
		_, err = g.Apply(game.AddPlayer{PlayerID: "p4", Name: "ALICE"})
		require.ErrorIs(t, err, game.ErrDuplicateName, "case-insensitive collision (upper)")

		// Trimmed match.
		_, err = g.Apply(game.AddPlayer{PlayerID: "p5", Name: "  Alice  "})
		require.ErrorIs(t, err, game.ErrDuplicateName, "whitespace-trimmed collision")
		_, err = g.Apply(game.AddPlayer{PlayerID: "p6", Name: "  ALICE  "})
		require.ErrorIs(t, err, game.ErrDuplicateName, "trim + case-fold collision")

		// A distinct name still works (sanity).
		_, err = g.Apply(game.AddPlayer{PlayerID: "p7", Name: "Bob"})
		require.NoError(t, err, "distinct name must succeed")

		// And the roster reflects exactly two players (Alice and
		// Bob) — none of the rejected joins should have leaked.
		require.Equal(t, 2, len(g.State().Players()))
	})

	t.Run("trims name on store", func(t *testing.T) {
		g := newWithCreated(t)
		evts, err := g.Apply(game.AddPlayer{PlayerID: "p1", Name: "  Alice  "})
		require.NoError(t, err)
		require.Equal(t, "Alice", g.State().Players()[0].Name(),
			"stored name must have leading/trailing whitespace trimmed")
		// The emitted PlayerJoined event carries the trimmed
		// name too — clients shouldn't have to know about the
		// transformation.
		require.Len(t, evts, 1)
		pj, ok := evts[0].(game.PlayerJoined)
		require.True(t, ok)
		require.Equal(t, "Alice", pj.Name)
	})

	t.Run("rejects empty fields", func(t *testing.T) {
		g := newWithCreated(t)
		_, err := g.Apply(game.AddPlayer{PlayerID: "", Name: "Alice"})
		require.Error(t, err)
		_, err = g.Apply(game.AddPlayer{PlayerID: "p1", Name: ""})
		require.Error(t, err)
		// Whitespace-only names trim to empty and must be
		// rejected too — otherwise " " could create a row that
		// looks blank in the UI.
		_, err = g.Apply(game.AddPlayer{PlayerID: "p2", Name: "   "})
		require.Error(t, err, "whitespace-only name must be rejected")
	})

	t.Run("rejects join when lobby reaches MaxPlayers", func(t *testing.T) {
		g := game.New()
		// Use a small MaxPlayers so the test runs quickly.
		_, err := g.Apply(game.CreateGame{
			GameID: "g1", MinPlayers: 5, MaxPlayers: 6, MafiaCount: 1,
		})
		require.NoError(t, err)
		names := []string{"a", "b", "c", "d", "e", "f"}
		for i, n := range names {
			_, err := g.Apply(game.AddPlayer{PlayerID: game.PlayerID(n), Name: n})
			require.NoError(t, err, "fill %d", i)
		}
		_, err = g.Apply(game.AddPlayer{PlayerID: "g", Name: "g"})
		require.ErrorIs(t, err, game.ErrLobbyFull, "7th into a 6-cap lobby must be rejected")
	})

	t.Run("rejects AddPlayer before CreateGame", func(t *testing.T) {
		g := game.New()
		_, err := g.Apply(game.AddPlayer{PlayerID: "p1", Name: "Alice"})
		require.ErrorIs(t, err, game.ErrWrongPhase)
	})

	t.Run("rejects AddPlayer after StartGame (roles dealt)", func(t *testing.T) {
		// StartGame deals roles but does NOT transition out of
		// PhaseLobby — BeginNight does. We still want to block new
		// joiners as soon as roles are dealt, because adding one
		// would require re-dealing the whole roster and leaking
		// information to existing players (see applyAddPlayer doc).
		g := newWithCreated(t)
		for _, n := range []string{"a", "b", "c", "d", "e"} {
			_, err := g.Apply(game.AddPlayer{PlayerID: game.PlayerID(n), Name: n})
			require.NoError(t, err)
		}
		_, err := g.Apply(game.StartGame{})
		require.NoError(t, err)
		// Game is still in PhaseLobby here — verify the precondition
		// so future refactors of StartGame don't silently make this
		// test pass for the wrong reason.
		require.Equal(t, game.PhaseLobby, g.State().Phase())

		_, err = g.Apply(game.AddPlayer{PlayerID: "latecomer", Name: "Latecomer"})
		require.ErrorIs(t, err, game.ErrWrongPhase,
			"AddPlayer after roles are dealt must be rejected with wrong_phase")
	})
}

func TestSetMafiaCount(t *testing.T) {
	newGame := func(t *testing.T) *game.Game {
		t.Helper()
		g := game.New()
		_, err := g.Apply(standardCreate("g1", 0))
		require.NoError(t, err)
		return g
	}

	t.Run("valid change emits MafiaCountChanged", func(t *testing.T) {
		g := newGame(t) // starts at 1 mafia
		evts, err := g.Apply(game.SetMafiaCount{Count: 3})
		require.NoError(t, err)
		require.Len(t, evts, 1)

		ch, ok := findEvent[game.MafiaCountChanged](evts)
		require.True(t, ok)
		require.Equal(t, 1, ch.From)
		require.Equal(t, 3, ch.To)
		require.Equal(t, 3, g.State().MafiaCount())
	})

	t.Run("same value is ErrNoChange", func(t *testing.T) {
		g := newGame(t)
		_, err := g.Apply(game.SetMafiaCount{Count: 1})
		require.ErrorIs(t, err, game.ErrNoChange)
	})

	t.Run("out of range is rejected with ErrRosterMismatch", func(t *testing.T) {
		// Below the lower bound (must be ≥ 1) and above the upper
		// bound (MaxPlayers - reservedTownRoles - 1) both surface
		// the same sentinel as the equivalent rejection in
		// applyStartGame. We assert ErrorIs (not just Error) so a
		// future change that drops the wrapper would fail loudly
		// rather than silently downgrading these to ErrCodeInternal
		// on the wire.
		g := newGame(t) // MaxPlayers=20, so max mafia = 20-2-1 = 17
		_, err := g.Apply(game.SetMafiaCount{Count: 0})
		require.ErrorIs(t, err, game.ErrRosterMismatch)
		_, err = g.Apply(game.SetMafiaCount{Count: 18})
		require.ErrorIs(t, err, game.ErrRosterMismatch)
	})

	t.Run("rejected once roles are dealt (StartGame)", func(t *testing.T) {
		// The picker locks at StartGame, not at BeginNight: once
		// composeRoster has assigned roles, tweaking mafia count
		// would do nothing (it wouldn't re-deal), so we reject
		// to prevent silent no-ops. Same predicate that blocks
		// AddPlayer after StartGame.
		g := newGame(t)
		for _, n := range []string{"a", "b", "c", "d", "e"} {
			_, err := g.Apply(game.AddPlayer{PlayerID: game.PlayerID(n), Name: n})
			require.NoError(t, err)
		}
		_, err := g.Apply(game.StartGame{})
		require.NoError(t, err)
		// Game is still in PhaseLobby here — verify the
		// precondition so a future refactor of StartGame doesn't
		// silently make this test pass for the wrong reason
		// (i.e. by failing the phase check instead of the
		// roles-dealt check).
		require.Equal(t, game.PhaseLobby, g.State().Phase())

		_, err = g.Apply(game.SetMafiaCount{Count: 2})
		require.ErrorIs(t, err, game.ErrWrongPhase,
			"SetMafiaCount after roles are dealt must be rejected with wrong_phase")
	})

	t.Run("rejected outside PhaseLobby (post BeginNight)", func(t *testing.T) {
		// Belt-and-braces: even if the roles-dealt check above
		// were removed, the original phase check should still
		// reject SetMafiaCount once the game has actually
		// progressed past lobby.
		g := newGame(t)
		for _, n := range []string{"a", "b", "c", "d", "e"} {
			_, err := g.Apply(game.AddPlayer{PlayerID: game.PlayerID(n), Name: n})
			require.NoError(t, err)
		}
		_, err := g.Apply(game.StartGame{})
		require.NoError(t, err)
		_, err = g.Apply(game.BeginNight{})
		require.NoError(t, err)

		_, err = g.Apply(game.SetMafiaCount{Count: 2})
		require.ErrorIs(t, err, game.ErrWrongPhase)
	})
}

func TestStartGame(t *testing.T) {
	fillLobby := func(t *testing.T, seed int64, n int) *game.Game {
		t.Helper()
		g := game.New()
		_, err := g.Apply(standardCreate("g1", seed))
		require.NoError(t, err)
		for i := 0; i < n; i++ {
			pid := game.PlayerID(string(rune('a' + i)))
			_, err := g.Apply(game.AddPlayer{PlayerID: pid, Name: string(pid)})
			require.NoError(t, err)
		}
		return g
	}

	t.Run("happy path: roles dealt, phase stays in lobby", func(t *testing.T) {
		g := fillLobby(t, 42, 5)
		evts, err := g.Apply(game.StartGame{})
		require.NoError(t, err)

		// StartGame now deals roles only. The host's BeginNight is
		// what transitions to PhaseNight. So events here are:
		// GameStarted + 5 RoleAssigned. No PhaseChanged.
		require.Len(t, evts, 6)
		_, ok := findEvent[game.GameStarted](evts)
		require.True(t, ok, "GameStarted present")

		_, ok = findEvent[game.PhaseChanged](evts)
		require.False(t, ok, "StartGame must NOT transition phases (BeginNight does that)")
		require.Equal(t, game.PhaseLobby, g.State().Phase(),
			"phase stays in Lobby until host issues BeginNight")

		// Composition: 1 mafia, 1 detective, 1 doctor, 2 villagers.
		players := g.State().Players()
		counts := map[game.Role]int{}
		for _, p := range players {
			require.True(t, p.Role().Valid(), "player %s has invalid role %q", p.ID(), p.Role())
			require.True(t, p.Alive(), "player %s should start alive", p.ID())
			counts[p.Role()]++
		}
		require.Equal(t, 1, counts[game.RoleMafia])
		require.Equal(t, 1, counts[game.RoleDetective])
		require.Equal(t, 1, counts[game.RoleDoctor])
		require.Equal(t, 2, counts[game.RoleVillager])
	})

	t.Run("BeginNight after StartGame transitions to PhaseNight", func(t *testing.T) {
		g := fillLobby(t, 42, 5)
		_, err := g.Apply(game.StartGame{})
		require.NoError(t, err)

		evts, err := g.Apply(game.BeginNight{})
		require.NoError(t, err)
		pc, ok := findEvent[game.PhaseChanged](evts)
		require.True(t, ok)
		require.Equal(t, game.PhaseLobby, pc.From)
		require.Equal(t, game.PhaseNight, pc.To)
		require.Equal(t, game.PhaseNight, g.State().Phase())

		// BeginNight emits the night-scoped opening sub-phase event,
		// NOT a NightNarrationStarted for the mafia. The first role's
		// narrate fires when AdvancePhase elapses the opening
		// (see rules_phase.go's advanceNightSubPhase).
		_, ok = findEvent[game.NightOpeningStarted](evts)
		require.True(t, ok, "BeginNight must emit NightOpeningStarted")
		_, narrate := findEvent[game.NightNarrationStarted](evts)
		require.False(t, narrate,
			"BeginNight must NOT emit NightNarrationStarted; opening comes first")
	})

	t.Run("BeginNight before StartGame is rejected", func(t *testing.T) {
		g := fillLobby(t, 42, 5)
		_, err := g.Apply(game.BeginNight{})
		require.ErrorIs(t, err, game.ErrWrongPhase,
			"BeginNight in lobby requires roles to be dealt first")
	})

	t.Run("composition scales with player count and mafia count", func(t *testing.T) {
		g := game.New()
		_, err := g.Apply(game.CreateGame{
			GameID: "g1", MinPlayers: 5, MaxPlayers: 20, MafiaCount: 2,
		})
		require.NoError(t, err)
		for i := 0; i < 8; i++ {
			pid := game.PlayerID(string(rune('a' + i)))
			_, err := g.Apply(game.AddPlayer{PlayerID: pid, Name: string(pid)})
			require.NoError(t, err)
		}
		_, err = g.Apply(game.StartGame{})
		require.NoError(t, err)

		counts := map[game.Role]int{}
		for _, p := range g.State().Players() {
			counts[p.Role()]++
		}
		require.Equal(t, 2, counts[game.RoleMafia])
		require.Equal(t, 1, counts[game.RoleDetective])
		require.Equal(t, 1, counts[game.RoleDoctor])
		require.Equal(t, 4, counts[game.RoleVillager], "8 players - 2 mafia - det - doc = 4 villagers")
	})

	t.Run("RoleAssigned events are private to each player", func(t *testing.T) {
		g := fillLobby(t, 1, 5)
		evts, err := g.Apply(game.StartGame{})
		require.NoError(t, err)
		for _, e := range evts {
			ra, ok := e.(game.RoleAssigned)
			if !ok {
				continue
			}
			vis := ra.Visibility()
			require.Equal(t, "player", vis.Audience)
			require.Equal(t, ra.PlayerID, vis.Player,
				"RoleAssigned must be visible only to its own subject")
		}
	})

	t.Run("deterministic given seed", func(t *testing.T) {
		const seed int64 = 12345
		first := fillLobby(t, seed, 5)
		_, err := first.Apply(game.StartGame{})
		require.NoError(t, err)

		second := fillLobby(t, seed, 5)
		_, err = second.Apply(game.StartGame{})
		require.NoError(t, err)

		rolesOf := func(g *game.Game) map[game.PlayerID]game.Role {
			out := map[game.PlayerID]game.Role{}
			for _, p := range g.State().Players() {
				out[p.ID()] = p.Role()
			}
			return out
		}
		require.Equal(t, rolesOf(first), rolesOf(second),
			"same seed must yield same role assignment")
	})

	t.Run("different seeds usually differ", func(t *testing.T) {
		// Not a hard guarantee for any one pair, but extremely unlikely
		// to coincide across 5! permutations for the seeds we pick.
		a := fillLobby(t, 1, 5)
		b := fillLobby(t, 999, 5)
		_, _ = a.Apply(game.StartGame{})
		_, _ = b.Apply(game.StartGame{})

		differ := false
		ap, bp := a.State().Players(), b.State().Players()
		for i := range ap {
			if ap[i].Role() != bp[i].Role() {
				differ = true
				break
			}
		}
		require.True(t, differ, "seeds 1 vs 999 produced identical assignments — implausible, check determinism logic")
	})

	t.Run("rejects when player count below MinPlayers", func(t *testing.T) {
		g := fillLobby(t, 0, 4) // need 5
		_, err := g.Apply(game.StartGame{})
		require.ErrorIs(t, err, game.ErrRosterMismatch)
	})

	t.Run("rejects when mafia count would leave no villagers", func(t *testing.T) {
		// 5 players, 3 mafia → only Det + Doc + 0 villagers left. Reject.
		g := game.New()
		_, err := g.Apply(game.CreateGame{
			GameID: "g1", MinPlayers: 5, MaxPlayers: 20, MafiaCount: 3,
		})
		require.NoError(t, err)
		for _, n := range []string{"a", "b", "c", "d", "e"} {
			_, err := g.Apply(game.AddPlayer{PlayerID: game.PlayerID(n), Name: n})
			require.NoError(t, err)
		}
		_, err = g.Apply(game.StartGame{})
		require.ErrorIs(t, err, game.ErrRosterMismatch)
	})

	t.Run("rejects StartGame after roles dealt (idempotency guard)", func(t *testing.T) {
		g := fillLobby(t, 1, 5)
		_, err := g.Apply(game.StartGame{})
		require.NoError(t, err)

		// Second StartGame should fail: roles already exist on the
		// players. Re-shuffling mid-game would be a disaster.
		_, err = g.Apply(game.StartGame{})
		require.ErrorIs(t, err, game.ErrWrongPhase)
	})

	t.Run("rejects StartGame in PhaseNight", func(t *testing.T) {
		g := fillLobby(t, 1, 5)
		_, err := g.Apply(game.StartGame{})
		require.NoError(t, err)
		_, err = g.Apply(game.BeginNight{})
		require.NoError(t, err)
		_, err = g.Apply(game.StartGame{})
		require.ErrorIs(t, err, game.ErrWrongPhase)
	})
}

// TestSentinelsAreDistinct guards against the easy refactor mistake of
// collapsing two errors into the same value.
func TestSentinelsAreDistinct(t *testing.T) {
	sentinels := []error{
		game.ErrWrongPhase,
		game.ErrUnknownPlayer,
		game.ErrDuplicatePlayer,
		game.ErrPlayerDead,
		game.ErrNotYourAction,
		game.ErrSelfTarget,
		game.ErrRosterMismatch,
		game.ErrLobbyFull,
		game.ErrGameEnded,
		game.ErrNoChange,
		game.ErrAlreadyActed,
	}
	for i, a := range sentinels {
		for j, b := range sentinels {
			if i == j {
				continue
			}
			require.False(t, errors.Is(a, b),
				"sentinel %d (%v) must not equal sentinel %d (%v)", i, a, j, b)
		}
	}
}
