package room

import "github.com/malhar/mafia-the-game/internal/game"

// outbound is the closed sum type of messages a room sends back to a
// subscriber. The transport layer (3c) will JSON-encode these; tests
// receive them as Go values.
//
// The four outbound shapes split along intent:
//   - outJoined / outRejoined  : one-shot replies to a join attempt.
//   - outEvent                  : the streaming game-event channel.
//   - outError                  : per-command rejection sent only to
//     the originating subscriber.
type outbound interface {
	isOutbound()
}

// outJoined acknowledges a successful first-time join. The Secret is
// the rejoin credential; the client must remember it (typically in
// localStorage) to reconnect later. Sent only to the joining subscriber.
type outJoined struct {
	PlayerID game.PlayerID
	Secret   string
	RoomCode string
	IsHost   bool
}

func (outJoined) isOutbound() {}

// outRejoined acknowledges a successful rejoin and includes the full
// projected event log so the client can rebuild its view from scratch.
// Sent only to the rejoining subscriber.
type outRejoined struct {
	PlayerID game.PlayerID
	RoomCode string
	IsHost   bool

	// Events is the entire event log filtered through the projection
	// for this player — i.e. everything they have ever been allowed
	// to see, in order.
	Events []game.Event
}

func (outRejoined) isOutbound() {}

// outEvent carries one engine event, already passed through the
// per-player projection. This is the streaming channel — every state
// change in the game emits one or more outEvents to each subscriber.
type outEvent struct {
	Event game.Event
}

func (outEvent) isOutbound() {}

// outError reports a rejected command back to the originating
// subscriber only. Code is the sentinel name (e.g. "wrong_phase",
// "no_change"); Message is a human-readable explanation.
type outError struct {
	Code    string
	Message string
}

func (outError) isOutbound() {}
