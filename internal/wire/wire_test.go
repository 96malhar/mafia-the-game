package wire_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/malhar/mafia-the-game/internal/game"
	"github.com/malhar/mafia-the-game/internal/wire"
)

// TestPhaseStringsMatchEngine guards against drift between the wire
// package's stringified phase tags and the engine's game.Phase
// constants. If someone renames a Phase value in the engine, this
// test fails before the rename ships.
func TestPhaseStringsMatchEngine(t *testing.T) {
	cases := []struct {
		name    string
		wireStr string
		engine  game.Phase
	}{
		{"lobby", wire.PhaseLobby, game.PhaseLobby},
		{"night", wire.PhaseNight, game.PhaseNight},
		{"day_discussion", wire.PhaseDayDiscussion, game.PhaseDayDiscussion},
		{"day_vote", wire.PhaseDayVote, game.PhaseDayVote},
		{"ended", wire.PhaseEnded, game.PhaseEnded},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, string(tc.engine), tc.wireStr)
		})
	}
}

// TestRoleStringsMatchEngine: same guard for game.Role.
func TestRoleStringsMatchEngine(t *testing.T) {
	cases := []struct {
		name    string
		wireStr string
		engine  game.Role
	}{
		{"villager", wire.RoleVillager, game.RoleVillager},
		{"mafia", wire.RoleMafia, game.RoleMafia},
		{"detective", wire.RoleDetective, game.RoleDetective},
		{"doctor", wire.RoleDoctor, game.RoleDoctor},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, string(tc.engine), tc.wireStr)
		})
	}
}

// TestFactionStringsMatchEngine: same guard for game.Faction.
func TestFactionStringsMatchEngine(t *testing.T) {
	require.Equal(t, string(game.FactionTown), wire.FactionTown)
	require.Equal(t, string(game.FactionMafia), wire.FactionMafia)
}
