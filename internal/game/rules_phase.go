package game

// applyAdvancePhase elapses the current night SUB-phase (PhaseNight
// only). It is an INTERNAL command, invoked by the room's wall-clock
// timer when the active sub-phase's deadline is reached. Daytime
// pacing is NOT driven by AdvancePhase: hosts use BeginNight /
// OpenVoting / ClearVotes / FinalizeVotes for those transitions.
//
// Each AdvancePhase advances exactly one sub-phase boundary. The five-
// step state machine per role turn is:
//
//	narrate ─▶ act ─[submit]─▶ ponder(short) ─▶ sleep ─▶ settle ─▶ next role
//	narrate ─▶ act ─[timer]──▶                  sleep ─▶ settle ─▶ next role
//	narrate ────────────────▶ ponder(random)──▶ sleep ─▶ settle ─▶ next role   (phantom)
//
// Submission (NightAction) drives the act→ponder edge directly; every
// other edge is driven by AdvancePhase. After the last role's settle,
// the engine runs resolveNight and transitions to PhaseDayDiscussion
// (or PhaseEnded if a faction won).
//
// Transition summary by current sub-phase:
//
// Every Night sub-phase transition emits a NightSubPhaseStarted whose
// Sub field names the sub-phase below (e.g. Sub=act, Sub=sleep):
//
//	Lobby / DayDiscussion / DayVote -> ErrWrongPhase
//	Ended                           -> ErrGameEnded
//	Night, narrate                  -> Sub=act (real) OR Sub=ponder (phantom)
//	Night, act    (timeout)         -> Sub=sleep
//	Night, ponder                   -> Sub=sleep
//	Night, sleep                    -> Sub=settle
//	Night, settle (midQueue)        -> Sub=narrate (next role)
//	Night, settle (lastRole)        -> resolveNight + PhaseChanged
//
// AdvancePhase received during NightSubAct counts as the timeout
// branch: no submission was recorded for this turn, so we skip
// ponder and go straight to sleep.
func (g *Game) applyAdvancePhase(_ AdvancePhase) ([]Event, error) {
	if g.state.id == "" {
		return nil, ErrWrongPhase
	}
	switch g.state.phase {
	case PhaseEnded:
		return nil, ErrGameEnded
	case PhaseNight:
		return g.advanceNightSubPhase(), nil
	default:
		// Day phases are host-driven; AdvancePhase is invalid here.
		return nil, ErrWrongPhase
	}
}

// advanceNightSubPhase implements one tick of the per-role state
// machine described on applyAdvancePhase. It assumes:
//   - g.state.phase == PhaseNight, AND
//   - g.state.currentNightRole and currentNightSubPhase are non-zero
//     (i.e. we're mid-turn).
//
// If currentNightSubPhase is empty (shouldn't happen in normal flow)
// the function returns no events, leaving state untouched.
func (g *Game) advanceNightSubPhase() []Event {
	sub := g.state.currentNightSubPhase
	if sub == "" {
		// Defensive: shouldn't happen — beginNightTurns always
		// populates a sub-phase before returning. If it does, no-op
		// rather than panicking, since this is reachable from a
		// wall-clock timer that we don't want to be load-bearing on
		// engine invariants.
		return nil
	}
	// Note: currentNightRole IS empty during NightSubOpening (the
	// night-scoped beat that precedes any role's turn), so we do
	// NOT short-circuit on it.
	role := g.state.currentNightRole

	switch sub {
	case NightSubOpening:
		// Opening elapsed: pop the first role and enter its narrate.
		// nightTurnQueue is guaranteed non-empty here because
		// beginNightTurns populates it before entering opening.
		g.state.currentNightSubPhase = ""
		return g.beginNextNightTurn()

	case NightSubNarrate:
		// narrate → act (real) OR narrate → ponder (phantom).
		// hasLivingRole determines which branch; real turns wait
		// for either the actor's submission or this AdvancePhase
		// firing again at the end of NightSubAct.
		if g.state.HasLivingRole(role) {
			return g.enterNightSubPhase(NightSubAct)
		}
		return g.enterNightSubPhase(NightSubPonder)

	case NightSubAct:
		// Reaching here means AdvancePhase fired during the act
		// window — i.e. the timer expired without a submission.
		// We still pass through ponder so the audio cadence and
		// sub-phase sequence are uniform across submit/timeout. The
		// room's Ponder function reads nightSubmitted (false here)
		// to pick an appropriate post-timeout duration; the default
		// reuses the post-submit beat (~2s) so observers can't
		// distinguish submit from timeout by listening alone.
		g.state.nightSubmitted = false
		return g.enterNightSubPhase(NightSubPonder)

	case NightSubPonder:
		// ponder → sleep, both for real turns (post-submit) and
		// for phantom turns (post-narrate).
		return g.enterNightSubPhase(NightSubSleep)

	case NightSubSleep:
		// sleep → settle. Universal; runs after every role's sleep
		// including the last one (whose settle precedes the
		// night → day transition).
		return g.enterNightSubPhase(NightSubSettle)

	case NightSubSettle:
		// End of this role's turn. Pop the next role from the queue
		// and start it at narrate; or, if the queue is empty,
		// resolve the night and transition to DayDiscussion.
		g.state.currentNightRole = ""
		g.state.currentNightSubPhase = ""
		g.state.nightSubmitted = false
		if len(g.state.nightTurnQueue) > 0 {
			return g.beginNextNightTurn()
		}
		return g.resolveAndExitNight()
	}
	return nil
}

