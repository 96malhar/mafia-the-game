package game

// This file holds the small, shared guards and state-transition helpers
// that the apply* command handlers lean on. Centralizing them keeps the
// documented error-ordering contract (PhaseEnded BEFORE the generic
// phase mismatch) in one place instead of being copy-pasted into every
// handler, where a single drift would change the wire error a client
// sees.

// requireActiveGame is the common preamble for every command that acts on
// an existing game. It rejects a pristine (never-created) engine with
// ErrWrongPhase and an already-finished game with ErrGameEnded.
//
// The PhaseEnded check MUST come before any generic phase mismatch so the
// wire layer can map it to wire.ErrCodeGameEnded ("This game has already
// ended.") rather than the generic "already in progress" message.
func (g *Game) requireActiveGame() error {
	if g.state.id == "" {
		return ErrWrongPhase
	}
	if g.state.phase == PhaseEnded {
		return ErrGameEnded
	}
	return nil
}

// requirePhase checks the game is active AND currently in want, returning
// ErrWrongPhase on a mismatch (or ErrGameEnded if the game has ended).
func (g *Game) requirePhase(want Phase) error {
	if err := g.requireActiveGame(); err != nil {
		return err
	}
	if g.state.phase != want {
		return ErrWrongPhase
	}
	return nil
}

// requireLobbyOpen checks the game is in PhaseLobby AND roles have not yet
// been dealt. StartGame closes the lobby (rolesDealt) even though the game
// stays in PhaseLobby until BeginNight, so the lobby-mutating commands
// (AddPlayer, SetMafiaCount, the optional-role toggles) gate on both.
func (g *Game) requireLobbyOpen() error {
	if err := g.requirePhase(PhaseLobby); err != nil {
		return err
	}
	if g.state.rolesDealt {
		return ErrWrongPhase
	}
	return nil
}

// applyLobbyToggle is the shared body for the optional-role lobby toggles
// (SetConsort, SetVigilante). It validates the lobby is open, no-ops when
// the flag is unchanged, flips it, and returns the matching change event.
func (g *Game) applyLobbyToggle(enabled bool, current *bool, event Event) ([]Event, error) {
	if err := g.requireLobbyOpen(); err != nil {
		return nil, err
	}
	if enabled == *current {
		return nil, ErrNoChange
	}
	*current = enabled
	return []Event{event}, nil
}

// endGameIfWon appends the GameEnded event and the PhaseChanged into
// PhaseEnded when a faction has won, flipping the engine to PhaseEnded. It
// returns the (possibly extended) event slice and whether the game ended.
// Callers run it after applying a death so a cabal-ending resolution ends
// the game in exactly one place.
func (g *Game) endGameIfWon(events []Event) ([]Event, bool) {
	endEvt, ended := g.checkWin()
	if !ended {
		return events, false
	}
	events = append(events, endEvt)
	from := g.state.phase
	g.state.phase = PhaseEnded
	events = append(events, PhaseChanged{From: from, To: PhaseEnded, Day: g.state.day})
	return events, true
}

// endDayToDiscussion flips PhaseDayVote back to PhaseDayDiscussion,
// clears the tally, marks the lynch resolved (so the only legal host
// command becomes BeginNight), and returns the PhaseChanged event.
func (g *Game) endDayToDiscussion() Event {
	from := g.state.phase
	g.state.phase = PhaseDayDiscussion
	g.state.votes = nil
	g.state.dayLynchResolved = true
	return PhaseChanged{From: from, To: PhaseDayDiscussion, Day: g.state.day}
}
