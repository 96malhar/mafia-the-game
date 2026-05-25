package game

import "errors"

// Sentinel errors returned by Apply when a command is rejected. They are
// values (not types) so callers can use errors.Is for matching while we
// keep the option of wrapping with extra context (e.g. fmt.Errorf with %w)
// inside the engine.
//
// These names are deliberately *behavioural* (what went wrong) rather
// than referencing the specific command — "wrong phase" is reusable
// across many commands, whereas "ErrCannotJoinAfterStart" would not be.
var (
	// ErrWrongPhase is returned when a command is submitted in a phase
	// where it is not legal (e.g. DayVote during Night, AddPlayer after
	// the game has started).
	ErrWrongPhase = errors.New("game: command not allowed in current phase")

	// ErrUnknownPlayer is returned when a command references a player ID
	// that is not part of this game.
	ErrUnknownPlayer = errors.New("game: unknown player")

	// ErrDuplicatePlayer is returned when AddPlayer is called with an ID
	// that already exists in the lobby.
	ErrDuplicatePlayer = errors.New("game: player already in game")

	// ErrPlayerDead is returned when a command requires the actor or
	// target to be alive but they are not.
	ErrPlayerDead = errors.New("game: player is dead")

	// ErrNotYourAction is returned when a player submits a night action
	// their role does not permit (e.g. a villager calling NightAction).
	ErrNotYourAction = errors.New("game: action not permitted for this role")

	// ErrSelfTarget is returned when a player targets themselves in a
	// context that forbids it (e.g. detective investigating self).
	ErrSelfTarget = errors.New("game: cannot target self")

	// ErrRosterMismatch is returned by StartGame when the configured
	// Roles slice length does not match the number of joined players.
	ErrRosterMismatch = errors.New("game: number of roles does not match number of players")

	// ErrGameEnded is returned when any command (other than inspection)
	// is submitted after PhaseEnded.
	ErrGameEnded = errors.New("game: game has ended")
)
