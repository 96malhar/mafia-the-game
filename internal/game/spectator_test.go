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

// TestSpectator_PrivateRoleResultsNotLeakedToTheDead guards the boundary the
// spectator feed must NOT cross: a dead spectator may watch WHO acted on whom
// (SpectatorNightAction), but must never receive the private OUTCOMES or
// secrets those roles learn — the detective's investigation result above all,
// plus the mafia's faction kill ack, the mafia roster, and a roleblock
// notice. Adding the spectator feed widened only SpectatorNightAction's
// audience; every other night event keeps its original privacy.
func TestSpectator_PrivateRoleResultsNotLeakedToTheDead(t *testing.T) {
	g := fixedRoster(t)
	playNight(t, g, map[game.Role]game.PlayerID{game.RoleMafia: "town2"})
	require.False(t, livingByID(g, "town2"), "town2 should be dead after the night")

	events := []game.Event{
		// What the dead ARE allowed to spectate: who the detective targeted.
		game.SpectatorNightAction{
			Actor: "det", ActorRole: game.RoleDetective,
			Target: "mafia1", TargetRole: game.RoleMafia,
		},
		// Private outcomes/secrets the dead must NOT receive.
		game.DetectiveResult{Detective: "det", Target: "mafia1", IsMafia: true},
		game.NightActionRecorded{Actor: "mafia1", Target: "town1", Faction: game.FactionMafia},
		game.MafiaRosterRevealed{Members: []game.PlayerID{"mafia1"}},
		game.Blocked{PlayerID: "doc"},
	}

	t.Run("a dead spectator sees the action feed but none of the secrets", func(t *testing.T) {
		out := game.Project("town2", events, g.State())
		require.NotEmpty(t, findAllEvents[game.SpectatorNightAction](out),
			"the dead spectate who the detective targeted")
		require.Empty(t, findAllEvents[game.DetectiveResult](out),
			"the dead must NOT learn the detective's investigation result")
		require.Empty(t, findAllEvents[game.NightActionRecorded](out),
			"the dead must NOT receive the mafia's private faction kill ack")
		require.Empty(t, findAllEvents[game.MafiaRosterRevealed](out),
			"the dead must NOT receive the mafia roster")
		require.Empty(t, findAllEvents[game.Blocked](out),
			"the dead must NOT receive a private roleblock notice")
	})

	t.Run("the detective still receives their own result", func(t *testing.T) {
		// PrivateTo is aliveness-agnostic: the owner sees it whether alive or
		// dead — what's withheld is only NON-owner dead spectators.
		out := game.Project("det", events, g.State())
		require.NotEmpty(t, findAllEvents[game.DetectiveResult](out),
			"the detective sees their own investigation result")
	})
}
