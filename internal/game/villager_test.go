package game_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/96malhar/mafia-the-game/internal/game"
)

// Villager-specific behaviour: the plain townsperson has no night
// action, wins with the town, never appears in the mafia roster, and
// participates only in the daytime vote.

func TestVillager_FactionIsTown(t *testing.T) {
	require.Equal(t, game.FactionTown, game.RoleVillager.Faction())
	require.False(t, game.FactionTown.MafiaAligned(),
		"the town faction is not mafia-aligned")
}

func TestVillager_HasNoNightAction(t *testing.T) {
	// A villager submitting a night action is rejected with
	// ErrNotYourAction regardless of whose turn it is — they have no
	// power to use. This is distinct from ErrNotYourTurn (wrong time for
	// a role that DOES act).
	g := fixedRoster(t)
	require.Equal(t, game.RoleMafia, g.State().CurrentNightRole())

	_, err := g.Apply(game.NightAction{Actor: "town1", Target: "town2"})
	require.ErrorIs(t, err, game.ErrNotYourAction,
		"a villager has no night action to perform")
}

func TestVillager_NeverAppearsInMafiaRoster(t *testing.T) {
	g := game.New()
	_, err := g.Apply(standardCreate("g1", 0))
	require.NoError(t, err)
	addPlayers(t, g, "a", "b", "c", "d", "e")
	start, err := g.Apply(game.StartGame{})
	require.NoError(t, err)

	roleByID := map[game.PlayerID]game.Role{}
	for _, p := range g.State().Players() {
		roleByID[p.ID()] = p.Role()
	}
	roster, ok := findEvent[game.MafiaRosterRevealed](start)
	require.True(t, ok)
	for _, m := range roster.Members {
		require.NotEqual(t, game.RoleVillager, roleByID[m],
			"a villager must never be listed in the mafia roster")
	}
}

func TestVillager_VotesAndCanBeVotedDuringDay(t *testing.T) {
	// The villager's only agency is the daytime vote: they can cast one
	// and can themselves be a vote target.
	g := fixedRoster(t)
	toDayVote(t, g, map[game.Role]game.PlayerID{
		game.RoleMafia:  "town1",
		game.RoleDoctor: "town1", // save -> town1 lives to vote
	})

	evts, err := g.Apply(game.DayVote{Voter: "town1", Target: "mafia1"})
	require.NoError(t, err, "a living villager may vote")
	cast, ok := findEvent[game.VoteCast](evts)
	require.True(t, ok)
	require.Equal(t, game.PlayerID("town1"), cast.Voter)

	_, err = g.Apply(game.DayVote{Voter: "town2", Target: "town1"})
	require.NoError(t, err, "a villager may be voted against")
}

func TestVillager_CountsTowardTownWin(t *testing.T) {
	// Villagers are town: lynching the last mafia hands the town the win
	// while villagers are still alive and counted on the town side.
	g := fixedRoster(t)
	playNight(t, g, nil) // quiet night, everyone idles
	require.Equal(t, game.PhaseDayDiscussion, g.State().Phase())

	evts := finalizeLynch(t, g, "mafia1")
	ge, ok := findEvent[game.GameEnded](evts)
	require.True(t, ok, "lynching the last mafia ends the game")
	require.Equal(t, game.FactionTown, ge.Winner,
		"the surviving villagers win with the town")
}
