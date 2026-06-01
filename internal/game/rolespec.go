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

	// killTarget, if set, is the player the mafia is trying to kill.
	// Last-write wins among multiple mafia killers (V1 simplification).
	// The vigilante's kill is tracked separately (vigKillTarget) so the
	// two killers don't clobber each other and so resolvePhase can apply
	// them in order (mafia first; see the vigilante rules there).
	killTarget PlayerID
	hasKill    bool

	// vigKillTarget, if set, is the player the vigilante is shooting
	// tonight. vigilanteActor is the shooter's own id — resolvePhase
	// reads it to check the shooter is still alive after the mafia kill
	// resolves (the mafia kill takes precedence). vigilanteFired records
	// that a vigilante shot was recorded this night, so resolveNight can
	// mark the one-shot bullet spent.
	vigKillTarget  PlayerID
	vigilanteActor PlayerID
	hasVigKill     bool
	vigilanteFired bool

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

	// blocked is the player the Consort targeted tonight (empty if no
	// consort acted). hasBlock distinguishes "blocked nobody" from a
	// block on the player whose id happens to be the zero value. A
	// block nullifies the blocked player's night action UNLESS they are
	// mafia (blocking a mafioso is allowed but has no effect — the kill
	// is a faction action). Exactly one player can be blocked per night
	// (a single consort), so no per-actor de-dup flag is needed.
	blocked  PlayerID
	hasBlock bool

	// events accumulates events to be appended after resolution.
	events []Event
}

