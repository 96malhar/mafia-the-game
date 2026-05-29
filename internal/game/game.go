package game

import "fmt"

// Game is the engine wrapper around GameState. It owns the authoritative
// state and is the only entry point for mutation.
//
// Game itself holds no I/O, no time, no networking. Apply is a
// deterministic function from (current state, command) -> (events, new
// state). Replay is therefore trivial: construct a fresh Game and apply
// the events' originating commands in order to reproduce the same state.
type Game struct {
	state *GameState
}

// New constructs an empty Game in PhaseLobby. The first command applied
// must be CreateGame; everything else returns ErrWrongPhase until then.
func New() *Game {
	return &Game{state: newState()}
}

// State returns the read-only state for inspection. Mutations must go
// through Apply.
func (g *Game) State() *GameState { return g.state }

// Apply attempts to execute cmd against the current state. On success it
// returns the events produced by the command, in the order they were
// generated, and mutates the state to reflect them. On failure no state
// change occurs and the returned events slice is nil.
//
// The type switch dispatches to a small focused handler per command,
// each living in rules.go. This keeps Apply itself a thumbnail of the
// engine's vocabulary.
func (g *Game) Apply(cmd Command) ([]Event, error) {
	switch c := cmd.(type) {
	case CreateGame:
		return g.applyCreateGame(c)
	case AddPlayer:
		return g.applyAddPlayer(c)
	case SetMafiaCount:
		return g.applySetMafiaCount(c)
	case StartGame:
		return g.applyStartGame(c)
	case BeginNight:
		return g.applyBeginNight(c)
	case OpenVoting:
		return g.applyOpenVoting(c)
	case RevealVotes:
		return g.applyRevealVotes(c)
	case ClearVotes:
		return g.applyClearVotes(c)
	case FinalizeVotes:
		return g.applyFinalizeVotes(c)
	case NightAction:
		return g.applyNightAction(c)
	case DayVote:
		return g.applyDayVote(c)
	case AdvancePhase:
		return g.applyAdvancePhase(c)
	default:
		// Unreachable: Command is a closed interface (see command.go),
		// so the compiler guarantees this switch covers every shape.
		// We keep the default for defense-in-depth.
		return nil, fmt.Errorf("game: unknown command type %T", cmd)
	}
}
