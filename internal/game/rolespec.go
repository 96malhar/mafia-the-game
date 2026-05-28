package game

import (
	"sort"
)

// This file defines how new roles plug into the engine.
//
// To add a new role:
//  1. Add a new const to role.go (just the string identifier).
//  2. Add an entry to the roleSpecs map below with the role's
//     Faction and (if applicable) NightAction.
//
// That's it. Role.Valid(), Role.Faction(), and allRoles() all read
// from the registry, so a new role automatically participates in
// validation, faction checks, and test coverage without any other
// edits. If you find yourself reaching for a switch on actor.role
// outside this file or role.go, the registry is probably missing an
// extension point — add the hook here.

// nightPhase orders night-action resolution. Roles declare which phase
// their Apply function runs in; resolveNight iterates phases in order.
//
// The split matters because some roles read state set by others. For
// example, the doctor's save must run AFTER kill intents are recorded
// but BEFORE they actually mutate alive status — hence the
// "Schedule" / "Resolve" split below.
type nightPhase int

const (
	// nightPhaseBlock runs first. Roleblockers and similar abilities
	// that cancel other roles' actions live here. (No such role exists
	// in v1; the phase is reserved for future use.)
	nightPhaseBlock nightPhase = iota

	// nightPhaseSchedule is where "I intend to kill X" and "I intend to
	// save X" record their intent into the NightContext. No mutation of
	// player state yet.
	nightPhaseSchedule

	// nightPhaseResolve is where the NightContext computes the net
	// effect of all scheduled intents (one kill, or one save, or
	// nothing) and applies it to player state.
	nightPhaseResolve

	// nightPhaseReveal runs last. Roles that produce private info based
	// on the resolved state (Detective, future Lookout) live here so
	// they see who actually died.
	nightPhaseReveal
)

// nightContext is the scratchpad threaded through every spec's Apply
// during resolveNight. It holds the in-flight resolution state and
// accumulates events to emit.
//
// Specs read/write fields directly — the API is intentionally small and
// concrete rather than a deep abstraction. We can grow it as new role
// shapes demand new fields.
type nightContext struct {
	state *GameState

	// killTarget, if set, is the player the mafia (or a future
	// vigilante) is trying to kill. Last-write wins among multiple
	// killers (V1 simplification).
	killTarget PlayerID
	hasKill    bool

	// saveTarget, if set, is the player the doctor is protecting.
	saveTarget PlayerID
	hasSave    bool

	// savingDoctor is the PlayerID of the doctor who issued the save
	// (used so PlayerSaved can carry the right Visibility). Empty if
	// hasSave is false.
	savingDoctor PlayerID

	// died is set during nightPhaseResolve to the player who actually
	// died this night (empty if nobody died). Reveal-phase roles
	// (detective, etc.) can read this if they care.
	died PlayerID

	// events accumulates events to be appended after resolution.
	events []Event
}

// NightAction describes the per-role hooks for a night-acting role. A
// role without a night action leaves this nil in its RoleSpec.
type nightActionSpec struct {
	// Phase is the resolution phase this action runs in. See the
	// nightPhase constants for semantics.
	Phase nightPhase

	// Validate is called by applyNightAction after generic checks
	// (actor/target exist and are alive, target is not empty) pass. It
	// returns a sentinel error to reject the action, or nil to record
	// it. Validate must be pure — no state mutation.
	Validate func(s *GameState, actor, target *Player) error

	// Apply runs during resolveNight, in Phase order. The actor and
	// target args are the *current* (possibly already-mutated) records;
	// callers MUST NOT mutate state directly — write intent or effects
	// through ctx.
	Apply func(ctx *nightContext, actor, target *Player)
}

// roleSpec is the per-role plugin. Today it carries a Faction (a static
// fact about the role) and an optional NightAction. As we add more
// role-shapes (passive roles, day actions, group voting), this struct
// gains more optional hooks.
//
// What does NOT live here: the wall-clock duration of the role's
// "wake up" / "go to sleep" audio cues. Those used to live as
// Narrate/Sleep function fields on roleSpec, but were moved out to
// internal/room/config.go so that ALL wall-clock timing is owned by
// the room layer in one place. The engine remains timeless; the room
// is the sole arbiter of when sub-phases begin and end.
//
// Trade-off (named explicitly so future readers don't reconsider this
// without context): adding a new role with custom narration timing
// now requires editing BOTH rolespec.go (to register the role) AND
// room/config.go (to register the role's per-day Narrate/Sleep
// duration). The "one file per role" property is broken specifically
// for timing. The benefit is that the room layer is the single
// authority on wall-clock policy.
type roleSpec struct {
	Faction     Faction
	NightAction *nightActionSpec
}

