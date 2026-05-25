package game

// applyNightAction records one role-specific night action.
//
// This handler is now a thin wrapper over the role registry in
// rolespec.go: generic structural checks live here, and role-specific
// validation lives in each role's NightAction.Validate hook.
//
// Generic validation:
//   - PhaseNight only.
//   - Actor and Target must be known and alive.
//   - Actor must be a role with a NightAction in the registry.
//     (Villagers, and any future role lacking a night action, are
//     rejected with ErrNotYourAction.)
//   - Each actor commits once per night (ErrAlreadyActed).
//
// Role-specific validation is delegated to spec.NightAction.Validate
// (e.g., "mafia cannot kill mafia", "doctor cannot self-save on n1",
// "detective cannot self-investigate").
//
// On success this emits a single NightActionRecorded scoped to the
// actor's faction. Effects (kill/save/info) are emitted later by
// resolveNight when AdvancePhase ends Night.
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
		return nil, ErrNotYourAction
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

	return []Event{NightActionRecorded{
		Actor:   c.Actor,
		Target:  c.Target,
		Faction: actor.role.Faction(),
	}}, nil
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
