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

	// LastSeq is the log high-water mark at join time (the count of events
	// preceding this join). The client adopts it as its resume cursor so a
	// reconnect right after joining sends an accurate ?since= — the backlog
	// events carry no per-event sequence, and the joiner's own PlayerJoined
	// arrives as the next streaming OutEvent with Seq = LastSeq+1.
	LastSeq int
	Events  []game.Event
}

func (OutJoined) isOutbound() {}

// OutRejoined acknowledges a successful rejoin. It is a cursor-driven
// resume: the client reconnects with the highest sequence it has already
// applied (?since=N), and the room replies with only the events after that
// point rather than re-shipping the entire log on every flap.
//
// FromSeq is the cursor the delta starts AFTER: when FromSeq > 0, Events is
// the projected tail since the client's cursor and the client appends it to
// its existing view; when FromSeq == 0 (a cold rejoin, an unknown/too-old
// cursor, or a post-reset rebaseline) Events is the full projected log and
// the client rebuilds from scratch. LastSeq is the room's current
// high-water mark (len of the log) — the client adopts it as its cursor
// once the batch is applied, so subsequent OutEvent.Seq values continue
// monotonically. Sent only to the rejoining subscriber.
type OutRejoined struct {
	PlayerID game.PlayerID
	Name     string
	RoomCode string
	IsHost   bool

	FromSeq int
	LastSeq int

	// Events is the projected slice the client should apply — the tail
	// since FromSeq, or the whole log when FromSeq == 0.
	Events []game.Event
}

func (OutRejoined) isOutbound() {}

// OutEvent carries one engine event, already passed through the
// per-player projection. This is the streaming channel — every state
// change in the game emits one or more OutEvents to each subscriber.
//
// Seq is the event's 1-based position in the room's canonical log (its
// index + 1). It is the same absolute sequence the cursor-resume protocol
// keys on: the client tracks the highest Seq it has applied and sends it as
// ?since=N on reconnect. Because events are projected per viewer, a given
// subscriber sees an increasing-but-gappy Seq stream (filtered events are
// skipped), which is exactly right — the gaps are events it was never
// allowed to see and never needs to catch up on.
type OutEvent struct {
	Seq   int
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
