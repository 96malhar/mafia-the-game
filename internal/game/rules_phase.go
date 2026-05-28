package game

// applyAdvancePhase ends the current night turn (PhaseNight only). It
// is an INTERNAL command, invoked by the room's per-turn timer when a
// real role times out or a phantom turn elapses. Daytime pacing is no
// longer driven by AdvancePhase: hosts use BeginNight / OpenVoting /
// ClearVotes / FinalizeVotes for those transitions.
//
// Transition summary:
//
//	Lobby / DayDiscussion / DayVote -> ErrWrongPhase
//	Ended                           -> ErrGameEnded
//	Night, midQueue                 -> NightTurnEnded + NightTurnStarted (next role)
//	Night, lastTurn                 -> NightTurnEnded + resolveNight;
//	                                   if not won -> DayDiscussion (Day N+1).
func (g *Game) applyAdvancePhase(_ AdvancePhase) ([]Event, error) {
	if g.state.id == "" {
		return nil, ErrWrongPhase
	}
	switch g.state.phase {
	case PhaseEnded:
		return nil, ErrGameEnded
	case PhaseNight:
		return g.advanceFromNight(), nil
	default:
		// Day phases are host-driven; AdvancePhase is invalid here.
		return nil, ErrWrongPhase
	}
}

// advanceFromNight ends the current role's turn. If more roles remain
// in the night-turn queue, it pops the next one and emits
// NightTurnStarted. Otherwise it runs resolveAndExitNight which
// handles resolution + win-check + transition to DayDiscussion.
func (g *Game) advanceFromNight() []Event {
	var events []Event
	if g.state.currentNightRole != "" {
		events = append(events, NightTurnEnded{Role: g.state.currentNightRole})
		g.state.currentNightRole = ""
		g.state.nightTurnDeadlineMillis = 0
	}

	if len(g.state.nightTurnQueue) > 0 {
		events = append(events, g.beginNextNightTurn()...)
		return events
	}
	events = append(events, g.resolveAndExitNight()...)
	return events
}

// resolveAndExitNight is the common Night-end path shared by:
//   - advanceFromNight when the queue is exhausted by skip/timeout;
//   - applyNightAction when the action ended the last turn in the queue.
//
// It runs resolveNight, checks for a win, and transitions to
// DayDiscussion (or PhaseEnded). The day counter is incremented when
// entering DayDiscussion. Caller is responsible for emitting any
// NightTurnEnded events for the prior turn before calling this.
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
// by the first NightTurnStarted (mafia, or its phantom).
func (g *Game) applyBeginNight(_ BeginNight) ([]Event, error) {
	if g.state.id == "" {
		return nil, ErrWrongPhase
	}
	switch g.state.phase {
	case PhaseEnded:
		return nil, ErrGameEnded
	case PhaseLobby:
		// Roles must have been dealt by StartGame first.
		if len(g.state.players) == 0 || g.state.players[0].role == "" {
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
// player of that role is still alive. Turns for roles with no living
// holder are emitted as "phantom" turns: they take a (shorter,
// randomized) wall-clock duration on the room side and accept no
// NightAction, but the moderator narration still plays — otherwise
// the room would deduce a role is dead just from the missing audio
// cue. The night ends when the whole queue is exhausted.
//
// Returns the events to be appended after PhaseChanged.
func (g *Game) beginNightTurns() []Event {
	g.state.nightTurnQueue = g.state.nightTurnQueue[:0]
	// Canonical order. Mafia first so the doctor (last) saves with the
	// most information about who's been targeted.
	g.state.nightTurnQueue = append(g.state.nightTurnQueue,
		RoleMafia, RoleDetective, RoleDoctor)
	return g.beginNextNightTurn()
}

// beginNextNightTurn pops the front of the queue and sets it as the
// current role. Deadline is left at 0 — the engine is timeless; the
// room layer stamps a real deadline before broadcasting. The Phantom
// flag on the emitted event tells the room/clients to treat this turn
// as "audio only": no action will be accepted, and the room arms a
// shorter (randomized) wall-clock timer.
//
// Note on Phantom for RoleMafia: hasLivingRole(RoleMafia) is always
// true when this function runs. checkWin (called after every
// state-changing event) emits GameEnded the instant living mafia
// hits zero and the phase transitions to PhaseEnded, which prevents
// any further beginNightTurns/beginNextNightTurn calls. The
// uniform `Phantom: !hasLivingRole(next)` computation is kept for
// symmetry across roles — it's correct, it's just dead-on-arrival
// for the mafia case.
func (g *Game) beginNextNightTurn() []Event {
	if len(g.state.nightTurnQueue) == 0 {
		return nil
	}
	next := g.state.nightTurnQueue[0]
	g.state.nightTurnQueue = g.state.nightTurnQueue[1:]
	g.state.currentNightRole = next
	g.state.nightTurnDeadlineMillis = 0
	return []Event{NightTurnStarted{
		Role:     next,
		Deadline: 0,
		Phantom:  !g.hasLivingRole(next),
	}}
}

// hasLivingRole reports whether at least one living player holds the
// given role.
func (g *Game) hasLivingRole(r Role) bool {
	for i := range g.state.players {
		if g.state.players[i].alive && g.state.players[i].role == r {
			return true
		}
	}
	return false
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
