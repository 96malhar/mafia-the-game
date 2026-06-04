package game_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/96malhar/mafia-the-game/internal/game"
)

// TestSpectator_NightActionMirrorsToGraveyard asserts that every submitted
// night action emits a Graveyard-scoped SpectatorNightAction carrying both
// participants' roles, so dead spectators can render the night feed as
// "Actor (role) targeted Target (role)".
func TestSpectator_NightActionMirrorsToGraveyard(t *testing.T) {
	// fixedRoster leaves the game on the mafia act window; town1/town2 are
	// villagers (see fixedRoster).
	g := fixedRoster(t)
	evts := nightAction(t, g, "mafia1", "town1")

	sa, ok := findEvent[game.SpectatorNightAction](evts)
	require.True(t, ok, "a submitted night action mirrors to the graveyard")
	require.Equal(t, game.PlayerID("mafia1"), sa.Actor)
	require.Equal(t, game.RoleMafia, sa.ActorRole)
	require.Equal(t, game.PlayerID("town1"), sa.Target)
	require.Equal(t, game.RoleVillager, sa.TargetRole)
	require.Equal(t, game.Graveyard().Audience, sa.Visibility().Audience,
		"the spectator feed is graveyard-only — never seen by the living")
}

// TestSpectator_NightActionReachesOnlyTheDead asserts the spectator feed is
// projected to dead players and to no living (or unknown) viewer — a living
// leak would hand the table cross-role night targeting.
func TestSpectator_NightActionReachesOnlyTheDead(t *testing.T) {
	g := fixedRoster(t)
	playNight(t, g, map[game.Role]game.PlayerID{game.RoleMafia: "town2"})
	require.False(t, livingByID(g, "town2"), "town2 should be dead after the night")

	events := []game.Event{
		game.SpectatorNightAction{
			Actor: "mafia1", ActorRole: game.RoleMafia,
			Target: "town1", TargetRole: game.RoleVillager,
		},
	}

	t.Run("a dead player sees the night action", func(t *testing.T) {
		out := game.Project("town2", events, g.State())
		require.Len(t, out, 1, "town2 (dead) must receive the spectator feed")
	})

	t.Run("the living and unknown viewers see nothing", func(t *testing.T) {
		assertNobodySees(t, g.State(), events,
			[]game.PlayerID{"mafia1", "det", "doc", "town1", "stranger"},
			"a spectator night action")
	})
}
