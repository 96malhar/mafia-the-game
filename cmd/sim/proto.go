package main

import "encoding/json"

// The sim treats the server as a black box reachable via the public
// JSON wire format. We intentionally re-declare the message shapes here
// rather than importing them from internal/transport/ws — this forces
// any future protocol change to be conscious and bidirectional.

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
	PlayerID string `json:"playerId"`
	Secret   string `json:"secret"`
	RoomCode string `json:"roomCode"`
	IsHost   bool   `json:"isHost"`
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

type evNightTurnStarted struct {
	Role     string `json:"role"`
	Deadline int64  `json:"deadline"`
	Phantom  bool   `json:"phantom"`
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

// Wire-tag constants. Keep these stable; they must match the server's
// emitters in internal/transport/ws/codec.go.
const (
	msgJoined = "joined"
	msgEvent  = "event"
	msgError  = "error"

	evTagPlayerJoined     = "playerJoined"
	evTagRoleAssigned     = "roleAssigned"
	evTagPhaseChanged     = "phaseChanged"
	evTagNightTurnStarted = "nightTurnStarted"
	evTagPlayerKilled     = "playerKilled"
	evTagPlayerLynched    = "playerLynched"
	evTagDetectiveResult  = "detectiveResult"
	evTagGameEnded        = "gameEnded"
)

// Phase string constants — these correspond to game.Phase values.
const (
	phaseLobby         = "lobby"
	phaseNight         = "night"
	phaseDayDiscussion = "day_discussion"
	phaseDayVote       = "day_vote"
	phaseEnded         = "ended"
)

// Role string constants.
const (
	roleVillager  = "villager"
	roleMafia     = "mafia"
	roleDoctor    = "doctor"
	roleDetective = "detective"
)