// roleSpecs is the registry. Every value in the Role enum MUST have an
// entry here; a TestEveryRoleHasASpec test enforces this invariant.
var roleSpecs = map[Role]roleSpec{
	RoleVillager: {
		Faction: FactionTown,
		// No NightAction.
	},

	RoleMafia: {
		Faction: FactionMafia,
		// Narrate/Sleep durations live in internal/room/config.go's
		// DefaultNarrate / DefaultSleep. Mafia has a per-day Narrate
		// variant (Day 0 includes the "look around, recognize each
		// other" beat); see DefaultNarrate for the value and the
		// client-coupling comment.
		NightAction: &nightActionSpec{
			Phase: nightPhaseSchedule,
			Validate: func(_ *GameState, _, target *Player) error {
				if target.role == RoleMafia {
					// Mafia cannot kill another mafia.
					return ErrNotYourAction
				}
				return nil
			},
			Apply: func(ctx *nightContext, _, target *Player) {
				// V1: last-write wins among multiple mafia killers.
				ctx.killTarget = target.id
				ctx.hasKill = true
			},
		},
	},

	RoleDoctor: {
		Faction: FactionTown,
		// Narrate/Sleep durations live in internal/room/config.go.
		// Doctor uses the universal defaults (no per-day variant).
		NightAction: &nightActionSpec{
			Phase: nightPhaseSchedule,
			// The doctor can save anyone, including themselves, on
			// any night. The earlier "no self-save on Night 1" rule
			// is intentionally relaxed: the role is meant to be
			// powerful, and forcing a doctor to skip themselves on
			// the first night was a fiddly carve-out that confused
			// new players. Validate is nil because the generic
			// alive-actor / alive-target checks in applyNightAction
			// are enough.
			Validate: nil,
			Apply: func(ctx *nightContext, actor, target *Player) {
				ctx.saveTarget = target.id
				ctx.hasSave = true
				ctx.savingDoctor = actor.id
			},
		},
	},

	RoleDetective: {
		Faction: FactionTown,
		// Narrate/Sleep durations live in internal/room/config.go.
		// Detective uses the universal defaults (no per-day variant).
		NightAction: &nightActionSpec{
			// Detective's result is emitted at action time (see
			// applyNightAction in rules_night.go) for an
			// immediate-feedback UX. The reveal-phase Apply is a
			// no-op: kept for symmetry with other roles, in case
			// future detective abilities need to read resolved
			// state. Phase remains nightPhaseReveal so any future
			// post-resolve logic slots in at the right point.
			Phase: nightPhaseReveal,
			Validate: func(_ *GameState, actor, target *Player) error {
				if actor.id == target.id {
					return ErrSelfTarget
				}
				return nil
			},
			Apply: func(_ *nightContext, _, _ *Player) {
				// no-op (see comment above)
			},
		},
	},
}

// resolvePhase is the implicit nightPhaseResolve handler. It is not a
// role spec because no role "owns" the resolve step — it's the
// engine's reconciliation of scheduled intents into actual player-state
// changes and the public kill/save events.
//
// Doctor saves cancel a kill iff they target the same player. The
// private PlayerSaved is visible only to the doctor (see event.go).
func resolvePhase(ctx *nightContext) {
	if !ctx.hasKill {
		return
	}
	if ctx.hasSave && ctx.saveTarget == ctx.killTarget {
		ctx.events = append(ctx.events, PlayerSaved{
			PlayerID: ctx.killTarget,
			Doctor:   ctx.savingDoctor,
		})
		return
	}
	if tp, ok := ctx.state.findPlayer(ctx.killTarget); ok {
		tp.alive = false
		ctx.died = ctx.killTarget
	}
	ctx.events = append(ctx.events, PlayerKilled{PlayerID: ctx.killTarget})
}

// allRoles returns every role known to the registry, in stable
// (string-sorted) order. The registry is the single source of truth;
// callers that need to iterate all roles should use this rather than
// hand-listing constants. Tests use it to verify registry coverage.
func allRoles() []Role {
	out := make([]Role, 0, len(roleSpecs))
	for r := range roleSpecs {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return string(out[i]) < string(out[j]) })
	return out
}
