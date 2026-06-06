// Package wire holds the stable string tags that ride on the JSON
// wire between the Mafia server and any client (the browser front-
// end, future native clients).
//
// What lives here:
//   - Client → server message type tags (the "type" field of inbound
//     envelopes, e.g. "join", "nightAction").
//   - Server → client message type tags (the "type" field of outbound
//     envelopes, e.g. "joined", "event", "error").
//   - Engine event tags (the "type" field of the inner event envelope
//     carried inside a server "event" message, e.g. "playerJoined").
//
// What does NOT live here:
//   - Go DTOs (clientJoinData / serverJoinedData / etc). Those are
//     transport-internal Go types in internal/transport/ws.
//   - The on-wire spellings of domain enums (Role, Phase, Faction).
//     Those ARE the engine's own string-typed values (game.Role etc.):
//     the codec writes them straight to the wire via string(v.Role), so
//     the engine is the single source of truth and there is no separate
//     mirror to keep in sync.
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
	ClientMsgNightPass     = "nightPass"
	ClientMsgVote          = "vote"
	ClientMsgStartGame     = "startGame"
	ClientMsgBeginNight    = "beginNight"
	ClientMsgOpenVoting    = "openVoting"
	ClientMsgRevealVotes   = "revealVotes"
	ClientMsgClearVotes    = "clearVotes"
	ClientMsgFinalizeVotes = "finalizeVotes"
	ClientMsgSetMafia      = "setMafia"
	ClientMsgSetConsort    = "setConsort"
	ClientMsgSetVigilante  = "setVigilante"
	ClientMsgSetYakuza     = "setYakuza"
	ClientMsgSetTracker    = "setTracker"
	ClientMsgRecruit       = "recruit"
	ClientMsgResetGame     = "resetGame"
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
	EventVigilanteChanged      = "vigilanteChanged"
	EventYakuzaChanged         = "yakuzaChanged"
	EventTrackerChanged        = "trackerChanged"
	EventBlocked               = "blocked"
	EventRecruited             = "recruited"
	EventRecruitRecorded       = "recruitRecorded"
	EventConsortPromoted       = "consortPromoted"
	EventPhaseChanged          = "phaseChanged"
	EventNightOpeningStarted   = "nightOpeningStarted"
	EventNightNarrationStarted = "nightNarrationStarted"
	EventNightActionStarted    = "nightActionStarted"
	EventNightPonderStarted    = "nightPonderStarted"
	EventNightSleepStarted     = "nightSleepStarted"
	EventNightSettleStarted    = "nightSettleStarted"
	EventNightActionRecorded   = "nightActionRecorded"
	EventSpectatorNightAction  = "spectatorNightAction"
	EventPlayerKilled          = "playerKilled"
	EventDetectiveResult       = "detectiveResult"
	EventTrackerResult         = "trackerResult"
	EventVoteCast              = "voteCast"
	EventVoteChanged           = "voteChanged"
	EventVoteRetracted         = "voteRetracted"
	EventVotesRevealed         = "votesRevealed"
	EventVoteCleared           = "voteCleared"
	EventPlayerLynched         = "playerLynched"
	EventNoLynch               = "noLynch"
	EventRosterRevealed        = "rosterRevealed"
	EventGameEnded             = "gameEnded"
	EventGameReset             = "gameReset"
)

// Note: the on-wire spellings of domain enums (Role, Phase, Faction) are
// intentionally NOT mirrored here. They are the engine's own
// string-typed values (game.Role / game.Phase / game.Faction); the codec
// writes them to the wire directly via string(v.Role), so the engine is
// the single source of truth.