// enterNightSubPhase mutates state to enter the given sub-phase for
// the current role and returns the matching event. Deadline is left
// at 0 — the room layer stamps a wall-clock value before broadcasting.
// Called from advanceNightSubPhase (timer-driven edges) and from
// applyNightAction (the act → ponder edge driven by submission).
func (g *Game) enterNightSubPhase(sub NightSubPhase) []Event {
	role := g.state.currentNightRole
	g.state.currentNightSubPhase = sub

	// One event shape covers every role-scoped sub-phase. Phantom is
	// only consumed for narrate/ponder, but computing it uniformly is
	// correct everywhere: enterNightSubPhase is never called for the
	// (role-less) opening, a living role's act is never phantom, and
	// sleep/settle carry the flag harmlessly (the wire encoder omits
	// it for those sub-phases). Deadline is left 0 — the room stamps a
	// wall-clock value before broadcasting.
	return []Event{NightSubPhaseStarted{
		Sub:      sub,
		Role:     role,
		Day:      g.state.day,
		Deadline: 0,
		Phantom:  role != "" && !g.state.HasLivingRole(role),
	}}
}

// resolveAndExitNight is the common Night-end path: it's called from
// advanceNightSubPhase when the last role's settle completes (queue
// empty). It runs resolveNight, checks for a win, and transitions to
// DayDiscussion (or PhaseEnded). The day counter is incremented when
// entering DayDiscussion. Sub-phase state must already be cleared by
// the caller before this runs (advanceNightSubPhase does this when
// it leaves NightSubSettle with an empty queue).
//
// DayDiscussion is entered with dayLynchResolved=false so the host's
// OpenVoting command is enabled; the only way out of DayDiscussion
// after that is the host pressing the appropriate button.
func (g *Game) resolveAndExitNight() []Event {
	events := g.resolveNight()

	if endEvt, ended := g.checkWin(); ended {
		events = append(events, endEvt)
		from := g.state.phase
		g.state.phase = PhaseEnded
		events = append(events, PhaseChanged{From: from, To: PhaseEnded, Day: g.state.day})
		return events
	}

	from := g.state.phase
	g.state.day++
	g.state.phase = PhaseDayDiscussion
	g.state.dayLynchResolved = false
	events = append(events, PhaseChanged{From: from, To: PhaseDayDiscussion, Day: g.state.day})
	return events
}

