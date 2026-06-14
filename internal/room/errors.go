package room

import (
	"errors"

	"github.com/96malhar/mafia-the-game/internal/game"
	"github.com/96malhar/mafia-the-game/internal/wire"
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
	{game.ErrTownNotMajority, wire.ErrCodeTownNotMajority},
	{game.ErrLobbyFull, wire.ErrCodeLobbyFull},
	{game.ErrGameEnded, wire.ErrCodeGameEnded},
	{game.ErrNoChange, wire.ErrCodeNoChange},
	{game.ErrAlreadyActed, wire.ErrCodeAlreadyActed},
	{game.ErrBlocked, wire.ErrCodeBlocked},
	{game.ErrVotingIncomplete, wire.ErrCodeVotingIncomplete},

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
// should use errorWithMsg, which keeps the typed Code and the rejection
// metric while overriding the text.
//
// TestRoom_ErrorForMapsAllSentinels enforces that every engine
// sentinel is present in sentinelCodes. TestErrorCodes_Registry
// (whole-package) enforces that every wire.ErrorCode has a matching
// sentinel here.
func errorFor(err error) OutError {
	code := codeFor(err)
	// Single chokepoint for every rejection: record it as a metric
	// (labelled by code) instead of logging, so we get trends/alerting
	// without flooding the logs with normal user errors.
	recordCommandRejected(code)
	return OutError{Code: code, Message: err.Error()}
}

// codeFor maps a known sentinel to its wire-stable ErrorCode, defaulting to
// ErrCodeInternal for anything unrecognized. It is the pure lookup half of
// errorFor — split out so read-only probes (JoinStatus) can classify an error
// WITHOUT recording a command-rejected metric, which would otherwise count a
// mere "can I join?" question as a failed command.
func codeFor(err error) wire.ErrorCode {
	for _, m := range sentinelCodes {
		if errors.Is(err, m.err) {
			return m.code
		}
	}
	return wire.ErrCodeInternal
}

// errorWithMsg is errorFor with a per-call-site Message override. It keeps
// the typed wire.ErrorCode and routes through errorFor's rejection-metric
// chokepoint, replacing only the default sentinel text — for codes like
// ErrForbidden that cover several distinct situations the user benefits
// from distinguishing.
func errorWithMsg(err error, msg string) OutError {
	out := errorFor(err)
	out.Message = msg
	return out
}

// startErrorFor is errorFor specialized for the StartGame command. It keeps
// the machine-readable Code (and the rejection metric) but rewrites the
// Message for roster failures into player-facing English that points the host
// at the lever to adjust — mirroring joinErrorFor for the join handshake.
// Client code renders OutError.Message as-is, so all "what does this mean to
// the host?" knowledge stays server-side.
func startErrorFor(err error) OutError {
	out := errorFor(err)
	out.Message = startBlockMessage(out.Code, out.Message)
	return out
}

// startBlockMessage maps a StartGame rejection code to player-facing English,
// falling back to the raw sentinel text for any code that isn't roster-shaped.
// It's the single source of "why can't this roster start?" — kept in sync with
// the client's pre-start hint in web/actions.js (which gates the Start button).
func startBlockMessage(code wire.ErrorCode, fallback string) string {
	switch code {
	case wire.ErrCodeTownNotMajority:
		return "The town must hold more than half the seats. Reduce the number of mafia, or turn off a mafia-aligned role like the Yakuza or Consort."
	case wire.ErrCodeRosterMismatch:
		return "This roster can't start. Check the player count, and make sure the mafia plus special roles don't outnumber the seats."
	default:
		return fallback
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
	out.Message = joinBlockMessage(out.Code, out.Message)
	return out
}

// joinBlockMessage maps a join-blocking wire code to player-facing English,
// falling back to the raw sentinel text for any code that shouldn't block a
// join. It is the single source of "what does this rejection mean to a would-
// be joiner?" — shared by joinErrorFor (the live join handshake) and
// JoinStatus (the pre-join CheckRoom probe) so the two always say the same
// thing.
func joinBlockMessage(code wire.ErrorCode, fallback string) string {
	switch code {
	case wire.ErrCodeWrongPhase:
		// AddPlayer is only legal in PhaseLobby with no roles dealt.
		// Both pre-StartGame phase mismatches and the
		// "roles already dealt" check in applyAddPlayer surface here.
		return "This game is already in progress. Create a new room to play."
	case wire.ErrCodeLobbyFull:
		return "This room is full. Create a new room to play."
	case wire.ErrCodeGameEnded:
		return "This game has already ended. Create a new room to play."
	case wire.ErrCodeDuplicateName:
		// Engine rejects names that match (case-insensitively, after
		// trim) someone already in the lobby. The client renders this
		// in the join form so the user can pick a different name
		// without losing the rest of the join flow.
		return "That name is already taken. Pick a different name."
	default:
		return fallback
	}
}

// JoinStatus is the result of a pre-join probe (Room.JoinStatus, surfaced by
// the CheckRoom HTTP endpoint). Joinable is true when a fresh join would
// currently be accepted; otherwise Code/Message explain why, using the SAME
// player-facing text the live join handshake (joinErrorFor) would return.
//
// Unlike a real rejection, building a JoinStatus records NO command-rejected
// metric — a probe is a question, not a failed command.
type JoinStatus struct {
	Joinable bool
	Code     wire.ErrorCode // zero ("") when Joinable
	Message  string         // player-facing; empty when Joinable
}

// joinStatusFor builds a JoinStatus from an engine join-block reason (nil =
// joinable, per game.JoinBlockedReason). It reuses codeFor + joinBlockMessage
// WITHOUT errorFor's rejection metric.
func joinStatusFor(reason error) JoinStatus {
	if reason == nil {
		return JoinStatus{Joinable: true}
	}
	code := codeFor(reason)
	return JoinStatus{Joinable: false, Code: code, Message: joinBlockMessage(code, reason.Error())}
}
