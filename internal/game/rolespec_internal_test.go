package game

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// These are INTERNAL tests (package game) — they assert invariants of
// the role registry. With Role.Valid(), Role.Faction(), and
// allRoles() all derived from roleSpecs, the registry is the single
// source of truth; these tests guard against accidental regressions
// to that arrangement (e.g. someone re-introducing a hand-listed
// switch that drifts).

func TestRegistry_RoleConstantsHaveSpecs(t *testing.T) {
	// Every Role const declared in role.go must have an entry in
	// roleSpecs. We hand-enumerate the constants here (the one place
	// it's appropriate) so adding a const without a spec entry is a
	// hard test failure, not a silent "unknown role" at runtime.
	roles := []Role{RoleVillager, RoleMafia, RoleDetective, RoleDoctor, RoleConsort, RoleVigilante}
	for _, r := range roles {
		_, ok := roleSpecs[r]
		require.True(t, ok, "role const %q has no entry in roleSpecs", r)
	}
}

func TestRegistry_FactionMatchesSpec(t *testing.T) {
	// Role.Faction() reads roleSpecs, so this is mostly a tautology
	// today — but the test will catch any future "fast path" in
	// Faction() that hardcodes a value and forgets to update the
	// registry.
	for r, spec := range roleSpecs {
		require.Equal(t, spec.Faction, r.Faction(),
			"roleSpecs[%q].Faction (%q) disagrees with %q.Faction() (%q)",
			r, spec.Faction, r, r.Faction())
	}
}

func TestRegistry_AllRolesAreValid(t *testing.T) {
	for _, r := range allRoles() {
		require.True(t, r.Valid(), "allRoles() lists %q but Role.Valid() rejects it", r)
	}
}

func TestRegistry_NightActionPhasesAreKnown(t *testing.T) {
	known := map[nightPhase]bool{
		nightPhaseBlock:    true,
		nightPhaseSchedule: true,
		nightPhaseResolve:  true,
		nightPhaseReveal:   true,
	}
	for r, spec := range roleSpecs {
		if spec.NightAction == nil {
			continue
		}
		require.True(t, known[spec.NightAction.Phase],
			"role %q has unknown nightPhase %d in NightAction.Phase", r, spec.NightAction.Phase)
	}
}

// Note: narrate/sleep timing assertions used to live here but were
// moved to internal/room (TestConfig_SubPhaseDuration) when
// wall-clock-duration ownership migrated to the room layer. See the
// package comment at the top of rolespec.go and defaultSubPhaseDuration
// in internal/room/config.go for the rationale.
