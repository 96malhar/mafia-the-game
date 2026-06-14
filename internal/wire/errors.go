package wire

// ErrorCode is the typed enum of every machine-readable error code the
// server can send to a client in a ServerMsgError frame.
//
// Why a typed string (not an int): the value travels as JSON; the
// string form is the wire contract. Defining a named type instead of
// raw `string` lets the compiler catch typos at producer sites
// (room.OutError, transport/ws/pumps) — a typo is now a compile error,
// not a silent string mismatch.
//
// Stability: like the other tag families in this package, the
// underlying string of each constant is part of the wire contract.
// Clients in the wild key off the string ("auth_failed", "wrong_phase"
// etc.) when deciding how to recover, so renaming or removing one is
// a compatibility break. Adding a new code is fine.
//
// Single source of truth: ErrorCodes (below) lists every constant in
// this file. internal/room enforces — via TestErrorCodes_Registry — that
// every code here has a corresponding sentinel error in room.errorFor
// and vice versa, so producer and consumer can't drift apart.
type ErrorCode string

const (
	// ErrCodeInternal is the catch-all bucket for engine or server
	// errors that don't match a more specific sentinel. Should be
	// rare in practice; if it shows up in real traffic, that's a
	// signal to add a more specific code.
	ErrCodeInternal ErrorCode = "internal"

	// --- Engine-derived codes -------------------------------------
	//
	// Each of these maps 1:1 to a sentinel in internal/game/errors.go.
	// The mapping lives in internal/room/errors.go (errorFor).

	ErrCodeWrongPhase       ErrorCode = "wrong_phase"
	ErrCodeUnknownPlayer    ErrorCode = "unknown_player"
	ErrCodeDuplicatePlayer  ErrorCode = "duplicate_player"
	ErrCodeDuplicateName    ErrorCode = "duplicate_name"
	ErrCodePlayerDead       ErrorCode = "player_dead"
	ErrCodeNotYourAction    ErrorCode = "not_your_action"
	ErrCodeNotYourTurn      ErrorCode = "not_your_turn"
	ErrCodeSelfTarget       ErrorCode = "self_target"
	ErrCodeRosterMismatch   ErrorCode = "roster_mismatch"
	ErrCodeTownNotMajority  ErrorCode = "town_not_majority"
	ErrCodeLobbyFull        ErrorCode = "lobby_full"
	ErrCodeGameEnded        ErrorCode = "game_ended"
	ErrCodeNoChange         ErrorCode = "no_change"
	ErrCodeAlreadyActed     ErrorCode = "already_acted"
	ErrCodeBlocked          ErrorCode = "blocked"
	ErrCodeVotingIncomplete ErrorCode = "voting_incomplete"

	// --- Room / transport-layer codes -----------------------------
	//
	// These don't originate in the engine: they're how the room or
	// the websocket transport reports its own failures (auth, framing,
	// authorization).

	// ErrCodeAuthFailed: rejoin handshake with an unknown player ID
	// or wrong secret.
	ErrCodeAuthFailed ErrorCode = "auth_failed"

	// ErrCodeNotJoined: a command other than join/rejoin arrived
	// before the subscriber has identified itself.
	ErrCodeNotJoined ErrorCode = "not_joined"

	// ErrCodeForbidden: the subscriber is authenticated but lacks
	// the privilege for this command (e.g. non-host issuing a
	// host-only command, or anyone sending AdvancePhase which is
	// server-internal).
	ErrCodeForbidden ErrorCode = "forbidden"

	// ErrCodeBadFrame: the transport received a websocket frame of
	// the wrong type (binary when text was expected).
	ErrCodeBadFrame ErrorCode = "bad_frame"

	// ErrCodeBadMessage: the transport received text that didn't
	// parse as a valid client message (invalid JSON, missing type,
	// unknown type).
	ErrCodeBadMessage ErrorCode = "bad_message"
)

// ErrorCodes is the canonical list of every ErrorCode constant. The
// room package iterates this in TestErrorCodes_Registry to assert
// that errorFor maps each one to a known sentinel and rejects unknown
// strings — i.e. that this file and internal/room/errors.go can't
// drift apart.
//
// Order is stable (declaration order) so test failures point at a
// predictable position in the slice.
var ErrorCodes = []ErrorCode{
	ErrCodeInternal,

	ErrCodeWrongPhase,
	ErrCodeUnknownPlayer,
	ErrCodeDuplicatePlayer,
	ErrCodeDuplicateName,
	ErrCodePlayerDead,
	ErrCodeNotYourAction,
	ErrCodeNotYourTurn,
	ErrCodeSelfTarget,
	ErrCodeRosterMismatch,
	ErrCodeTownNotMajority,
	ErrCodeLobbyFull,
	ErrCodeGameEnded,
	ErrCodeNoChange,
	ErrCodeAlreadyActed,
	ErrCodeBlocked,
	ErrCodeVotingIncomplete,

	ErrCodeAuthFailed,
	ErrCodeNotJoined,
	ErrCodeForbidden,
	ErrCodeBadFrame,
	ErrCodeBadMessage,
}
