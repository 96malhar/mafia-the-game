package game_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/96malhar/mafia-the-game/internal/game"
)

// Graveyard behaviour: once a player dies they spectate the full roster
// for the rest of the game. The engine surfaces this with a
// RosterRevealed event (Graveyard-scoped) emitted whenever the board
// changes in a way the dead should see — a kill, a lynch, or a consort
// promotion — and suppressed when nothing relevant happened. The
// projection-side "only the dead see it" rule lives in
// projection_test.go; these tests pin the engine-side EMISSION.

func TestGraveyard_NightKillRevealsRosterToTheDead(t *testing.T) {
	g := fixedRoster(t)

	evts := playNight(t, g, map[game.Role]game.PlayerID{game.RoleMafia: "town2"})

	rv, ok := findEvent[game.RosterRevealed](evts)
	require.True(t, ok, "a night kill must reveal the roster to the graveyard")
	require.Equal(t, "dead", rv.Visibility().Audience,
		"the roster snapshot is graveyard-scoped, never public")
	require.Len(t, rv.Roles, 5, "the snapshot names every player")
	require.Equal(t, game.RoleMafia, rv.Roles["mafia1"])
	require.Equal(t, game.RoleVillager, rv.Roles["town2"])
}

func TestGraveyard_LynchRevealsRosterToTheDead(t *testing.T) {
	g := fixedRoster(t)
	// Doctor saves the mafia's target so everyone is alive going into
	// the day and the lynch is the only death.
	playNight(t, g, map[game.Role]game.PlayerID{
		game.RoleMafia:  "town1",
		game.RoleDoctor: "town1",
	})

	evts := finalizeLynch(t, g, "town2") // villager lynch, game continues

	rv, ok := findEvent[game.RosterRevealed](evts)
	require.True(t, ok, "a lynch must reveal the roster to the graveyard")
	require.Len(t, rv.Roles, 5)
}

func TestGraveyard_NoRosterWhenBoardUnchanged(t *testing.T) {
	g := fixedRoster(t)

	// A doctor-saved night: nobody dies, so the graveyard's knowledge is
	// unchanged and no snapshot should be emitted (the log isn't padded).
	saved := playNight(t, g, map[game.Role]game.PlayerID{
		game.RoleMafia:  "town1",
		game.RoleDoctor: "town1",
	})
	_, ok := findEvent[game.RosterRevealed](saved)
	require.False(t, ok, "a saved night emits no graveyard roster")

	// A no-lynch day likewise changes nothing.
	_, err := g.Apply(game.OpenVoting{})
	require.NoError(t, err)
	noLynch, err := g.Apply(game.FinalizeVotes{})
	require.NoError(t, err)
	_, ok = findEvent[game.RosterRevealed](noLynch)
	require.False(t, ok, "a no-lynch day emits no graveyard roster")
}

func TestGraveyard_GameEndingBatchEmitsNoRoster(t *testing.T) {
	g := fixedRoster(t)
	playNight(t, g, map[game.Role]game.PlayerID{
		game.RoleMafia:  "town1",
		game.RoleDoctor: "town1",
	})

	// Lynching the lone mafioso ends the game: the terminal batch carries
	// the PUBLIC GameEnded.FinalRoles, so no graveyard-only snapshot is
	// needed (or emitted) on that batch.
	evts := finalizeLynch(t, g, "mafia1")
	_, ended := findEvent[game.GameEnded](evts)
	require.True(t, ended, "lynching the lone mafioso ends the game")
	_, ok := findEvent[game.RosterRevealed](evts)
	require.False(t, ok, "the terminal batch relies on public FinalRoles, not a graveyard roster")
}

// The consort is the one role that changes after StartGame: when the
// cabal is wiped she is promoted to mafia. A player who died BEFORE
// the promotion must still see her CURRENT identity — so the roster
// is re-revealed to the graveyard after the takeover.
func TestGraveyard_ConsortPromotionRefreshesDeadRoster(t *testing.T) {
	g := fixedRosterWithConsort(t)

	// Night 1: the mafia kills town2, who joins the graveyard and sees a
	// roster that still lists the consort as a consort.
	nightEvts := runConsortNightToDay(t, g, map[game.Role]game.PlayerID{game.RoleMafia: "town2"})
	require.False(t, livingByID(g, "town2"))
	rv1, ok := findEvent[game.RosterRevealed](nightEvts)
	require.True(t, ok, "the night kill reveals the roster to the graveyard")
	require.Equal(t, game.RoleConsort, rv1.Roles["consort"],
		"before promotion the dead see the consort as a consort")

	// Day 1: lynch the last mafioso. That wipes the cabal and promotes the
	// consort to mafia; the game continues (she is now the sole killer).
	lynchEvts := finalizeLynch(t, g, "mafia1")
	_, promoted := findEvent[game.ConsortPromoted](lynchEvts)
	require.True(t, promoted, "lynching the last mafioso promotes the consort")

	rv2, ok := findEvent[game.RosterRevealed](lynchEvts)
	require.True(t, ok, "the promotion re-reveals the roster to the graveyard")
	require.Equal(t, game.RoleMafia, rv2.Roles["consort"],
		"after promotion the dead see the consort as mafia")

	// And that refreshed roster actually projects through to the player
	// who died first, back on night 1.
	out := game.Project("town2", lynchEvts, g.State())
	got, ok := findEvent[game.RosterRevealed](out)
	require.True(t, ok, "town2 (dead since night 1) receives the refreshed roster")
	require.Equal(t, game.RoleMafia, got.Roles["consort"])
}
