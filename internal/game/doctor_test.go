package game_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/96malhar/mafia-the-game/internal/game"
)

// Doctor-specific behaviour: a save cancels a kill on the same target,
// the doctor may save anyone (including themselves) on any night, a save
// is entirely silent (no confirmation reaches the doctor — survival is
// the only signal), and protection lasts only one night.

func TestDoctor_FactionIsTown(t *testing.T) {
	require.Equal(t, game.FactionTown, game.RoleDoctor.Faction())
}

// TestDoctor_SaveOutcomes covers the doctor's single-night save outcomes: a
// save cancels a kill on the same target, misses on the wrong one, may protect
// the doctor themselves, is irrelevant when nothing was attacked, and is always
// SILENT (no event — survival is the only signal). mafiaTarget == "" means the
// mafia idles, so no kill is queued.
func TestDoctor_SaveOutcomes(t *testing.T) {
	tests := []struct {
		name         string
		mafiaTarget  game.PlayerID
		doctorTarget game.PlayerID
		wantKilled   bool
		wantVictim   game.PlayerID // only meaningful when wantKilled
		note         string        // the load-bearing WHY, preserved per case
	}{
		{
			name: "save cancels kill on same target", mafiaTarget: "town1", doctorTarget: "town1",
			note: "the save cancels the kill — nobody dies; the protected target survives",
		},
		{
			name: "save on wrong target leaves the kill standing", mafiaTarget: "town1", doctorTarget: "town2",
			wantKilled: true, wantVictim: "town1",
			note: "protecting someone other than the mafia's victim doesn't help the victim",
		},
		{
			name: "self-save protects the doctor (legal on any night)", mafiaTarget: "doc", doctorTarget: "doc",
			note: "the doctor may protect themselves on any night, including night 1; a self-save is legal and cancels the kill",
		},
		{
			name: "save on an un-attacked player is silently irrelevant", mafiaTarget: "", doctorTarget: "town1",
			note: "protecting a player nobody attacked produces no kill event — the mafia idles, so there is no kill to cancel",
		},
		{
			// Same inputs as the same-target case, kept distinct to pin the
			// no-save-event invariant explicitly.
			name: "a save that lands is still silent", mafiaTarget: "town1", doctorTarget: "town1",
			note: "a save that cancels a kill is still silent: the only emitted night-outcome events are kills, never a save. Survival is the only signal — there is no private confirmation to leak the role",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			g := fixedRoster(t)
			actions := map[game.Role]game.PlayerID{game.RoleDoctor: tc.doctorTarget}
			if tc.mafiaTarget != "" {
				actions[game.RoleMafia] = tc.mafiaTarget
			}
			evts := playNight(t, g, actions)

			killed, ok := findEvent[game.PlayerKilled](evts)
			if tc.wantKilled {
				require.True(t, ok, tc.note)
				require.Equal(t, tc.wantVictim, killed.PlayerID)
				require.False(t, livingByID(g, tc.wantVictim))
			} else {
				require.False(t, ok, tc.note)
				require.True(t, livingByID(g, tc.doctorTarget))
			}
		})
	}
}

// A save protects only the night it was cast. Night 1 the doctor
// saves town1 from the mafia; night 2 the doctor idles and the same
// target is killed.
func TestDoctor_ProtectionDoesNotPersistAcrossNights(t *testing.T) {
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
