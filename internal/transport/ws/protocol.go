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

	"github.com/96malhar/mafia-the-game/internal/wire"
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
// The string values are sourced from internal/wire so the sim and the
// server can't drift; the typed wrapper here just buys us
// exhaustive-switch ergonomics on the decoder side.
type clientMsgType string

const (
	clientMsgJoin          clientMsgType = wire.ClientMsgJoin
	clientMsgNightAction   clientMsgType = wire.ClientMsgNightAction
	clientMsgNightPass     clientMsgType = wire.ClientMsgNightPass
	clientMsgVote          clientMsgType = wire.ClientMsgVote
	clientMsgStartGame     clientMsgType = wire.ClientMsgStartGame
	clientMsgBeginNight    clientMsgType = wire.ClientMsgBeginNight
	clientMsgOpenVoting    clientMsgType = wire.ClientMsgOpenVoting
	clientMsgRevealVotes   clientMsgType = wire.ClientMsgRevealVotes
	clientMsgClearVotes    clientMsgType = wire.ClientMsgClearVotes
	clientMsgFinalizeVotes clientMsgType = wire.ClientMsgFinalizeVotes
	clientMsgSetMafia      clientMsgType = wire.ClientMsgSetMafia
	clientMsgSetConsort    clientMsgType = wire.ClientMsgSetConsort
	clientMsgSetVigilante  clientMsgType = wire.ClientMsgSetVigilante
	clientMsgSetYakuza     clientMsgType = wire.ClientMsgSetYakuza
	clientMsgRecruit       clientMsgType = wire.ClientMsgRecruit
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

// clientSetMafiaData carries a host-driven adjustment to the planned
// mafia count during PhaseLobby. The engine validates the range; the
// transport just forwards the number.
type clientSetMafiaData struct {
	Count int `json:"count"`
}

// clientSetConsortData carries a host-driven toggle of the optional
// Consort role during PhaseLobby. The engine validates phase/no-op; the
// transport just forwards the flag.
type clientSetConsortData struct {
	Enabled bool `json:"enabled"`
}

// clientSetVigilanteData carries a host-driven toggle of the optional
// Vigilante role during PhaseLobby. The engine validates phase/no-op;
// the transport just forwards the flag.
type clientSetVigilanteData struct {
	Enabled bool `json:"enabled"`
}

// clientSetYakuzaData carries a host-driven toggle of the optional Yakuza
// role during PhaseLobby. The engine validates phase/no-op; the transport
// just forwards the flag.
type clientSetYakuzaData struct {
	Enabled bool `json:"enabled"`
}

// clientRecruitData carries the Yakuza's recruit target. Actor is omitted
// on the wire — the room rewrites it server-side to the authenticated PID,
// like clientNightActionData.
type clientRecruitData struct {
	Target string `json:"target"`
}

// --- Server → Client messages --------------------------------------------
//
// These mirror room.outbound plus a few control messages the transport
// layer adds (ping/pong are handled at the websocket frame level by the
// library, so we don't model them here).

// serverMsgType is the union of valid outbound `type` values.
// String values sourced from internal/wire (see clientMsgType note).
type serverMsgType string

const (
	serverMsgJoined   serverMsgType = wire.ServerMsgJoined
	serverMsgRejoined serverMsgType = wire.ServerMsgRejoined
	serverMsgEvent    serverMsgType = wire.ServerMsgEvent
	serverMsgError    serverMsgType = wire.ServerMsgError
)

// serverJoinedData acknowledges a successful first-time join.
//
// Events carries the projected event log of everything that happened
// before this join, so a late joiner can reconstruct existing state
// (notably: who else is in the room) without polling. The PlayerJoined
// event for THIS join is NOT in Events; it arrives as a normal "event"
// frame immediately after.
type serverJoinedData struct {
	PlayerID string          `json:"playerId"`
	Name     string          `json:"name"`
	Secret   string          `json:"secret"`
	RoomCode string          `json:"roomCode"`
	IsHost   bool            `json:"isHost"`
	Events   []eventEnvelope `json:"events"`
}

// serverRejoinedData acknowledges a successful rejoin and includes the
// full filtered event log so the client can rebuild its view.
type serverRejoinedData struct {
	PlayerID string          `json:"playerId"`
	Name     string          `json:"name"`
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
