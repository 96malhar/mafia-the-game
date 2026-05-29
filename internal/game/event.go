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

// GameCreated is the first event in every game's log. It carries the
// initial lobby configuration: min/max player counts and the initial
// mafia count. The actual per-player roster is composed at StartGame
// from these values, so GameCreated does NOT carry a Roles slice.
type GameCreated struct {
	GameID     GameID
	MinPlayers int
	MaxPlayers int
	MafiaCount int
	Seed       int64
}

func (GameCreated) isEvent()               {}
func (GameCreated) Visibility() Visibility { return Public() }

// MafiaCountChanged records a host-driven tweak to the planned mafia
// count during PhaseLobby. Public so every observer can see the
// configured composition update in real time.
type MafiaCountChanged struct {
	From int
	To   int
}

func (MafiaCountChanged) isEvent()               {}
func (MafiaCountChanged) Visibility() Visibility { return Public() }

// PlayerJoined records a successful lobby join.
type PlayerJoined struct {
	PlayerID PlayerID
	Name     string
}

func (PlayerJoined) isEvent()               {}
func (PlayerJoined) Visibility() Visibility { return Public() }

// HostChanged announces who the room's host is.
//
// "Host" is a ROOM-LAYER concept (the room picks the first joiner; the
// engine doesn't care who can issue host-only commands — that check
// lives in room.dispatch). This event type lives in the game package
// only because game.Event is a closed interface, and HostChanged
// rides the same event log so it gets the same projection, replay,
// and wire-encoding for free.
//
// Emitted by internal/room when r.host is assigned. The engine never
// produces HostChanged — searching for it in internal/game/rules*.go
// would correctly return nothing.
//
// Public visibility: every player should see the host badge, and
// future spectators too.
type HostChanged struct {
	PlayerID PlayerID
}

func (HostChanged) isEvent()               {}
func (HostChanged) Visibility() Visibility { return Public() }

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

// NightOpeningStarted announces the one-shot start-of-night beat that
// precedes the first role's narrate. Carries the "City, go to sleep."
// cue plus a fixed pre-wake silence so the room has time to settle
// before any role is named. No action accepted; currentNightRole is
// empty during this sub-phase.
//
// Emitted by applyBeginNight immediately after PhaseChanged{To: Night}.
// After Deadline elapses, AdvancePhase pops the first role from the
// night queue and enters its NightSubNarrate.
//
// Public visibility: every observer hears the cue.
type NightOpeningStarted struct {
	Day      int
	Deadline int64 // unix-millis; 0 means "engine timeless; room will stamp"
}

func (NightOpeningStarted) isEvent()               {}
func (NightOpeningStarted) Visibility() Visibility { return Public() }

// NightNarrationStarted announces the start of a role's "wake up"
// audio cue. Sized to cover the spoken prompt. No action accepted.
// Carries the Role (so the client narrates the right script) and
// a wall-clock Deadline (unix-millis) at which the sub-phase auto-
// elapses. Day is carried explicitly so clients can replay a
// projection without re-deriving it from the surrounding events.
//
// Phantom is true when no living player holds Role at the time the
// narrate starts. Narration still plays (so the room can't deduce
// which role is dead from missing audio), but the subsequent sub-
// phase will be NightPonderStarted (with a randomized duration)
// instead of NightActionStarted — see NightSubPhase.
//
// Public visibility: every observer sees the cue land.
type NightNarrationStarted struct {
	Role     Role
	Day      int
	Deadline int64 // unix-millis; 0 means "engine timeless; room will stamp"
	Phantom  bool
}

func (NightNarrationStarted) isEvent()               {}
func (NightNarrationStarted) Visibility() Visibility { return Public() }

// NightActionStarted announces that the actor's decision window is now
// open. Emitted only for non-phantom turns (phantom turns substitute
// NightPonderStarted directly after narrate). NightAction submissions
// from the current role are accepted between this event and either
// the engine emitting NightPonderStarted (early submission) or the
// room driving AdvancePhase at Deadline (timeout).
//
// Public so all clients can render countdown chrome; only the actor's
// client renders the Target buttons.
type NightActionStarted struct {
	Role     Role
	Day      int
	Deadline int64 // unix-millis; 0 means "engine timeless; room will stamp"
}

func (NightActionStarted) isEvent()               {}
func (NightActionStarted) Visibility() Visibility { return Public() }

// NightPonderStarted is the post-act / phantom-substitute pause. For
// real turns where the actor submitted, this is a short fixed beat
// (default 2s) that gives the room a moment to absorb the action
// before sleep. For phantom turns, this is a randomized 5–10s pause
// that stands in for the missing act window — making the night
// cadence statistically indistinguishable from a real turn so the
// city can't deduce a role is dead.
//
// No action accepted in this sub-phase. Real turns that timed out
// (no submission) skip ponder and go straight to sleep.
type NightPonderStarted struct {
	Role     Role
	Day      int
	Deadline int64 // unix-millis; 0 means "engine timeless; room will stamp"
	Phantom  bool
}

