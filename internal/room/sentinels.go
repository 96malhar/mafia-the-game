package room

import "errors"

// Sentinel errors for failures that originate in the room or transport
// layer, not the engine. They mirror the role engine sentinels play in
// internal/game/errors.go: callers (mostly room.go and the transport
// pumps) return one of these instead of fabricating an ad-hoc
// OutError literal, and errorFor maps them to a stable wire.ErrorCode.
//
// Why have these at all? Without them, every site that wants to report
// "auth failed" would inline OutError{Code: "auth_failed", ...} — and
// the compiler can't tell us when the string drifts from what the
// client expects. Going through sentinels means a typo is a Go
// compile error.
var (
	// ErrAuthFailed is returned by a rejoin handshake when the
	// player ID is unknown or the secret doesn't match. We don't
	// distinguish the two cases on purpose: leaking "player exists,
	// wrong secret" vs "no such player" would help an attacker
	// enumerate slots.
	ErrAuthFailed = errors.New("room: unknown player or bad secret")

	// ErrNotJoined is returned when a subscriber issues a command
	// before completing the join/rejoin handshake (the room has no
	// PlayerID for it yet).
	ErrNotJoined = errors.New("room: join first")

	// ErrForbidden is returned when a subscriber is authenticated
	// but lacks the privilege for this command. Used for two cases:
	//   - Non-host issuing a host-only command (StartGame etc.)
	//   - Anyone issuing AdvancePhase, which is server-internal.
	// The Message on the resulting OutError is overridden per call
	// site so the user knows WHICH privilege they lack.
	ErrForbidden = errors.New("room: forbidden")

	// ErrBadFrame is returned by the transport when a websocket
	// frame is the wrong type (binary instead of text).
	ErrBadFrame = errors.New("room: expected text frame")

	// ErrBadMessage is returned by the transport when a frame's
	// payload doesn't parse as a valid client message: invalid
	// JSON, missing/blank "type" field, or an unknown type.
	// The Message on the resulting OutError carries the parse
	// detail.
	ErrBadMessage = errors.New("room: malformed client message")

	// ErrInternal is the catch-all for genuinely unexpected room
	// failures (e.g. crypto/rand returning an error during secret
	// generation). Should be rare; if it fires in production, the
	// fix is usually to add a more specific sentinel.
	ErrInternal = errors.New("room: internal error")
)
