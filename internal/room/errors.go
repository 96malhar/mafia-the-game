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
