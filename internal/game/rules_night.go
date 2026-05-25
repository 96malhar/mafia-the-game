package game

// applyNightAction records one role-specific night action.
//
// Validation:
//   - PhaseNight only.
//   - Actor and (non-empty) Target must be known players and alive.
//   - Actor must be a role that has a night action
//     (mafia, doctor, detective). Villagers and unknown roles are
//     rejected with ErrNotYourAction.
//   - Detective cannot investigate self (ErrSelfTarget). The doctor on
//     night 1 cannot self-save either, but that's a "first night"
//     special case we handle here by disallowing self-save on day 0;
//     later nights, the doctor MAY self-save.
//   - Each actor commits once per night (ErrAlreadyActed); changes are
//     not allowed (unlike day votes).
//   - Mafia cannot target their own faction (ErrNotYourAction is the
//     closest sentinel — a mafia targeting another mafia is treated as
//     an illegal action, same as a villager calling NightAction).
//
// On success this emits a single NightActionRecorded scoped to the
// actor's faction. The actual kill/save/investigate effects are NOT
// emitted here — they are resolved together when AdvancePhase ends
// Night, so the events arrive in a stable, deterministic order.
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

	// Role must be capable of acting at night.
	switch actor.role {
	case RoleMafia, RoleDoctor, RoleDetective:
		// ok
	default:
		return nil, ErrNotYourAction
	}

	// Target validation.
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

	// Role-specific target rules.
	switch actor.role {
	case RoleMafia:
		// Mafia cannot kill another mafia.
		if target.role.Faction() == FactionMafia {
			return nil, ErrNotYourAction
		}
	case RoleDetective:
		if c.Actor == c.Target {
			return nil, ErrSelfTarget
		}
	case RoleDoctor:
		// On the first night (day 0), the doctor may not self-save.
		// On subsequent nights, self-save is allowed.
		if c.Actor == c.Target && g.state.day == 0 {
			return nil, ErrSelfTarget
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

// resolveNight computes the effects of all submitted night actions and
// returns the events to append to the log. Called by AdvancePhase when
// leaving PhaseNight.
//
// Resolution order (deterministic):
//  1. Mafia kill target is identified. If multiple mafia voted, the
//     last-submitted action wins. (V1 simplification — proper mafia-
//     internal consensus is a future enhancement.)
//  2. Doctor's save target is identified.
//  3. If doctor saved the kill target, emit PlayerSaved (private to
//     doctor) and no kill.
//  4. Otherwise emit PlayerKilled (public) and mark target dead.
//  5. Detective result is emitted last (private to detective).
//
// pendingNight is cleared regardless of outcome.
func (g *Game) resolveNight() []Event {
	var events []Event
	var killTarget, doctorTarget, detective, detectiveTarget PlayerID
	var hasKill, hasSave, hasInvestigate bool

	// Snapshot decisions. Iterate players (deterministic order) so we
	// can resolve duplicates (multiple mafia actors) by latest action.
	for _, p := range g.state.players {
		target, ok := g.state.pendingNight[p.id]
		if !ok {
			continue
		}
		switch p.role {
		case RoleMafia:
			killTarget = target
			hasKill = true
		case RoleDoctor:
			doctorTarget = target
			hasSave = true
		case RoleDetective:
			detective = p.id
			detectiveTarget = target
			hasInvestigate = true
		}
	}

	if hasKill {
		if hasSave && doctorTarget == killTarget {
			// Doctor saved the kill target. Private notification to
			// the doctor so the village can't deduce the role.
			var doctorID PlayerID
			for _, p := range g.state.players {
				if p.role == RoleDoctor && p.alive {
					doctorID = p.id
					break
				}
			}
			events = append(events, PlayerSaved{
				PlayerID: killTarget,
				Doctor:   doctorID,
			})
		} else {
			// Apply the kill.
			if tp, ok := g.state.findPlayer(killTarget); ok {
				tp.alive = false
			}
			events = append(events, PlayerKilled{PlayerID: killTarget})
		}
	}

	if hasInvestigate {
		var isMafia bool
		if tp, ok := g.state.findPlayer(detectiveTarget); ok {
			isMafia = tp.role.Faction() == FactionMafia
		}
		events = append(events, DetectiveResult{
			Detective: detective,
			Target:    detectiveTarget,
			IsMafia:   isMafia,
		})
	}

	g.state.pendingNight = nil
	return events
}
