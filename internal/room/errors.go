package room

import (
	"errors"

	"github.com/malhar/mafia-the-game/internal/game"
	"github.com/malhar/mafia-the-game/internal/wire"
)

// sentinelCodes is the single mapping table from a known sentinel
// (engine or room-level) to its wire-stable ErrorCode. Adding a new
// sentinel is a one-line addition here; TestErrorCodes_Registry then
// catches if anyone forgets to expose it in internal/wire.
//
// We use a slice of pairs rather than map[error]wire.ErrorCode because
// errors are not naturally comparable in a hash-stable way (an error
// implementation could use a custom Error() but still pointer-equal
// itself); errors.Is on a typed sentinel value is the canonical match.
var sentinelCodes = []struct {
	err  error
	code wire.ErrorCode
}{
	// Engine sentinels (internal/game/errors.go).
	{game.ErrWrongPhase, wire.ErrCodeWrongPhase},
	{game.ErrUnknownPlayer, wire.ErrCodeUnknownPlayer},
	{game.ErrDuplicatePlayer, wire.ErrCodeDuplicatePlayer},
	{game.ErrDuplicateName, wire.ErrCodeDuplicateName},
	{game.ErrPlayerDead, wire.ErrCodePlayerDead},
	{game.ErrNotYourAction, wire.ErrCodeNotYourAction},
	{game.ErrNotYourTurn, wire.ErrCodeNotYourTurn},
	{game.ErrSelfTarget, wire.ErrCodeSelfTarget},
	{game.ErrRosterMismatch, wire.ErrCodeRosterMismatch},
	{game.ErrLobbyFull, wire.ErrCodeLobbyFull},
	{game.ErrGameEnded, wire.ErrCodeGameEnded},
	{game.ErrNoChange, wire.ErrCodeNoChange},
	{game.ErrAlreadyActed, wire.ErrCodeAlreadyActed},

	// Room / transport sentinels (sentinels.go).
	{ErrAuthFailed, wire.ErrCodeAuthFailed},
	{ErrNotJoined, wire.ErrCodeNotJoined},
	{ErrForbidden, wire.ErrCodeForbidden},
	{ErrBadFrame, wire.ErrCodeBadFrame},
	{ErrBadMessage, wire.ErrCodeBadMessage},
	{ErrInternal, wire.ErrCodeInternal},
}

// errorFor maps a known sentinel into an OutError with a typed
// wire.ErrorCode. The default branch — for any error that isn't one
// of our sentinels — returns ErrCodeInternal so the client at least
// gets a coherent code rather than the raw Go error text leaking into
// the wire `code` field.
//
// Callers that need a per-call-site Message (e.g. ErrForbidden, which
// covers both "non-host command" and "advancePhase is server-internal")
// should call errorFor first and then overwrite OutError.Message —
// see room.dispatch.
//
// TestRoom_ErrorForMapsAllSentinels enforces that every engine
// sentinel is present in sentinelCodes. TestErrorCodes_Registry
// (whole-package) enforces that every wire.ErrorCode has a matching
// sentinel here.
func errorFor(err error) OutError {
	for _, m := range sentinelCodes {
		if errors.Is(err, m.err) {
			return OutError{Code: m.code, Message: err.Error()}
		}
	}
	return OutError{Code: wire.ErrCodeInternal, Message: err.Error()}
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
	case wire.ErrCodeWrongPhase:
		// AddPlayer is only legal in PhaseLobby with no roles dealt.
		// Both pre-StartGame phase mismatches and the
		// "roles already dealt" check in applyAddPlayer surface
		// here.
		out.Message = "This game is already in progress. Create a new room to play."
	case wire.ErrCodeLobbyFull:
		out.Message = "This room is full. Create a new room to play."
	case wire.ErrCodeGameEnded:
		out.Message = "This game has already ended. Create a new room to play."
	case wire.ErrCodeDuplicateName:
		// Engine rejects names that match (case-insensitively,
		// after trim) someone already in the lobby. The client
		// renders this in the join form so the user can pick a
		// different name without losing the rest of the join flow.
		out.Message = "That name is already taken. Pick a different name."
	}
	return out
}
