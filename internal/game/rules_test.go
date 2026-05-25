package game_test

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/malhar/mafia-the-game/internal/game"
)

// Use the package-external test idiom (game_test) so we exercise only the
// public API — the same surface real callers will see.

// standardRoster returns a 5-player roster: 1 mafia, 1 detective, 1 doctor,
// 2 villagers. Convenient for tests that don't care about composition.
func standardRoster() []game.Role {
	return []game.Role{
		game.RoleMafia,
		game.RoleDetective,
		game.RoleDoctor,
		game.RoleVillager,
		game.RoleVillager,
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
		evts, err := g.Apply(game.CreateGame{
			GameID: "g1",
			Roles:  standardRoster(),
			Seed:   42,
		})
		require.NoError(t, err)
		require.Len(t, evts, 1, "exactly one event")

		created, ok := findEvent[game.GameCreated](evts)
		require.True(t, ok, "GameCreated event present")
		require.Equal(t, game.GameID("g1"), created.GameID)
		require.Equal(t, int64(42), created.Seed)
		require.Equal(t, standardRoster(), created.Roles)

		require.Equal(t, game.PhaseLobby, g.State().Phase())
		require.Equal(t, game.GameID("g1"), g.State().ID())
	})

	t.Run("rejects duplicate CreateGame", func(t *testing.T) {
		g := game.New()
		_, err := g.Apply(game.CreateGame{GameID: "g1", Roles: standardRoster()})
		require.NoError(t, err)

		_, err = g.Apply(game.CreateGame{GameID: "g2", Roles: standardRoster()})
		require.ErrorIs(t, err, game.ErrWrongPhase)
	})

	t.Run("validation errors", func(t *testing.T) {
		cases := []struct {
			name string
			cmd  game.CreateGame
		}{
			{
				name: "empty GameID",
				cmd:  game.CreateGame{Roles: standardRoster()},
			},
			{
				name: "too few roles",
				cmd:  game.CreateGame{GameID: "g1", Roles: []game.Role{game.RoleMafia, game.RoleVillager}},
			},
			{
				name: "unknown role",
				cmd:  game.CreateGame{GameID: "g1", Roles: []game.Role{"jester", game.RoleVillager, game.RoleVillager}},
			},
			{
				name: "no mafia",
				cmd:  game.CreateGame{GameID: "g1", Roles: []game.Role{game.RoleVillager, game.RoleVillager, game.RoleVillager}},
			},
			{
				name: "no town",
				cmd:  game.CreateGame{GameID: "g1", Roles: []game.Role{game.RoleMafia, game.RoleMafia, game.RoleMafia}},
			},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				g := game.New()
				_, err := g.Apply(tc.cmd)
				require.Error(t, err)
			})
		}
	})
}

func TestAddPlayer(t *testing.T) {
	newWithCreated := func(t *testing.T) *game.Game {
		t.Helper()
		g := game.New()
		_, err := g.Apply(game.CreateGame{GameID: "g1", Roles: standardRoster()})
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

	t.Run("rejects empty fields", func(t *testing.T) {
		g := newWithCreated(t)
		_, err := g.Apply(game.AddPlayer{PlayerID: "", Name: "Alice"})
		require.Error(t, err)
		_, err = g.Apply(game.AddPlayer{PlayerID: "p1", Name: ""})
		require.Error(t, err)
	})

	t.Run("rejects join when lobby full", func(t *testing.T) {
		g := newWithCreated(t) // 5-role roster
		names := []string{"a", "b", "c", "d", "e"}
		for i, n := range names {
			_, err := g.Apply(game.AddPlayer{PlayerID: game.PlayerID(n), Name: n})
			require.NoError(t, err, "fill %d", i)
		}
		_, err := g.Apply(game.AddPlayer{PlayerID: "f", Name: "f"})
		require.Error(t, err, "6th should be rejected")
	})

	t.Run("rejects AddPlayer before CreateGame", func(t *testing.T) {
		g := game.New()
		_, err := g.Apply(game.AddPlayer{PlayerID: "p1", Name: "Alice"})
		require.ErrorIs(t, err, game.ErrWrongPhase)
	})
}

func TestStartGame(t *testing.T) {
	fillLobby := func(t *testing.T, seed int64) *game.Game {
		t.Helper()
		g := game.New()
		_, err := g.Apply(game.CreateGame{GameID: "g1", Roles: standardRoster(), Seed: seed})
		require.NoError(t, err)
		for _, n := range []string{"a", "b", "c", "d", "e"} {
			_, err := g.Apply(game.AddPlayer{PlayerID: game.PlayerID(n), Name: n})
			require.NoError(t, err)
		}
		return g
	}

	t.Run("happy path: phase advances, every player has a valid role", func(t *testing.T) {
		g := fillLobby(t, 42)
		evts, err := g.Apply(game.StartGame{})
		require.NoError(t, err)

		// Expect: GameStarted, 5x RoleAssigned, PhaseChanged.
		require.Len(t, evts, 7)
		_, ok := findEvent[game.GameStarted](evts)
		require.True(t, ok, "GameStarted present")

		pc, ok := findEvent[game.PhaseChanged](evts)
		require.True(t, ok, "PhaseChanged present")
		require.Equal(t, game.PhaseLobby, pc.From)
		require.Equal(t, game.PhaseNight, pc.To)
		require.Equal(t, 0, pc.Day)

		require.Equal(t, game.PhaseNight, g.State().Phase())

		// Every player got exactly one valid role; multiset of dealt
		// roles equals the configured roster.
		players := g.State().Players()
		dealt := make([]game.Role, 0, len(players))
		for _, p := range players {
			require.True(t, p.Role().Valid(), "player %s has invalid role %q", p.ID(), p.Role())
			require.True(t, p.Alive(), "player %s should start alive", p.ID())
			dealt = append(dealt, p.Role())
		}
		require.ElementsMatch(t, standardRoster(), dealt)
	})

	t.Run("RoleAssigned events are private to each player", func(t *testing.T) {
		g := fillLobby(t, 1)
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
		first := fillLobby(t, seed)
		_, err := first.Apply(game.StartGame{})
		require.NoError(t, err)

		second := fillLobby(t, seed)
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
		a := fillLobby(t, 1)
		b := fillLobby(t, 999)
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

	t.Run("rejects when lobby not full", func(t *testing.T) {
		g := game.New()
		_, err := g.Apply(game.CreateGame{GameID: "g1", Roles: standardRoster()})
		require.NoError(t, err)
		_, err = g.Apply(game.AddPlayer{PlayerID: "p1", Name: "Alice"})
		require.NoError(t, err)

		_, err = g.Apply(game.StartGame{})
		require.ErrorIs(t, err, game.ErrRosterMismatch)
	})

	t.Run("rejects StartGame in non-lobby phase", func(t *testing.T) {
		g := fillLobby(t, 1)
		_, err := g.Apply(game.StartGame{})
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
		game.ErrGameEnded,
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
