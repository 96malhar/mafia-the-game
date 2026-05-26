package game

// applyNightAction records one role-specific night action AND advances
// the turn machinery.
//
// This handler is a thin wrapper over the role registry in rolespec.go:
// generic structural checks live here, and role-specific validation
// lives in each role's NightAction.Validate hook.
//
// Generic validation:
//   - PhaseNight only.
//   - Actor and Target must be known and alive.
//   - Actor's role must equal currentNightRole — the strict turn-order
//     gate that makes the game playable in person ("Mafia, wake up…
//     now Detective"). The error is ErrNotYourTurn (distinct from
//     ErrNotYourAction so the UI can tell "wrong role" from "wrong
//     time").
//   - Actor must be a role with a NightAction in the registry.
//   - For the mafia turn the "actor" may be any living mafia (faction-
//     collective); first valid submission locks the kill target and
//     ends the turn for the whole faction.
//   - Each actor commits once per night (ErrAlreadyActed).
//
// Role-specific validation is delegated to spec.NightAction.Validate.
//
// Emitted events (atomic batch):
//
//	NightActionRecorded{actor, target, faction}    — faction-only
//	NightTurnEnded{role: currentNightRole}         — public
//	NightTurnStarted{role: nextRole}               — public, if queue non-empty
//
// If the queue is now empty, the room will follow up with an
// AdvancePhase to resolve the night.
func (g *Game) applyNightAction(c NightAction) ([]Event, error) {
	if g.state.id == "" {
		return nil, ErrWrongPhase
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

	// First valid submission ends the turn. For mafia (faction-
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
	// mafia member"). We emit it BEFORE NightTurnEnded so the modal
	// pops the moment the action is recorded — much better UX than
	// waiting for the whole night to resolve. The detective's
	// reveal-phase Apply is now a no-op (see rolespec.go); the
	// information is purely role-based (target.role.Faction()), so
	// it doesn't depend on the resolve step at all.
	if actor.role == RoleDetective {
		events = append(events, DetectiveResult{
			Detective: actor.id,
			Target:    target.id,
			IsMafia:   target.role.Faction() == FactionMafia,
		})
	}

	events = append(events, NightTurnEnded{Role: g.state.currentNightRole})
	g.state.currentNightRole = ""
	g.state.nightTurnDeadlineMillis = 0

	// After a detective action we INTENTIONALLY do not start the next
	// turn here. The engine state has currentNightRole="" with the
	// queue still populated; the room layer schedules a brief pause
	// (so the detective can read their result modal) and then sends
	// AdvancePhase to pop the next role. Tests that drive the engine
	// directly must do the same — see playNight in rules_phase_test.go.
	if actor.role == RoleDetective {
		return events, nil
	}

	// If the queue still has roles to act, pop the next one. Otherwise
	// — this was the last role's turn — resolve the night now so the
	// behaviour matches the "final skip resolves the night" case. This
	// removes a footgun where a caller would have to know whether the
	// last turn was an action vs a timeout.
	if next := g.beginNextNightTurn(); len(next) > 0 {
		events = append(events, next...)
		return events, nil
	}
	events = append(events, g.resolveAndExitNight()...)
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
		tp, ok := g.state.findPlayer(target)
		if !ok {
			continue
		}
		spec.NightAction.Apply(ctx, actor, tp)
	}
}
