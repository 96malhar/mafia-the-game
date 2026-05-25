package game

// Event is an immutable fact about something that has happened in the
// game. Events are produced by Apply and appended to the room's event log
// in order; they are the source of truth from which all state and all
// per-player views are derived.
//
// Event is a CLOSED interface (see Command for the rationale): only types
// in this package can satisfy it.
//
// Every event carries a Visibility tag so the projection layer (in a
// later step) can redact private information per player. The engine
// itself never hides anything — it always produces the full truth and
// lets the projection apply the redaction.
type Event interface {
	isEvent()

	// Visibility describes who is allowed to see this event. The engine
	// sets this; the projection enforces it.
	Visibility() Visibility
}

// Visibility describes the audience for an Event.
//
// A Visibility is one of:
//
//   - PublicTo("")           : everyone, including dead players & spectators.
//   - PrivateTo(playerID)    : only that single player.
//   - FactionOnly(faction)   : only living members of that faction.
//
// We model this as a single value (rather than three separate interface
// types) because it's small, comparable, and easy to JSON-encode.
type Visibility struct {
	// Audience is one of "public", "player", "faction".
	Audience string
	// Player is set when Audience == "player".
	Player PlayerID
	// Faction is set when Audience == "faction".
	Faction Faction
}

// Public is shorthand for an event visible to everyone.
func Public() Visibility { return Visibility{Audience: "public"} }

// PrivateTo restricts visibility to a single player.
func PrivateTo(p PlayerID) Visibility { return Visibility{Audience: "player", Player: p} }

// FactionOnly restricts visibility to living members of a faction.
func FactionOnly(f Faction) Visibility { return Visibility{Audience: "faction", Faction: f} }

// --- Concrete events ---------------------------------------------------

// GameCreated is the first event in every game's log.
type GameCreated struct {
	GameID GameID
	Roles  []Role
	Seed   int64
}

func (GameCreated) isEvent()               {}
func (GameCreated) Visibility() Visibility { return Public() }

// PlayerJoined records a successful lobby join.
type PlayerJoined struct {
	PlayerID PlayerID
	Name     string
}

func (PlayerJoined) isEvent()               {}
func (PlayerJoined) Visibility() Visibility { return Public() }

// GameStarted records the transition from Lobby to the first Night.
type GameStarted struct{}

func (GameStarted) isEvent()               {}
func (GameStarted) Visibility() Visibility { return Public() }

// RoleAssigned tells one player which role they were dealt. Private by
// construction — only the recipient sees this event.
type RoleAssigned struct {
	PlayerID PlayerID
	Role     Role
}

func (e RoleAssigned) isEvent()               {}
func (e RoleAssigned) Visibility() Visibility { return PrivateTo(e.PlayerID) }

// PhaseChanged records a phase transition and the (in-game) day number.
// Day 1 begins after the first night.
type PhaseChanged struct {
	From Phase
	To   Phase
	Day  int
}

func (PhaseChanged) isEvent()               {}
func (PhaseChanged) Visibility() Visibility { return Public() }

// NightActionRecorded acknowledges that a role-action was submitted. It
// is visible only to the actor's faction (so mafia members see each
// other's votes; the lone doctor / detective sees only their own).
type NightActionRecorded struct {
	Actor   PlayerID
	Target  PlayerID
	Faction Faction
}

func (e NightActionRecorded) isEvent()               {}
func (e NightActionRecorded) Visibility() Visibility { return FactionOnly(e.Faction) }

// PlayerKilled is emitted at Night -> Day if the mafia's target was not
// saved by the doctor. Always public.
type PlayerKilled struct {
	PlayerID PlayerID
}

func (PlayerKilled) isEvent()               {}
func (PlayerKilled) Visibility() Visibility { return Public() }

// PlayerSaved is emitted when the doctor's save cancels the mafia kill.
// Visible only to the doctor so the village can't deduce the role from
// public info.
type PlayerSaved struct {
	PlayerID PlayerID
	Doctor   PlayerID
}

func (e PlayerSaved) isEvent()               {}
func (e PlayerSaved) Visibility() Visibility { return PrivateTo(e.Doctor) }

// DetectiveResult delivers an investigation outcome privately to the
// detective.
type DetectiveResult struct {
	Detective PlayerID
	Target    PlayerID
	IsMafia   bool
}

func (e DetectiveResult) isEvent()               {}
func (e DetectiveResult) Visibility() Visibility { return PrivateTo(e.Detective) }

// VoteCast is a public day-vote tally update. Emitted once per vote
// submission so the UI can show live tallies.
type VoteCast struct {
	Voter  PlayerID
	Target PlayerID
}

func (VoteCast) isEvent()               {}
func (VoteCast) Visibility() Visibility { return Public() }

// VoteExtended is emitted at the end of PhaseDayVote when the tally has
// no strict plurality (a tie or no votes at all) and the day has not yet
// been extended. The vote tally is cleared and players are given another
// vote window before the day ends with no lynch.
type VoteExtended struct {
	Day int
}

func (VoteExtended) isEvent()               {}
func (VoteExtended) Visibility() Visibility { return Public() }

// PlayerLynched records the result of a day vote. The role of the
// lynched player is NOT revealed; that information is withheld until
// GameEnded.
type PlayerLynched struct {
	PlayerID PlayerID
}

func (PlayerLynched) isEvent()               {}
func (PlayerLynched) Visibility() Visibility { return Public() }

// GameEnded is the terminal event. Winner is one of FactionTown,
// FactionMafia. FinalRoles reveals every player's role to everyone — this
// is the only place roles become public, since per-death events
// (PlayerKilled, PlayerLynched) intentionally do not include them.
type GameEnded struct {
	Winner     Faction
	FinalRoles map[PlayerID]Role
}

func (GameEnded) isEvent()               {}
func (GameEnded) Visibility() Visibility { return Public() }
