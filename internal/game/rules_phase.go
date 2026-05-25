package game

// applyAdvancePhase moves the game to its next logical state and
// resolves any pending actions/votes for the phase being left.
//
// Transition table:
//
//	Lobby            -> ErrWrongPhase (use StartGame).
//	Night            -> resolveNight; win-check; if not over, -> DayDiscussion.
//	DayDiscussion    -> -> DayVote (no resolution).
//	DayVote (1st)    -> resolveDayVote; if decisive lynch + win-check;
//	                    else if not yet extended, clear votes and stay in
//	                    DayVote with dayVoteExtended=true (emit VoteExtended);
//	                    else end day with no lynch and continue to Night.
//	Ended            -> ErrGameEnded.
//
// On any transition that moves between phases, a PhaseChanged event is
// emitted. The day number is incremented when entering DayDiscussion.
func (g *Game) applyAdvancePhase(_ AdvancePhase) ([]Event, error) {
	if g.state.id == "" {
		return nil, ErrWrongPhase
	}
	switch g.state.phase {
	case PhaseLobby:
		return nil, ErrWrongPhase
	case PhaseEnded:
		return nil, ErrGameEnded
	case PhaseNight:
		return g.advanceFromNight(), nil
	case PhaseDayDiscussion:
		return g.advanceFromDayDiscussion(), nil
	case PhaseDayVote:
		return g.advanceFromDayVote(), nil
	default:
		return nil, ErrWrongPhase
	}
}

// advanceFromNight resolves night actions, checks for a winner, and if
// the game continues, transitions to PhaseDayDiscussion and increments
// the day counter.
func (g *Game) advanceFromNight() []Event {
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
	g.state.dayVoteExtended = false
	events = append(events, PhaseChanged{From: from, To: PhaseDayDiscussion, Day: g.state.day})
	return events
}

// advanceFromDayDiscussion is a no-op transition into PhaseDayVote.
// Votes are not yet possible during discussion, so there is no
// resolution to perform here.
func (g *Game) advanceFromDayDiscussion() []Event {
	from := g.state.phase
	g.state.phase = PhaseDayVote
	if g.state.votes == nil {
		g.state.votes = make(map[PlayerID]PlayerID)
	}
	return []Event{PhaseChanged{From: from, To: PhaseDayVote, Day: g.state.day}}
}

// advanceFromDayVote resolves the day vote, applying the extension rule:
//
//   - Unique plurality              -> lynch + win-check; transition to Night.
//   - No plurality, not yet extended -> emit VoteExtended; clear votes;
//     remain in PhaseDayVote.
//   - No plurality, already extended -> end day with no lynch; win-check;
//     transition to Night.
func (g *Game) advanceFromDayVote() []Event {
	var events []Event
	target, decisive := g.resolveDayVote()

	if !decisive {
		if !g.state.dayVoteExtended {
			g.state.dayVoteExtended = true
			g.state.votes = make(map[PlayerID]PlayerID)
			events = append(events, VoteExtended{Day: g.state.day})
			return events
		}
		// Already extended once and still indecisive: no lynch, move on.
		return g.endDayToNight(events)
	}

	// Decisive lynch.
	if tp, ok := g.state.findPlayer(target); ok {
		tp.alive = false
	}
	events = append(events, PlayerLynched{PlayerID: target})

	if endEvt, ended := g.checkWin(); ended {
		events = append(events, endEvt)
		from := g.state.phase
		g.state.phase = PhaseEnded
		events = append(events, PhaseChanged{From: from, To: PhaseEnded, Day: g.state.day})
		return events
	}
	return g.endDayToNight(events)
}

// endDayToNight performs the bookkeeping shared by both the "no lynch
// after extension" and "lynch did not end the game" exits from
// PhaseDayVote. It clears day-scoped state and emits PhaseChanged.
func (g *Game) endDayToNight(events []Event) []Event {
	from := g.state.phase
	g.state.phase = PhaseNight
	g.state.votes = nil
	g.state.dayVoteExtended = false
	events = append(events, PhaseChanged{From: from, To: PhaseNight, Day: g.state.day})
	return events
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
