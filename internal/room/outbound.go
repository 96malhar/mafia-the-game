package room

import "github.com/malhar/mafia-the-game/internal/game"

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
// localStorage) to reconnect later. Sent only to the joining subscriber.
type OutJoined struct {
	PlayerID game.PlayerID
	Secret   string
	RoomCode string
	IsHost   bool
}

func (OutJoined) isOutbound() {}

// OutRejoined acknowledges a successful rejoin and includes the full
// projected event log so the client can rebuild its view from scratch.
// Sent only to the rejoining subscriber.
type OutRejoined struct {
	PlayerID game.PlayerID
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
// subscriber only. Code is the sentinel name (e.g. "wrong_phase",
// "no_change"); Message is a human-readable explanation.
type OutError struct {
	Code    string
	Message string
}

func (OutError) isOutbound() {}
