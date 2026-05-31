// Package wire holds the stable string tags that ride on the JSON
// wire between the Mafia server and any client (the browser front-
// end, the sim's bot harness, future native clients).
//
// What lives here:
//   - Client → server message type tags (the "type" field of inbound
//     envelopes, e.g. "join", "nightAction").
//   - Server → client message type tags (the "type" field of outbound
//     envelopes, e.g. "joined", "event", "error").
//   - Engine event tags (the "type" field of the inner event envelope
//     carried inside a server "event" message, e.g. "playerJoined").
//   - Stable string representations of game-domain enums (Role,
//     Phase, Faction) that show up on the wire.
//
// What does NOT live here:
//   - Go DTOs (clientJoinData / serverJoinedData / etc). Those are
//     transport-internal Go types in internal/transport/ws. The sim
//     intentionally redeclares its own to assert the contract from
//     outside as a black-box client.
//   - JSON encoding helpers. Each side owns its own marshalling.
//
// Anything declared here must keep its on-the-wire string value
// indefinitely; clients in the wild may persist it (e.g. in a
// session-storage event log). Adding new tags is fine; renaming or
// removing an existing one is a compatibility break.
package wire

// --- Client → server message types ---------------------------------------

const (
	ClientMsgJoin          = "join"
	ClientMsgNightAction   = "nightAction"
	ClientMsgVote          = "vote"
	ClientMsgStartGame     = "startGame"
	ClientMsgBeginNight    = "beginNight"
	ClientMsgOpenVoting    = "openVoting"
	ClientMsgRevealVotes   = "revealVotes"
	ClientMsgClearVotes    = "clearVotes"
	ClientMsgFinalizeVotes = "finalizeVotes"
	ClientMsgSetMafia      = "setMafia"
	ClientMsgSetConsort    = "setConsort"
)

// --- Server → client message types ---------------------------------------

const (
	ServerMsgJoined   = "joined"
	ServerMsgRejoined = "rejoined"
	ServerMsgEvent    = "event"
	ServerMsgError    = "error"
)

// --- Engine event tags ---------------------------------------------------
//
// These ride inside the "data.event.type" field of a ServerMsgEvent
// envelope. Every game.Event type the server emits has exactly one
// stable tag here; encodeEvent in internal/transport/ws maps Go
// types to these tags.

const (
	EventGameCreated           = "gameCreated"
	EventMafiaCountChanged     = "mafiaCountChanged"
	EventPlayerJoined          = "playerJoined"
	EventHostChanged           = "hostChanged"
	EventGameStarted           = "gameStarted"
	EventRoleAssigned          = "roleAssigned"
	EventMafiaRoster           = "mafiaRoster"
	EventConsortChanged        = "consortChanged"
	EventBlocked               = "blocked"
	EventConsortPromoted       = "consortPromoted"
	EventPhaseChanged          = "phaseChanged"
	EventNightOpeningStarted   = "nightOpeningStarted"
	EventNightNarrationStarted = "nightNarrationStarted"
	EventNightActionStarted    = "nightActionStarted"
	EventNightPonderStarted    = "nightPonderStarted"
	EventNightSleepStarted     = "nightSleepStarted"
	EventNightSettleStarted    = "nightSettleStarted"
	EventNightActionRecorded   = "nightActionRecorded"
	EventPlayerKilled          = "playerKilled"
	EventPlayerSaved           = "playerSaved"
	EventDetectiveResult       = "detectiveResult"
	EventVoteCast              = "voteCast"
	EventVoteChanged           = "voteChanged"
	EventVoteRetracted         = "voteRetracted"
	EventVotesRevealed         = "votesRevealed"
	EventVoteCleared           = "voteCleared"
	EventPlayerLynched         = "playerLynched"
	EventNoLynch               = "noLynch"
	EventGameEnded             = "gameEnded"
)

// --- Phase strings -------------------------------------------------------
//
// Must mirror the values of game.Phase constants. Defined here too so
// the sim and any future external client don't have to import the
// engine package just to recognize a phase string.

const (
	PhaseLobby         = "lobby"
	PhaseNight         = "night"
	PhaseDayDiscussion = "day_discussion"
	PhaseDayVote       = "day_vote"
	PhaseEnded         = "ended"
)

// --- Role strings --------------------------------------------------------
//
// Must mirror the values of game.Role constants.

const (
	RoleVillager  = "villager"
	RoleMafia     = "mafia"
	RoleDetective = "detective"
	RoleDoctor    = "doctor"
)

// --- Faction strings -----------------------------------------------------
//
// Must mirror the values of game.Faction constants.

const (
	FactionTown  = "town"
	FactionMafia = "mafia"
)
