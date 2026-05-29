package main

import (
	"encoding/json"

	"github.com/malhar/mafia-the-game/internal/wire"
)

// The sim treats the server as a black box reachable via the public
// JSON wire format. We intentionally re-declare the message *shapes*
// (structs) here rather than importing them from
// internal/transport/ws — this forces any future shape change to be
// conscious and bidirectional. But the *string tags* (message types,
// event names, phase/role names) come from internal/wire so the sim
// and server can't drift on those.

// envelope is the outer JSON shape every message uses in both
// directions.
type envelope struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data,omitempty"`
}

// --- Client → Server (outgoing) ------------------------------------------

type clientJoin struct {
	Name string `json:"name"`
}

type clientNightAction struct {
	Target string `json:"target"`
}

type clientVote struct {
	Target string `json:"target"`
}

// --- Server → Client (incoming) ------------------------------------------

type serverJoined struct {
	PlayerID string          `json:"playerId"`
	Name     string          `json:"name"`
	Secret   string          `json:"secret"`
	RoomCode string          `json:"roomCode"`
	IsHost   bool            `json:"isHost"`
	Events   []eventEnvelope `json:"events"`
}

type serverEvent struct {
	Event eventEnvelope `json:"event"`
}

type serverError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// eventEnvelope mirrors the engine event envelope. Concrete event
// payloads are decoded lazily — we keep Data raw and unmarshal only
// the shapes the sim actually reacts to.
type eventEnvelope struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data,omitempty"`
}

// Event payload shapes the sim consumes. We include only the fields
// our strategies need; extra fields in the wire payload are ignored.

type evPlayerJoined struct {
	PlayerID string `json:"playerId"`
	Name     string `json:"name"`
}

type evRoleAssigned struct {
	PlayerID string `json:"playerId"`
	Role     string `json:"role"`
}

type evPhaseChanged struct {
	From string `json:"from"`
	To   string `json:"to"`
	Day  int    `json:"day"`
}

// evNightActionStarted mirrors wire.EventNightActionStarted — the
// sub-phase event the sim's bot listens for to know when its act
// window has opened. We intentionally don't model the other five
// night sub-phase events here: the bot doesn't need to react to
// narrate / opening / ponder / sleep / settle since the server's
// timers drive their transitions and submission is only valid
// during act anyway.
type evNightActionStarted struct {
	Role     string `json:"role"`
	Day      int    `json:"day"`
	Deadline int64  `json:"deadline"`
}

type evPlayerKilled struct {
	PlayerID string `json:"playerId"`
}

type evPlayerLynched struct {
	PlayerID string `json:"playerId"`
}

type evDetectiveResult struct {
	Detective string `json:"detective"`
	Target    string `json:"target"`
	IsMafia   bool   `json:"isMafia"`
}

type evGameEnded struct {
	Winner     string            `json:"winner"`
	FinalRoles map[string]string `json:"finalRoles"`
}

// Local aliases re-export the wire constants under the sim's
// historical lower-case names so the bot / strategy code stays
// readable. The single source of truth lives in internal/wire.
const (
	msgJoined = wire.ServerMsgJoined
	msgEvent  = wire.ServerMsgEvent
	msgError  = wire.ServerMsgError

	evTagPlayerJoined       = wire.EventPlayerJoined
	evTagRoleAssigned       = wire.EventRoleAssigned
	evTagPhaseChanged       = wire.EventPhaseChanged
	evTagNightActionStarted = wire.EventNightActionStarted
	evTagPlayerKilled       = wire.EventPlayerKilled
	evTagPlayerLynched      = wire.EventPlayerLynched
	evTagNoLynch            = wire.EventNoLynch
	evTagDetectiveResult    = wire.EventDetectiveResult
	evTagVotesRevealed      = wire.EventVotesRevealed
	evTagVoteCleared        = wire.EventVoteCleared
	evTagGameEnded          = wire.EventGameEnded

	phaseLobby         = wire.PhaseLobby
	phaseNight         = wire.PhaseNight
	phaseDayDiscussion = wire.PhaseDayDiscussion
	phaseDayVote       = wire.PhaseDayVote
	phaseEnded         = wire.PhaseEnded

	roleVillager  = wire.RoleVillager
	roleMafia     = wire.RoleMafia
	roleDoctor    = wire.RoleDoctor
	roleDetective = wire.RoleDetective
)
