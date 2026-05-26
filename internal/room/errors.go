package room

import (
	"errors"

	"github.com/malhar/mafia-the-game/internal/game"
)

// errorFor maps an engine sentinel into an OutError with a stable
// machine-readable Code. Clients render messages keyed off Code, so
// every sentinel in internal/game/errors.go must have a case here.
// TestRoom_ErrorForMapsAllSentinels enforces that.
func errorFor(err error) OutError {
	switch {
	case errors.Is(err, game.ErrWrongPhase):
		return OutError{Code: "wrong_phase", Message: err.Error()}
	case errors.Is(err, game.ErrUnknownPlayer):
		return OutError{Code: "unknown_player", Message: err.Error()}
	case errors.Is(err, game.ErrDuplicatePlayer):
		return OutError{Code: "duplicate_player", Message: err.Error()}
	case errors.Is(err, game.ErrPlayerDead):
		return OutError{Code: "player_dead", Message: err.Error()}
	case errors.Is(err, game.ErrNotYourAction):
		return OutError{Code: "not_your_action", Message: err.Error()}
	case errors.Is(err, game.ErrNotYourTurn):
		return OutError{Code: "not_your_turn", Message: err.Error()}
	case errors.Is(err, game.ErrSelfTarget):
		return OutError{Code: "self_target", Message: err.Error()}
	case errors.Is(err, game.ErrRosterMismatch):
		return OutError{Code: "roster_mismatch", Message: err.Error()}
	case errors.Is(err, game.ErrLobbyFull):
		return OutError{Code: "lobby_full", Message: err.Error()}
	case errors.Is(err, game.ErrGameEnded):
		return OutError{Code: "game_ended", Message: err.Error()}
	case errors.Is(err, game.ErrNoChange):
		return OutError{Code: "no_change", Message: err.Error()}
	case errors.Is(err, game.ErrAlreadyActed):
		return OutError{Code: "already_acted", Message: err.Error()}
	default:
		return OutError{Code: "internal", Message: err.Error()}
	}
}

// joinErrorFor is errorFor specialized for the first-time join
// handshake (room.handleJoin). It returns the same machine-readable
// Code as errorFor — so the wire contract and existing tests stay
// stable — but rewrites the Message for the codes that mean "this
// room can't accept you" into player-facing English. Other codes
// are passed through unchanged (they shouldn't fire during a join,
// but if a future engine change adds a new reason we'd rather show
// the raw sentinel than silently swallow it).
//
// The point of doing this server-side rather than on the client is
// to keep all "what does this error mean to the user?" knowledge in
// one place. Client code just renders OutError.Message as-is.
func joinErrorFor(err error) OutError {
	out := errorFor(err)
	switch out.Code {
	case "wrong_phase":
		// AddPlayer is only legal in PhaseLobby with no roles dealt.
		// Both pre-StartGame phase mismatches and the
		// "roles already dealt" check in applyAddPlayer surface
		// here.
		out.Message = "This game is already in progress. Create a new room to play."
	case "lobby_full":
		out.Message = "This room is full. Create a new room to play."
	case "game_ended":
		out.Message = "This game has already ended. Create a new room to play."
	}
	return out
}