// applyBeginNight transitions into PhaseNight, kicking off the night
// turn sequence atomically. Valid from two places:
//
//  1. PhaseLobby AFTER StartGame has dealt roles. This starts Night 1
//     (day stays 0).
//  2. PhaseDayDiscussion AFTER a vote has been finalized for the day
//     (dayLynchResolved == true). This starts Night N+1 (day stays as
//     the just-resolved day number; resolveAndExitNight increments it
//     before transitioning to the next DayDiscussion).
//
// In both cases the engine emits PhaseChanged{To: PhaseNight} followed
// by NightOpeningStarted (the one-shot "City, go to sleep." beat).
// After the room's opening timer elapses, AdvancePhase drives the
// transition to the first role's NightSubNarrate; see NightSubPhase
// for the per-role state machine that follows.
func (g *Game) applyBeginNight(_ BeginNight) ([]Event, error) {
	if g.state.id == "" {
		return nil, ErrWrongPhase
	}
	switch g.state.phase {
	case PhaseEnded:
		return nil, ErrGameEnded
	case PhaseLobby:
		// Roles must have been dealt by StartGame first.
		if !g.state.rolesDealt {
			return nil, ErrWrongPhase
		}
	case PhaseDayDiscussion:
		// Only after a finalized vote — i.e. the room is between
		// "X was lynched" and the next night.
		if !g.state.dayLynchResolved {
			return nil, ErrWrongPhase
		}
	default:
		return nil, ErrWrongPhase
	}

	from := g.state.phase
	g.state.phase = PhaseNight
	g.state.votes = nil
	g.state.dayLynchResolved = false
	events := []Event{PhaseChanged{From: from, To: PhaseNight, Day: g.state.day}}
	events = append(events, g.beginNightTurns()...)
	return events, nil
}

// applyOpenVoting transitions PhaseDayDiscussion into PhaseDayVote.
// Valid only when no lynch has been resolved yet on this day (after a
// finalized vote, the day is effectively over and the only legal
// action is BeginNight).
func (g *Game) applyOpenVoting(_ OpenVoting) ([]Event, error) {
	if g.state.id == "" {
		return nil, ErrWrongPhase
	}
	if g.state.phase == PhaseEnded {
		return nil, ErrGameEnded
	}
	if g.state.phase != PhaseDayDiscussion {
		return nil, ErrWrongPhase
	}
	if g.state.dayLynchResolved {
		return nil, ErrWrongPhase
	}
	from := g.state.phase
	g.state.phase = PhaseDayVote
	g.state.votes = make(map[PlayerID]PlayerID)
	return []Event{PhaseChanged{From: from, To: PhaseDayVote, Day: g.state.day}}, nil
}

// applyClearVotes wipes the in-flight vote tally so the room can
// re-vote from a clean slate. Stays in PhaseDayVote.
//
// Returns ErrNoChange if there are no votes to clear, to avoid noisy
// double-clicks producing redundant events.
func (g *Game) applyClearVotes(_ ClearVotes) ([]Event, error) {
	if g.state.id == "" {
		return nil, ErrWrongPhase
	}
	if g.state.phase == PhaseEnded {
		return nil, ErrGameEnded
	}
	if g.state.phase != PhaseDayVote {
		return nil, ErrWrongPhase
	}
	if len(g.state.votes) == 0 {
		return nil, ErrNoChange
	}
	g.state.votes = make(map[PlayerID]PlayerID)
	return []Event{VoteCleared{Day: g.state.day}}, nil
}

// applyFinalizeVotes resolves the current vote tally. Requires a
// unique plurality (decisive lynch); otherwise rejects with ErrNoChange
// so the host can ClearVotes and re-run the round. On success, lynches
// the plurality target and transitions to PhaseDayDiscussion with
// dayLynchResolved=true (or PhaseEnded if the lynch ends the game).
func (g *Game) applyFinalizeVotes(_ FinalizeVotes) ([]Event, error) {
	if g.state.id == "" {
		return nil, ErrWrongPhase
	}
	if g.state.phase == PhaseEnded {
		return nil, ErrGameEnded
	}
	if g.state.phase != PhaseDayVote {
		return nil, ErrWrongPhase
	}

	target, decisive := g.resolveDayVote()
	if !decisive {
		return nil, ErrNoChange
	}

	var events []Event
	if tp, ok := g.state.findPlayer(target); ok {
		tp.alive = false
	}
	events = append(events, PlayerLynched{PlayerID: target})

	if endEvt, ended := g.checkWin(); ended {
		events = append(events, endEvt)
		from := g.state.phase
		g.state.phase = PhaseEnded
		events = append(events, PhaseChanged{From: from, To: PhaseEnded, Day: g.state.day})
		return events, nil
	}

	// Lynch but no win: return to DayDiscussion with the resolved flag
	// set so the only legal host command is BeginNight.
	from := g.state.phase
	g.state.phase = PhaseDayDiscussion
	g.state.votes = nil
	g.state.dayLynchResolved = true
	events = append(events, PhaseChanged{From: from, To: PhaseDayDiscussion, Day: g.state.day})
	return events, nil
}

