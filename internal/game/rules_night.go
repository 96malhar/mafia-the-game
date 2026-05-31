package game

// applyNightAction records one role-specific night action AND drives
// the act → ponder edge of the per-role state machine. All other
// edges are driven by AdvancePhase from the room's wall-clock timer
// (see advanceNightSubPhase).
//
// This handler is a thin wrapper over the role registry in rolespec.go:
// generic structural checks live here, and role-specific validation
// lives in each role's NightAction.Validate hook.
//
// Generic validation:
//   - PhaseNight only AND currentNightSubPhase == NightSubAct (i.e.
//     the actor's window is open). Submissions during narrate /
//     ponder / sleep / settle are rejected with ErrNotYourTurn so
//     the wire and UI can keep "wrong time" distinct from "wrong
//     role" (ErrNotYourAction).
//   - Actor and Target must be known and alive.
//   - Actor's role must equal currentNightRole — the strict turn-order
//     gate that makes the game playable in person.
//   - Actor must be a role with a NightAction in the registry
//     (villagers are rejected with ErrNotYourAction).
//   - For the mafia turn the "actor" may be any living mafia (faction-
//     collective); first valid submission locks the kill target and
//     ends the act window for the whole faction.
//   - Each actor commits once per night (ErrAlreadyActed).
//
// Role-specific validation is delegated to spec.NightAction.Validate.
//
// Emitted events (atomic batch on success):
//
//	NightActionRecorded{actor, target, faction}    — faction-only
//	DetectiveResult{...}                           — detective only, private
//	NightPonderStarted{role, day, phantom: false}  — public
//
// After ponder elapses (room's timer), AdvancePhase drives ponder →
// sleep → settle → next role. The detective's read-modal pause is
// folded into the ponder duration (sized by the room's per-role
// Ponder function) rather than a separate timer hook.
func (g *Game) applyNightAction(c NightAction) ([]Event, error) {
	if g.state.id == "" {
		return nil, ErrWrongPhase
	}
	if g.state.phase == PhaseEnded {
		return nil, ErrGameEnded
	}
	if g.state.phase != PhaseNight {
		return nil, ErrWrongPhase
	}

	actor, ok := g.state.findPlayer(c.Actor)
	if !ok {
		return nil, ErrUnknownPlayer
	}
	if !actor.alive {
		return nil, ErrPlayerDead
	}

	spec, ok := roleSpecs[actor.role]
	if !ok || spec.NightAction == nil {
		// Roles with no night action (villager) are categorically
		// rejected regardless of whose turn it is. This keeps the
		// "you have no night action" error stable for UI gating.
		return nil, ErrNotYourAction
	}

	// Strict turn-order gate. The current role MUST match the actor's
	// role; mafia is the faction-collective case (any mafia can submit
	// when it's the mafia's turn).
	if actor.role != g.state.currentNightRole {
		return nil, ErrNotYourTurn
	}
	// And we must be in the act sub-phase. Submissions during narrate
	// / ponder / sleep / settle (or between turns) collapse onto the
	// same "wrong time" error.
	if g.state.currentNightSubPhase != NightSubAct {
		return nil, ErrNotYourTurn
	}

	// Roleblock: a blocked NON-mafia actor cannot act at all. The Consort
	// acts earlier in the queue, so her block is already recorded when
	// the detective/doctor window opens. A correct client never reaches
	// here — it's told it's blocked at the start of the turn (the
	// act-window Blocked event) and hides the target picker — so this
	// rejects a client that bypasses the UI. Mafia are immune (the
	// faction kill ignores the block) and fall through unaffected.
	if actor.role != RoleMafia && g.state.isNightBlocked(actor.id) {
		return nil, ErrBlocked
	}

	if c.Target == "" {
		return nil, ErrUnknownPlayer
	}
	target, ok := g.state.findPlayer(c.Target)
	if !ok {
		return nil, ErrUnknownPlayer
	}
	if !target.alive {
		return nil, ErrPlayerDead
	}

	if spec.NightAction.Validate != nil {
		if err := spec.NightAction.Validate(g.state, actor, target); err != nil {
			return nil, err
		}
	}

	if g.state.pendingNight == nil {
		g.state.pendingNight = make(map[PlayerID]PlayerID)
	}
	if _, already := g.state.pendingNight[c.Actor]; already {
		return nil, ErrAlreadyActed
	}
	g.state.pendingNight[c.Actor] = c.Target

	// First valid submission ends the act window. For mafia (faction-
	// collective), this is intentional: only one kill per night,
	// decided by whichever mafia submits first.
	events := []Event{
		NightActionRecorded{
			Actor:   c.Actor,
			Target:  c.Target,
			Faction: actor.role.Faction(),
		},
	}

	// Detective gets immediate private feedback ("X IS / is NOT a
	// mafia member"). We emit it BEFORE the ponder transition so the
	// modal pops the moment the action is recorded. The detective's
	// reveal-phase Apply is a no-op (see rolespec.go); the information
	// is purely role-based (target.role.Faction()), so it doesn't
	// depend on the resolve step at all.
	if actor.role == RoleDetective {
		// A blocked detective never reaches this point — applyNightAction
		// rejects their submission with ErrBlocked above — so we always
		// have a genuine result to deliver here.
		//
		// IsMafia checks the STRICT mafia role, not mafia-alignment: an
		// un-promoted Consort (role RoleConsort, faction FactionConsort)
		// reads as NOT mafia, so investigating her is misleading by
		// design. Only once she's promoted to RoleMafia (the cabal was
		// wiped out) does she read as mafia.
		events = append(events, DetectiveResult{
			Detective: actor.id,
			Target:    target.id,
			IsMafia:   target.role.Faction() == FactionMafia,
		})
	}

	// Drive act → ponder. Both submit (here) and timeout (AdvancePhase
	// during the act window) pass through ponder, so the audio cadence
	// and sub-phase sequence are uniform — observers can't tell a real
	// submission from a timed-out turn.
	events = append(events, g.enterNightSubPhase(NightSubPonder)...)
	return events, nil
}

