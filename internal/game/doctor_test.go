package game_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/96malhar/mafia-the-game/internal/game"
)

// Doctor-specific behaviour: a save cancels a kill on the same target,
// the doctor may save anyone (including themselves) on any night, the
// PlayerSaved acknowledgement is private to the doctor, a save with no
// matching kill is a silent no-op, and protection lasts only one night.

func TestDoctor_FactionIsTown(t *testing.T) {
	require.Equal(t, game.FactionTown, game.RoleDoctor.Faction())
}

func TestDoctor_SaveCancelsKillOnSameTarget(t *testing.T) {
	g := fixedRoster(t)
	evts := playNight(t, g, map[game.Role]game.PlayerID{
		game.RoleMafia:  "town1",
		game.RoleDoctor: "town1", // protect the mafia's target
	})

	_, killed := findEvent[game.PlayerKilled](evts)
	require.False(t, killed, "the save cancels the kill — nobody dies")
	saved, ok := findEvent[game.PlayerSaved](evts)
	require.True(t, ok, "a matching save emits PlayerSaved")
	require.Equal(t, game.PlayerID("town1"), saved.PlayerID)
	require.True(t, livingByID(g, "town1"), "the protected target survives")
}

func TestDoctor_SaveWrongTargetVictimDies(t *testing.T) {
	// Protecting someone other than the mafia's victim doesn't help the
	// victim.
	g := fixedRoster(t)
	evts := playNight(t, g, map[game.Role]game.PlayerID{
		game.RoleMafia:  "town1",
		game.RoleDoctor: "town2",
	})

	killed, ok := findEvent[game.PlayerKilled](evts)
	require.True(t, ok, "a save on the wrong player leaves the kill standing")
	require.Equal(t, game.PlayerID("town1"), killed.PlayerID)
	require.False(t, livingByID(g, "town1"))
}

func TestDoctor_SelfSaveProtectsTheDoctor(t *testing.T) {
	// The doctor may protect themselves on any night, including night 1.
	g := fixedRoster(t)
	evts := playNight(t, g, map[game.Role]game.PlayerID{
		game.RoleMafia:  "doc",
		game.RoleDoctor: "doc", // self-save
	})

	saved, ok := findEvent[game.PlayerSaved](evts)
	require.True(t, ok, "a self-save is legal and cancels the kill")
	require.Equal(t, game.PlayerID("doc"), saved.PlayerID)
	require.True(t, livingByID(g, "doc"), "the self-saved doctor survives")
}

func TestDoctor_SaveWithoutKillEmitsNothing(t *testing.T) {
	// Protecting a player nobody attacked produces neither a save nor a
	// kill event — the doctor's action is silently irrelevant.
	g := fixedRoster(t)
	evts := playNight(t, g, map[game.Role]game.PlayerID{
		game.RoleDoctor: "town1", // mafia idles, so no kill to cancel
	})

	_, saved := findEvent[game.PlayerSaved](evts)
	require.False(t, saved, "no PlayerSaved without a matching kill")
	_, killed := findEvent[game.PlayerKilled](evts)
	require.False(t, killed, "nobody died")
	require.True(t, livingByID(g, "town1"))
}

func TestDoctor_SaveIsPrivateToDoctor(t *testing.T) {
	// The save acknowledgement is visible only to the doctor — the town
	// must not learn that a kill was thwarted.
	g := fixedRoster(t)
	evts := playNight(t, g, map[game.Role]game.PlayerID{
		game.RoleMafia:  "town1",
		game.RoleDoctor: "town1",
	})

	saved, ok := findEvent[game.PlayerSaved](evts)
	require.True(t, ok)
	require.Equal(t, game.PlayerID("doc"), saved.Doctor)
	require.Equal(t, "player", saved.Visibility().Audience)
	require.Equal(t, game.PlayerID("doc"), saved.Visibility().Player,
		"only the doctor learns the save landed")
}

func TestDoctor_ProtectionDoesNotPersistAcrossNights(t *testing.T) {
	// A save protects only the night it was cast. Night 1 the doctor
	// saves town1 from the mafia; night 2 the doctor idles and the same
	// target is killed.
	g := fixedRoster(t)
	evts1 := playNight(t, g, map[game.Role]game.PlayerID{
		game.RoleMafia:  "town1",
		game.RoleDoctor: "town1",
	})
	_, killed1 := findEvent[game.PlayerKilled](evts1)
	require.False(t, killed1, "night 1: the save protects town1")
	require.True(t, livingByID(g, "town1"))

	noLynchDay(t, g)
	beginNightToMafiaAct(t, g)

	evts2 := playNight(t, g, map[game.Role]game.PlayerID{
		game.RoleMafia: "town1", // doctor idles this night
	})
	killed2, ok := findEvent[game.PlayerKilled](evts2)
	require.True(t, ok, "night 2: last night's save is gone, the kill lands")
	require.Equal(t, game.PlayerID("town1"), killed2.PlayerID)
	require.False(t, livingByID(g, "town1"))
}