// beginNightTurns is called whenever the game enters PhaseNight. It
// builds the night turn queue with ALL acting roles in the canonical
// order (Mafia → Detective → Doctor), regardless of whether any
// player of that role is still alive. It then enters NightSubOpening
// — the one-shot "City, go to sleep." beat that precedes any role's
// narration — without populating currentNightRole. The room's
// opening timer fires AdvancePhase, which pops the first role and
// enters its NightSubNarrate (see advanceNightSubPhase).
//
// Each role's turn walks the five-step NightSubPhase state machine;
// roles with no living holder substitute NightSubPonder for the act
// window so the audio cadence still narrates them. The night ends
// when the whole queue is exhausted.
//
// Returns the events to be appended after PhaseChanged.
func (g *Game) beginNightTurns() []Event {
	g.state.nightTurnQueue = g.state.nightTurnQueue[:0]
	// Canonical order. Mafia first so the doctor (last) saves with the
	// most information about who's been targeted.
	g.state.nightTurnQueue = append(g.state.nightTurnQueue,
		RoleMafia, RoleDetective, RoleDoctor)
	// Enter the night-scoped opening sub-phase. currentNightRole
	// stays empty until the opening elapses and advanceNightSubPhase
	// pops the first role.
	g.state.currentNightRole = ""
	g.state.currentNightSubPhase = NightSubOpening
	g.state.nightSubmitted = false
	return []Event{NightSubPhaseStarted{
		Sub:      NightSubOpening,
		Day:      g.state.day,
		Deadline: 0,
	}}
}

// beginNextNightTurn pops the front of the queue, sets it as the
// current role, and enters NightSubNarrate (the opening audio cue).
// Subsequent sub-phases are driven by AdvancePhase (from the room's
// wall-clock timer) or NightAction (the actor's submission). The
// room layer stamps a real Deadline before broadcasting; the engine
// is timeless and emits Deadline=0.
//
// Note on Phantom for RoleMafia: HasLivingRole(RoleMafia) is always
// true when this function runs. checkWin (called after every
// state-changing event) emits GameEnded the instant living mafia
// hits zero and the phase transitions to PhaseEnded, which prevents
// any further beginNightTurns/beginNextNightTurn calls. The uniform
// `Phantom: !HasLivingRole(next)` computation in enterNightSubPhase
// is kept for symmetry across roles — it's correct, it's just
// dead-on-arrival for the mafia case.
func (g *Game) beginNextNightTurn() []Event {
	if len(g.state.nightTurnQueue) == 0 {
		return nil
	}
	next := g.state.nightTurnQueue[0]
	g.state.nightTurnQueue = g.state.nightTurnQueue[1:]
	g.state.currentNightRole = next
	g.state.nightSubmitted = false
	return g.enterNightSubPhase(NightSubNarrate)
}

// checkWin evaluates win conditions and, if a faction has won, returns
// the GameEnded event and true. Otherwise returns the zero event and
// false.
//
// Mafia win:  living mafia >= living town  (they can no longer be lynched).
// Town win:   living mafia == 0.
//
// These conditions are mutually exclusive once both have at least one
// living member at the start of any check, which is invariant from the
// roster validation in CreateGame.
func (g *Game) checkWin() (GameEnded, bool) {
	mafia := g.state.factionLivingCount(FactionMafia)
	town := g.state.factionLivingCount(FactionTown)

	switch {
	case mafia == 0:
		return GameEnded{
			Winner:     FactionTown,
			FinalRoles: g.state.finalRolesSnapshot(),
		}, true
	case mafia >= town:
		return GameEnded{
			Winner:     FactionMafia,
			FinalRoles: g.state.finalRolesSnapshot(),
		}, true
	}
	return GameEnded{}, false
}
