package game

// Phase is the current stage of the game. Transitions are linear:
//
//	Lobby -> Night -> DayDiscussion -> DayVote -> Night -> ... -> Ended
//
// The day is split into two sub-phases so discussion happens before any
// votes are visible. Discussion forces social deduction; the vote phase
// then crystallizes a decision against a public tally.
//
// On the very first night the doctor may not self-save and the detective
// has not yet investigated anyone — those nuances live in the rules,
// not in the Phase type itself.
type Phase string

const (
	// PhaseLobby is the pre-game state where players join and the host
	// can start the game. No game actions are permitted.
	PhaseLobby Phase = "lobby"

	// PhaseNight is when role-specific night actions are submitted
	// privately (mafia kill, doctor save, detective investigate).
	PhaseNight Phase = "night"

	// PhaseDayDiscussion is the public talk-only phase that follows the
	// night reveal. Votes submitted during this phase are rejected with
	// ErrWrongPhase; players must wait for PhaseDayVote.
	PhaseDayDiscussion Phase = "day_discussion"

	// PhaseDayVote is the public voting phase. Surviving players cast
	// one vote each; the vote tally is public. If no player has a strict
	// plurality when the phase ends, the day is extended once for a
	// re-vote; if still tied, the day ends with no lynch.
	PhaseDayVote Phase = "day_vote"

	// PhaseEnded is terminal: a win condition has been reached and no
	// further commands (except inspection) are accepted.
	PhaseEnded Phase = "ended"
)

// Valid reports whether p is a known phase.
func (p Phase) Valid() bool {
	switch p {
	case PhaseLobby, PhaseNight, PhaseDayDiscussion, PhaseDayVote, PhaseEnded:
		return true
	}
	return false
}

// IsDay reports whether p is one of the day sub-phases. Useful for rules
// that apply to "the day" without caring which half.
func (p Phase) IsDay() bool {
	return p == PhaseDayDiscussion || p == PhaseDayVote
}
