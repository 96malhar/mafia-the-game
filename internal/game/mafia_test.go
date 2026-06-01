package game_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/96malhar/mafia-the-game/internal/game"
)

// Mafia-specific behaviour: the faction kill, the faction-collective
// single-action-per-night rule, the can't-kill-your-own constraint, the
// faction-scoped roster reveal, the parity win, and the never-phantom
// turn invariant. Generic night plumbing (turn order, phantom turns for
// other roles) lives in rules_phase_test.go.

func TestMafia_FactionIsMafiaAligned(t *testing.T) {
	require.Equal(t, game.FactionMafia, game.RoleMafia.Faction(),
		"the mafia role belongs to the mafia faction")
	require.True(t, game.FactionMafia.MafiaAligned(),
		"the mafia faction is mafia-aligned for win conditions")
}

func TestMafia_RosterRevealedToFactionListsEveryMafia(t *testing.T) {
	// StartGame emits a single MafiaRosterRevealed that names every
	// mafioso and is scoped to the mafia faction (never public, never
	// to town).
	g := game.New()
	_, err := g.Apply(game.CreateGame{
		GameID: "g1", MinPlayers: 5, MaxPlayers: 20, MafiaCount: 2, Seed: 7,
	})
	require.NoError(t, err)
	addPlayers(t, g, "a", "b", "c", "d", "e")
	start, err := g.Apply(game.StartGame{})
	require.NoError(t, err)

	roster, ok := findEvent[game.MafiaRosterRevealed](start)
	require.True(t, ok, "StartGame emits the mafia roster")
	require.Equal(t, "faction", roster.Visibility().Audience,
		"the roster is faction-scoped, never public")
	require.Equal(t, game.FactionMafia, roster.Visibility().Faction)

	// The roster lists exactly the players holding RoleMafia.
	var mafia []game.PlayerID
	for _, p := range g.State().Players() {
		if p.Role() == game.RoleMafia {
			mafia = append(mafia, p.ID())
		}
	}
	require.Len(t, mafia, 2)
	require.ElementsMatch(t, mafia, roster.Members,
		"the roster names every mafioso and no one else")
}

func TestMafia_UnsavedKillVictimDies(t *testing.T) {
	g := fixedRoster(t)
	evts := playNight(t, g, map[game.Role]game.PlayerID{
		game.RoleMafia:  "town1",
		game.RoleDoctor: "town2", // saves the wrong person
	})

	killed, ok := findEvent[game.PlayerKilled](evts)
	require.True(t, ok, "an unsaved mafia kill lands")
	require.Equal(t, game.PlayerID("town1"), killed.PlayerID)
	require.Equal(t, "public", killed.Visibility().Audience,
		"a death is public — the whole town wakes to the news")
	require.False(t, livingByID(g, "town1"), "the victim is dead")
}

func TestMafia_CannotKillFellowMafia(t *testing.T) {
	// A mafioso targeting another mafioso is rejected — the faction
	// never kills its own.
	g := fixedRoster2Mafia(t)
	require.Equal(t, game.RoleMafia, g.State().CurrentNightRole())

	_, err := g.Apply(game.NightAction{Actor: "mafia1", Target: "mafia2"})
	require.ErrorIs(t, err, game.ErrNotYourAction,
		"the mafia cannot target one of their own")
}

func TestMafia_OneKillPerNightAcrossTheFaction(t *testing.T) {
	// The kill is a faction-collective action: whichever mafioso submits
	// first decides the target and closes the act window for the whole
	// faction. A second mafioso's submission is rejected as wrong-turn
	// (the window is now in ponder).
	g := fixedRoster2Mafia(t)

	_, err := g.Apply(game.NightAction{Actor: "mafia1", Target: "town1"})
	require.NoError(t, err, "the first mafioso locks the kill")

	_, err = g.Apply(game.NightAction{Actor: "mafia2", Target: "det"})
	require.ErrorIs(t, err, game.ErrNotYourTurn,
		"only one mafia kill per night — the second submission is too late")
}

func TestMafia_ReachesParityAndWins(t *testing.T) {
	// 5 players, 2 mafia: one unsaved kill drops the town from 3 to 2,
	// matching the 2 mafia-aligned, so the mafia win immediately at
	// night resolution.
	g := fixedRoster2Mafia(t)
	evts := playNight(t, g, map[game.Role]game.PlayerID{
		game.RoleMafia: "town1", // unsaved (doctor idles)
	})

	ge, ok := findEvent[game.GameEnded](evts)
	require.True(t, ok, "the kill brings the mafia to parity")
	require.Equal(t, game.FactionMafia, ge.Winner)
	require.Equal(t, game.PhaseEnded, g.State().Phase())
}

func TestMafia_CannotBeKilledAtNight(t *testing.T) {
	// There is no night action that targets the mafia — the only way a
	// mafioso dies is a daytime lynch. Here the detective and doctor act
	// but neither can remove a mafioso, and the mafia survive the night.
	g := fixedRoster(t)
	playNight(t, g, map[game.Role]game.PlayerID{
		game.RoleMafia:     "town1",
		game.RoleDetective: "mafia1", // investigate, not kill
		game.RoleDoctor:    "town2",
	})
	require.True(t, livingByID(g, "mafia1"),
		"no night action can kill the mafia — only a lynch can")
}

// TestMafia_TurnIsNeverPhantom pins the invariant documented on
// beginNextNightTurn: the mafia's night turn is never phantom. The
// reasoning is that checkWin ends the game the instant living mafia
// reaches zero, so the engine never begins a night with no living
// mafia. This guards that reasoning against a future change to the win
// conditions that would silently let a phantom mafia turn slip through
// (narrating "Mafia, wake up" to a room with no mafia to act).
func TestMafia_TurnIsNeverPhantom(t *testing.T) {
	g := game.New()
	_, err := g.Apply(game.CreateGame{
		GameID: "g1", MinPlayers: 5, MaxPlayers: 20, MafiaCount: 1, Seed: 7,
	})
	require.NoError(t, err)
	addPlayers(t, g, "a", "b", "c", "d", "e")
	_, err = g.Apply(game.StartGame{})
	require.NoError(t, err)
	_, err = g.Apply(game.BeginNight{})
	require.NoError(t, err)

	// Opening → first role's narrate. Mafia is always first in the
	// canonical queue, and the game just started, so a mafia is alive.
	evts := advancePhase(t, g)
	require.Equal(t, game.RoleMafia, g.State().CurrentNightRole())

	nn, ok := findNightSub(evts, game.NightSubNarrate)
	require.True(t, ok, "opening should advance into the mafia's narrate")
	require.Equal(t, game.RoleMafia, nn.Role)
	require.False(t, nn.Phantom,
		"mafia narrate must never be phantom: a live game always has a living mafia")
	require.True(t, g.State().HasLivingRole(game.RoleMafia))

	// And the act window opens (not the phantom-substitute ponder).
	advancePhase(t, g) // narrate → act
	require.Equal(t, game.NightSubAct, g.State().CurrentNightSubPhase(),
		"living mafia gets a real act window, not a phantom ponder")
}