// resolveNight runs each scheduled night action through its role's
// Apply hook, in nightPhase order, with an implicit resolve step
// (kill vs save reconciliation) between Schedule and Reveal.
//
// This replaces the old hand-rolled switch on role; new roles plug in
// purely via the registry in rolespec.go.
//
// The iteration order within a phase is the players' join order, which
// is stable and deterministic.
func (g *Game) resolveNight() []Event {
	ctx := &nightContext{state: g.state}

	// Block: roleblockers nullify other actions. (No role in v1; the
	// iteration is here so a future spec slots in without touching
	// this function.)
	g.runNightPhase(ctx, nightPhaseBlock)

	// Schedule: declare intent without mutating state (mafia kill,
	// doctor save).
	g.runNightPhase(ctx, nightPhaseSchedule)

	// Resolve: reconcile kill vs save into at most one mutation.
	resolvePhase(ctx)

	// Reveal: info roles read the resolved state (Detective, future
	// Lookout).
	g.runNightPhase(ctx, nightPhaseReveal)

	g.state.pendingNight = nil
	return ctx.events
}

// runNightPhase invokes Apply on every player whose role's spec
// declares a NightAction in the given phase AND has a pending target
// for tonight. Iteration order is players' join order for determinism.
func (g *Game) runNightPhase(ctx *nightContext, phase nightPhase) {
	for i := range g.state.players {
		actor := &g.state.players[i]
		target, ok := g.state.pendingNight[actor.id]
		if !ok {
			continue
		}
		spec, ok := roleSpecs[actor.role]
		if !ok || spec.NightAction == nil {
			continue
		}
		if spec.NightAction.Phase != phase {
			continue
		}
		// Roleblock backstop (defense-in-depth). applyNightAction now
		// rejects a blocked non-mafia submission with ErrBlocked, so a
		// blocked actor is never in pendingNight and this branch is
		// unreachable in normal flow. It stays as a safety net: if that
		// check is ever bypassed, the action is still nullified here (no
		// save scheduled, no reveal run). Mafia are immune by design
		// (blocking a mafioso is a wasted night — the kill is a faction
		// action), and the consort is never her own target.
		if ctx.hasBlock && actor.id == ctx.blocked && actor.role != RoleMafia {
			continue
		}
		tp, ok := g.state.findPlayer(target)
		if !ok {
			continue
		}
		spec.NightAction.Apply(ctx, actor, tp)
	}
}
