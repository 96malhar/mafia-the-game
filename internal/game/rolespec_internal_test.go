package game

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// These are INTERNAL tests (package game) — they assert invariants of
// the role registry that are not observable through the public API.
// Adding a new role should make exactly one of these fail until the
// registry entry is added, which is the point.

func TestRegistry_EveryRoleHasASpec(t *testing.T) {
	for _, r := range allRoles() {
		spec, ok := roleSpecs[r]
		require.True(t, ok, "role %q has no entry in roleSpecs", r)

		// Spec's Faction must match the standalone Role.Faction() that
		// the rest of the codebase consults. If these ever diverge, a
		// real bug is imminent.
		require.Equal(t, r.Faction(), spec.Faction,
			"roleSpecs[%q].Faction (%q) disagrees with %q.Faction() (%q)",
			r, spec.Faction, r, r.Faction())
	}
}

func TestRegistry_AllRolesAreValid(t *testing.T) {
	// The Role.Valid() switch and allRoles() must stay in lock-step.
	for _, r := range allRoles() {
		require.True(t, r.Valid(), "allRoles() lists %q but Role.Valid() rejects it", r)
	}
}

func TestRegistry_AllRolesAppearInAllRoles(t *testing.T) {
	// Inverse direction: every role that has a registry entry must
	// also appear in allRoles(). This catches the "added to registry
	// but forgot to update allRoles()" mistake.
	listed := make(map[Role]bool, len(allRoles()))
	for _, r := range allRoles() {
		listed[r] = true
	}
	for r := range roleSpecs {
		require.True(t, listed[r],
			"role %q is in roleSpecs but missing from allRoles()", r)
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
