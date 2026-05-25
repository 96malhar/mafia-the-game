// Package ws is the WebSocket transport for the Mafia server.
//
// It translates between the room layer's Go types (internal/room.inbound
// / internal/room.outbound) and a JSON wire format. The package owns
// nothing game-related; all decisions about state and visibility have
// already been made by the room before a message gets here.
//
// # Wire shape
//
// Every message in both directions uses a tagged envelope:
//
//	{ "type": "<tag>", "data": { ... } }
//
// The tag determines how data is decoded. The package exposes:
//
//   - Inbound types (client → server): clientMsg* in this file.
//   - Outbound types (server → client): serverMsg* in this file.
//
// Field names in JSON are lowerCamelCase. Times are RFC3339 strings.
// Players, roles, factions, and phases ride through as strings — same
// values as the engine's typed strings.
package ws

import (
	"encoding/json"
	"fmt"
)

// envelope is the outer JSON shape for every message in both directions.
// `Data` is held as raw JSON during decoding so we can dispatch on
// `Type` before fully unmarshalling.
type envelope struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data,omitempty"`
}

// --- Client → Server messages --------------------------------------------
//
// These mirror room.inbound but in JSON-friendly shapes. The handler
// translates each one into an internal/room.inbound before submitting.

// clientMsgType is the union of valid inbound `type` values. Centralized
// so the decoder can validate and unknown tags can be rejected cleanly.
type clientMsgType string

const (
	clientMsgJoin         clientMsgType = "join"
	clientMsgNightAction  clientMsgType = "nightAction"
	clientMsgVote         clientMsgType = "vote"
	clientMsgStartGame    clientMsgType = "startGame"
	clientMsgAdvancePhase clientMsgType = "advancePhase"
)

// clientJoinData is the payload of a "join" message. Rejoin is signalled
// via the WebSocket connect URL (?playerId=&secret=), not a separate
// message — keeping the post-upgrade flow uniform.
type clientJoinData struct {
	Name string `json:"name"`
}

// clientNightActionData carries a night action. Actor is omitted on the
// wire — the room rewrites it server-side to the authenticated PID.
type clientNightActionData struct {
	Target string `json:"target"`
}

// clientVoteData carries a day vote. Voter is server-side only.
// Target == "" means "retract my vote."
type clientVoteData struct {
	Target string `json:"target"`
}

// --- Server → Client messages --------------------------------------------
//
// These mirror room.outbound plus a few control messages the transport
// layer adds (ping/pong are handled at the websocket frame level by the
// library, so we don't model them here).

// serverMsgType is the union of valid outbound `type` values.
type serverMsgType string

const (
	serverMsgJoined   serverMsgType = "joined"
	serverMsgRejoined serverMsgType = "rejoined"
	serverMsgEvent    serverMsgType = "event"
	serverMsgError    serverMsgType = "error"
)

// serverJoinedData acknowledges a successful first-time join.
type serverJoinedData struct {
	PlayerID string `json:"playerId"`
	Secret   string `json:"secret"`
	RoomCode string `json:"roomCode"`
	IsHost   bool   `json:"isHost"`
}

// serverRejoinedData acknowledges a successful rejoin and includes the
// full filtered event log so the client can rebuild its view.
type serverRejoinedData struct {
	PlayerID string          `json:"playerId"`
	RoomCode string          `json:"roomCode"`
	IsHost   bool            `json:"isHost"`
	Events   []eventEnvelope `json:"events"`
}

// serverEventData carries one engine event, post-projection.
type serverEventData struct {
	Event eventEnvelope `json:"event"`
}

// serverErrorData reports a rejected command or a transport-layer error.
type serverErrorData struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// eventEnvelope is how engine events appear on the wire. Each event
// type becomes a tagged shape: { "type": "playerJoined", "data": {...} }.
//
// We don't lean on Go's reflection to derive this; we write it out
// explicitly in codec.go so the wire format is stable across refactors
// of the engine's event Go types.
type eventEnvelope struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data,omitempty"`
}

// --- helpers --------------------------------------------------------------

// errBadEnvelope is returned by decoders when the JSON shape doesn't
// match expectations. We wrap it with context at call sites.
type errBadEnvelope struct {
	reason string
}

func (e errBadEnvelope) Error() string { return "ws: bad envelope: " + e.reason }

func badEnvelopef(format string, args ...any) error {
	return errBadEnvelope{reason: fmt.Sprintf(format, args...)}
}
