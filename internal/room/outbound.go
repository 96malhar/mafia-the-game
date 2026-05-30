package room

import (
	"github.com/96malhar/mafia-the-game/internal/game"
	"github.com/96malhar/mafia-the-game/internal/wire"
)

// Outbound is the closed sum type of messages a room sends back to a
// subscriber. The transport layer JSON-encodes these; tests receive
// them as Go values.
//
// Concrete shapes:
//   - OutJoined / OutRejoined : one-shot replies to a join attempt.
//   - OutEvent                : the streaming game-event channel.
//   - OutError                : per-command rejection sent only to
//     the originating subscriber.
//
// New shapes must be added here AND extend the type switch in
// transport/ws.encodeOutbound. The closed-interface marker (the
// unexported isOutbound method) keeps that obligation enforceable.
type Outbound interface {
	isOutbound()
}

// OutJoined acknowledges a successful first-time join. The Secret is
// the rejoin credential; the client must remember it (typically in
// sessionStorage) to reconnect later. Sent only to the joining
// subscriber.
//
// Name echoes back the display name the player chose. The server is
// the source of truth for player identity (display name included), so
// rejoiners receive the same field without needing to remember it
// client-side.
//
// Events is the projected event log of everything that happened in
// the room BEFORE this join, filtered through the joiner's projection.
// This lets a late joiner reconstruct who else is already in the room
// (and any other public state) without re-querying. The PlayerJoined
// event for THIS join is NOT in Events — it is broadcast separately
// to all subscribers (including the joiner) immediately after.
type OutJoined struct {
	PlayerID game.PlayerID
	Name     string
	Secret   string
	RoomCode string
	IsHost   bool
	Events   []game.Event
}

func (OutJoined) isOutbound() {}

// OutRejoined acknowledges a successful rejoin and includes the full
// projected event log so the client can rebuild its view from scratch.
// Sent only to the rejoining subscriber.
type OutRejoined struct {
	PlayerID game.PlayerID
	Name     string
	RoomCode string
	IsHost   bool

	// Events is the entire event log filtered through the projection
	// for this player — i.e. everything they have ever been allowed
	// to see, in order.
	Events []game.Event
}

func (OutRejoined) isOutbound() {}

// OutEvent carries one engine event, already passed through the
// per-player projection. This is the streaming channel — every state
// change in the game emits one or more OutEvents to each subscriber.
type OutEvent struct {
	Event game.Event
}

func (OutEvent) isOutbound() {}

// OutError reports a rejected command back to the originating
// subscriber only.
//
// Code is the machine-readable tag the client switches on; its set is
// closed and lives in internal/wire (wire.ErrorCode). Message is the
// human-readable explanation rendered as-is by clients — including
// any per-call-site overrides applied by joinErrorFor for the join
// handshake.
type OutError struct {
	Code    wire.ErrorCode
	Message string
}

func (OutError) isOutbound() {}