// NightAction describes the per-role hooks for a night-acting role. A
// role without a night action leaves this nil in its RoleSpec.
type nightActionSpec struct {
	// Phase is the resolution phase this action runs in. See the
	// nightPhase constants for semantics.
	Phase nightPhase

	// AllowPass lets a holder of this role explicitly DECLINE to act via
	// the NightPass command, ending their act window early without
	// recording anything. Only roles for whom "not acting" is a
	// meaningful, resource-preserving choice should set this — today just
	// the Vigilante (holding fire keeps his one bullet). When false (the
	// default) NightPass is rejected with ErrNotYourAction, so a role
	// that simply lets its timer run never grows a skip affordance.
	AllowPass bool

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
// "wake up" / "go to sleep" audio cues. Those are owned by
// internal/room/config.go (defaultSubPhaseDuration) so that ALL
// wall-clock timing lives in the room layer in one place. The engine
// remains timeless; the room is the sole arbiter of when sub-phases
// begin and end.
//
// Trade-off (named explicitly so future readers don't reconsider this
// without context): adding a new role with custom narration timing
// now requires editing BOTH rolespec.go (to register the role) AND
// room/config.go's defaultSubPhaseDuration (to give the role's
// narrate/sleep duration). The "one file per role" property is broken
// specifically for timing. The benefit is that the room layer is the
// single authority on wall-clock policy.
type roleSpec struct {
	Faction     Faction
	NightAction *nightActionSpec
}

// rejectSelfTarget returns ErrSelfTarget when a role is trying to act on
// itself. Several roles (consort, detective, vigilante) forbid this; the
// shared helper keeps the intent obvious as new roles are added.
func rejectSelfTarget(actor, target *Player) error {
	if actor.id == target.id {
		return ErrSelfTarget
	}
	return nil
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

	RoleConsort: {
		// Mafia-aligned for winning, but her OWN faction so she is
		// isolated from mafia coordination (FactionOnly(FactionMafia)
		// events never reach FactionConsort, and the mafia roster is
		// collected by faction == FactionMafia, excluding her).
		Faction: FactionConsort,
		// The block resolves in nightPhaseBlock — strictly BEFORE the
		// schedule (kill/save) and reveal phases — so a blocked role's
		// action is nullified before it would otherwise run.
		NightAction: &nightActionSpec{
			Phase: nightPhaseBlock,
			Validate: func(_ *GameState, actor, target *Player) error {
				// A consort blocking herself is meaningless. Any other
				// living target is allowed — including a mafioso (which
				// simply has no effect: resolveNight never skips a mafia
				// kill, so she just wastes her night).
				return rejectSelfTarget(actor, target)
			},
			Apply: func(ctx *nightContext, _, target *Player) {
				ctx.blocked = target.id
				ctx.hasBlock = true
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
				return rejectSelfTarget(actor, target)
			},
			Apply: func(_ *nightContext, _, _ *Player) {
				// no-op (see comment above)
			},
		},
	},

	RoleVigilante: {
		// Town-aligned: wins with the town, reads as "not mafia" to the
		// detective. Narrate/Sleep durations live in internal/room/config.go
		// (universal defaults — the vigilante's cue length matches the
		// generic "<role>, wake up. Choose someone to ..." template).
		Faction: FactionTown,
		NightAction: &nightActionSpec{
			// The vigilante records a kill intent during Schedule (like
			// the mafia and doctor); resolvePhase reconciles it AFTER the
			// mafia kill so the mafia takes precedence. The bullet is a
			// one-shot for the whole game (state.vigilanteShotUsed).
			Phase: nightPhaseSchedule,
			// The vigilante may hold fire: declining to shoot keeps his
			// single bullet for a later night, so he gets an explicit
			// NightPass (the client's "Hold fire" button) that ends his
			// turn early instead of burning the full act window.
			AllowPass: true,
			Validate: func(s *GameState, actor, target *Player) error {
				// Shooting yourself makes no sense.
				if err := rejectSelfTarget(actor, target); err != nil {
					return err
				}
				if s.vigilanteShotUsed {
					// The single bullet has already been fired on an
					// earlier night. A spent vigilante's turn is routed to
					// a phantom ponder (no act window — see
					// roleTurnIsPhantom), so a correct flow never reaches
					// here; this is the defense-in-depth backstop that
					// rejects a client submitting anyway. Reuse
					// ErrAlreadyActed so the wire/UI treat it the same as
					// a double-submit.
					return ErrAlreadyActed
				}
				return nil
			},
			Apply: func(ctx *nightContext, actor, target *Player) {
				ctx.vigKillTarget = target.id
				ctx.vigilanteActor = actor.id
				ctx.hasVigKill = true
				ctx.vigilanteFired = true
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
//
// Resolution is ORDER-DEPENDENT because of the vigilante:
//
//  1. The mafia kill resolves FIRST. A doctor save on the same target
//     cancels it.
//  2. The vigilante shot resolves SECOND, and only if the vigilante is
//     still alive (the mafia kill above may have killed him — rule 2,
//     mafia precedence). A doctor save on the vigilante's target cancels
//     the shot (rule 4) but the bullet is still spent (recorded in
//     resolveNight via ctx.vigilanteFired).
//
// A single doctor save protects exactly one player, so the mafia and
// vigilante branches never both fire a save on the same target.
func resolvePhase(ctx *nightContext) {
	// 1. Mafia kill takes precedence and resolves first.
	if ctx.hasKill {
		ctx.resolveHit(ctx.killTarget)
	}

	// 2. Vigilante shot — only if the shooter survived step 1.
	if ctx.hasVigKill {
		if shooter, ok := ctx.state.findPlayer(ctx.vigilanteActor); ok && shooter.alive {
			ctx.resolveHit(ctx.vigKillTarget)
		}
	}
}

// resolveHit applies a single attempted kill on target: a matching doctor
// save cancels it (emitting the private PlayerSaved), otherwise the target
// dies (emitting the public PlayerKilled). The alive guard also de-dups
// the case where two killers (mafia + vigilante) hit the same player —
// only one death lands.
func (ctx *nightContext) resolveHit(target PlayerID) {
	if ctx.hasSave && ctx.saveTarget == target {
		ctx.events = append(ctx.events, PlayerSaved{
			PlayerID: target,
			Doctor:   ctx.savingDoctor,
		})
		return
	}
	if tp, ok := ctx.state.findPlayer(target); ok && tp.alive {
		tp.alive = false
		ctx.died = target
		ctx.events = append(ctx.events, PlayerKilled{PlayerID: target})
	}
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