func (NightPonderStarted) isEvent()               {}
func (NightPonderStarted) Visibility() Visibility { return Public() }

// NightSleepStarted announces the start of a role's "go to sleep"
// audio cue. Sized to cover the spoken prompt. No action accepted.
// Public so every observer hears the cue land.
type NightSleepStarted struct {
	Role     Role
	Day      int
	Deadline int64 // unix-millis; 0 means "engine timeless; room will stamp"
}

func (NightSleepStarted) isEvent()               {}
func (NightSleepStarted) Visibility() Visibility { return Public() }

// NightSettleStarted is the post-sleep pause before the next role's
// narrate (or, for the last role of the night, before the
// night→day_discussion transition). A short fixed beat (default 2s)
// that lets the "go to sleep" cue land cleanly before the next
// narrator line begins. No action accepted.
type NightSettleStarted struct {
	Role     Role
	Day      int
	Deadline int64 // unix-millis; 0 means "engine timeless; room will stamp"
}

func (NightSettleStarted) isEvent()               {}
func (NightSettleStarted) Visibility() Visibility { return Public() }

// NightSubPhaseEvent is implemented by every Night*Started event — the
// family that opens a night sub-phase and carries a wall-clock
// Deadline the engine emits as 0 for the room to stamp. Grouping them
// behind one interface lets the room layer stamp the deadline and
// recognize a sub-phase transition with a single interface assertion
// instead of a parallel type-switch over all six concretes that would
// silently miss a newly-added event. Adding a sub-phase event means
// implementing WithDeadline (one method), and it automatically joins
// the family everywhere the interface is used.
//
// (subPhaseDuration in the room layer and encodeEvent in the transport
// still switch per concrete type — they genuinely need per-event
// behavior, sizing and wire shape respectively — so this interface
// doesn't try to absorb those.)
type NightSubPhaseEvent interface {
	Event
	// WithDeadline returns a copy of the event with its Deadline set
	// to ms (unix-millis), but only if it was still 0 (unstamped).
	// Value receiver + return-copy keeps events immutable.
	WithDeadline(ms int64) Event
}

func (e NightOpeningStarted) WithDeadline(ms int64) Event {
	if e.Deadline == 0 {
		e.Deadline = ms
	}
	return e
}

func (e NightNarrationStarted) WithDeadline(ms int64) Event {
	if e.Deadline == 0 {
		e.Deadline = ms
	}
	return e
}

func (e NightActionStarted) WithDeadline(ms int64) Event {
	if e.Deadline == 0 {
		e.Deadline = ms
	}
	return e
}

func (e NightPonderStarted) WithDeadline(ms int64) Event {
	if e.Deadline == 0 {
		e.Deadline = ms
	}
	return e
}

func (e NightSleepStarted) WithDeadline(ms int64) Event {
	if e.Deadline == 0 {
		e.Deadline = ms
	}
	return e
}

func (e NightSettleStarted) WithDeadline(ms int64) Event {
	if e.Deadline == 0 {
		e.Deadline = ms
	}
	return e
}

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

// VoteCast is emitted when a voter casts their first vote of the current
// PhaseDayVote. Subsequent changes by the same voter emit VoteChanged;
// retractions emit VoteRetracted.
type VoteCast struct {
	Voter  PlayerID
	Target PlayerID
}

func (VoteCast) isEvent()               {}
func (VoteCast) Visibility() Visibility { return Public() }

// VoteChanged is emitted when a voter who already had an active vote
// switches to a different target. It carries both old and new targets so
// the UI and replays can describe the move without state reconstruction.
type VoteChanged struct {
	Voter PlayerID
	From  PlayerID
	To    PlayerID
}

func (VoteChanged) isEvent()               {}
func (VoteChanged) Visibility() Visibility { return Public() }

// VoteRetracted is emitted when a voter withdraws their active vote
// without immediately picking another target (i.e. DayVote{Target: ""}).
type VoteRetracted struct {
	Voter PlayerID
	Was   PlayerID
}

func (VoteRetracted) isEvent()               {}
func (VoteRetracted) Visibility() Visibility { return Public() }

// VoteCleared is emitted when the host calls ClearVotes during
// PhaseDayVote. The in-flight tally is wiped and clients re-render
// with a fresh empty board so the room can vote again.
type VoteCleared struct {
	Day int
}

func (VoteCleared) isEvent()               {}
func (VoteCleared) Visibility() Visibility { return Public() }

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
