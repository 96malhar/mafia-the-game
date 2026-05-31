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

// ConsortChanged records a host-driven toggle of the optional Consort
// role during PhaseLobby. Public so every observer sees the configured
// composition update in real time (it does NOT reveal who, if anyone,
// will be dealt the role — that stays secret until GameEnded).
type ConsortChanged struct {
	Enabled bool
}

func (ConsortChanged) isEvent()               {}
func (ConsortChanged) Visibility() Visibility { return Public() }

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

// MafiaRosterRevealed tells the mafia who their fellow mafia are. Emitted
// once at StartGame so every mafioso knows their whole team from the
// outset. In the in-person game this is the "mafia, open your eyes and
// recognize each other" beat; in the remote UI we surface it explicitly
// since players can't physically see the table.
//
// Members lists every mafia PlayerID (including the recipient's own).
// FactionOnly so only LIVING mafia ever receive it — town never sees the
// roster, and a dead mafioso loses access on rejoin like other faction
// secrets (NightActionRecorded). Roles still stay hidden from town until
// GameEnded; this only ever widens knowledge within the mafia faction.
type MafiaRosterRevealed struct {
	Members []PlayerID
}

func (MafiaRosterRevealed) isEvent()               {}
func (MafiaRosterRevealed) Visibility() Visibility { return FactionOnly(FactionMafia) }

// PhaseChanged records a phase transition and the (in-game) day number.
// Day 1 begins after the first night.
type PhaseChanged struct {
	From Phase
	To   Phase
	Day  int
}

func (PhaseChanged) isEvent()               {}
func (PhaseChanged) Visibility() Visibility { return Public() }

// NightSubPhaseStarted announces the start of one night sub-phase. A
// single event type carries every sub-phase — opening, narrate, act,
// ponder, sleep, settle — distinguished by the Sub field. This
// replaces what used to be six near-identical event types
// (Night{Opening,Narration,Action,Ponder,Sleep,Settle}Started); they
// all shared the same {Role, Day, Deadline, Phantom} shape and forced
// six parallel definitions in the encoder, the duration table, and the
// WithDeadline boilerplate. Collapsing to one type + a Sub enum keeps
// the per-sub-phase knowledge in exactly the two places that genuinely
// need it: the room's duration table (subPhaseDuration) and the wire
// encoder (encodeEvent, which still maps each Sub to its own stable
// wire tag so existing clients are unaffected).
//
// Public visibility: every observer sees/hears the cue land. Only the
// acting player's client renders Target buttons (during Sub==act), but
// that gating is client-side; the event itself is public so all
// clients can render countdown chrome from the stamped Deadline.
type NightSubPhaseStarted struct {
	// Sub identifies which sub-phase began. See NightSubPhase for the
	// per-role state machine these step through.
	Sub NightSubPhase

	// Role is the role whose turn this sub-phase belongs to. Empty
	// during NightSubOpening — the night-scoped beat that precedes any
	// role's turn (currentNightRole is unset then).
	Role Role

	// Day is the in-game day number, carried explicitly so clients can
	// replay a projection without re-deriving it from surrounding
	// events.
	Day int

	// Deadline is the unix-millis wall-clock instant at which the
	// sub-phase auto-elapses. The engine emits 0 (it is timeless); the
	// room stamps a real value before broadcasting so all viewers —
	// current and future via the replayed log — agree on the timing.
	Deadline int64

	// Phantom is true when no living player holds Role at the time the
	// sub-phase starts. It is meaningful for narrate (narration still
	// plays, so the room can't deduce a dead role from missing audio)
	// and for ponder (the randomized phantom-substitute window that
	// stands in for a missing act). It is always false for opening (no
	// role) and for the sub-phases of a living role.
	Phantom bool
}

func (NightSubPhaseStarted) isEvent()               {}
func (NightSubPhaseStarted) Visibility() Visibility { return Public() }

// WithDeadline returns a copy of the event with its Deadline set to ms
// (unix-millis), but only if it was still 0 (unstamped). Value receiver
// + return-copy keeps events immutable. The room layer calls this to
// stamp a wall-clock deadline onto the engine's timeless event before
// broadcasting (see stampNightDeadlines in internal/room).
func (e NightSubPhaseStarted) WithDeadline(ms int64) Event {
	if e.Deadline == 0 {
		e.Deadline = ms
	}
	return e
}

// NightActionRecorded acknowledges that a role-action was submitted.
//
// Visibility depends on whether the action belongs to a collective or a
// solo role:
//
//   - Mafia acts as a faction: co-mafia (including those who didn't
//     submit) must see the locked kill to coordinate, so the ack is
//     faction-scoped.
//   - The doctor and detective are solo, but they share the single
//     FactionTown with the villagers. Scoping their ack to FactionTown
//     would broadcast the target (and actor) to the entire town, which
//     defeats their hidden roles. They get a private self-ack instead.
type NightActionRecorded struct {
	Actor   PlayerID
	Target  PlayerID
	Faction Faction
}

func (e NightActionRecorded) isEvent() {}
func (e NightActionRecorded) Visibility() Visibility {
	if e.Faction == FactionMafia {
		return FactionOnly(FactionMafia)
	}
	return PrivateTo(e.Actor)
}

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

// Blocked tells a player that the Consort nullified their night action.
// Emitted only when a real action was actually cancelled (a blocked
// doctor's save or detective's investigation) — never for a blocked
// villager (no action to lose) or a blocked mafioso (the kill is
// immune). Private to the blocked player; the room must never learn who
// the consort targeted.
type Blocked struct {
	PlayerID PlayerID
}

func (e Blocked) isEvent()               {}
func (e Blocked) Visibility() Visibility { return PrivateTo(e.PlayerID) }

// ConsortPromoted records that the Consort has been elevated to full
// RoleMafia because the original mafia cabal was wiped out while she
// still lived (see promoteConsortIfNeeded). Private to the promoted
// player: the town must NOT learn that a sleeper has taken over the
// kill — to everyone else the game simply continues. A fresh
// MafiaRosterRevealed (now listing just her) is emitted alongside so
// her client recognizes its new faction.
type ConsortPromoted struct {
	PlayerID PlayerID
}

func (e ConsortPromoted) isEvent()               {}
func (e ConsortPromoted) Visibility() Visibility { return PrivateTo(e.PlayerID) }

// DetectiveResult delivers an investigation outcome privately to the
// detective.
type DetectiveResult struct {
	Detective PlayerID
	Target    PlayerID
	IsMafia   bool
}

func (e DetectiveResult) isEvent()               {}
func (e DetectiveResult) Visibility() Visibility { return PrivateTo(e.Detective) }

// Vote events are PRIVATE TO THE VOTER while the tally is hidden.
//
// During PhaseDayVote the tally is secret until the host calls
// RevealVotes: each voter sees only their own cast/change/retract (so
// the UI can render "your vote" and let them change it), and no one can
// see anyone else's choice or the running counts. The public reveal
// rides on a single VotesRevealed event that carries the whole map at
// once, so we deliberately do NOT widen these per-voter events to
// public on reveal — that keeps the "who can see what" rule a function
// of the event alone rather than of mutable game state.

// VoteCast is emitted when a voter casts their first vote of the current
// PhaseDayVote. Subsequent changes by the same voter emit VoteChanged;
// retractions emit VoteRetracted.
type VoteCast struct {
	Voter  PlayerID
	Target PlayerID
}

func (VoteCast) isEvent()                 {}
func (e VoteCast) Visibility() Visibility { return PrivateTo(e.Voter) }

// VoteChanged is emitted when a voter who already had an active vote
// switches to a different target. It carries both old and new targets so
// the UI and replays can describe the move without state reconstruction.
type VoteChanged struct {
	Voter PlayerID
	From  PlayerID
	To    PlayerID
}

func (VoteChanged) isEvent()                 {}
func (e VoteChanged) Visibility() Visibility { return PrivateTo(e.Voter) }

// VoteRetracted is emitted when a voter withdraws their active vote
// without immediately picking another target (i.e. DayVote{Target: ""}).
type VoteRetracted struct {
	Voter PlayerID
	Was   PlayerID
}

func (VoteRetracted) isEvent()                 {}
func (e VoteRetracted) Visibility() Visibility { return PrivateTo(e.Voter) }

// VotesRevealed is emitted when the host calls RevealVotes. It carries
// the full voter→target tally so every viewer — alive, dead, or
// spectating — can render who voted for whom in one shot. It is the only
// vote-related event that is Public; the per-voter cast/change/retract
// events stay private to their voter (see the note above VoteCast).
type VotesRevealed struct {
	Day   int
	Tally map[PlayerID]PlayerID
}

func (VotesRevealed) isEvent()               {}
func (VotesRevealed) Visibility() Visibility { return Public() }

// VoteCleared is emitted when the host calls ClearVotes during
// PhaseDayVote. The in-flight tally is wiped (and any prior reveal is
// undone) and clients re-render with a fresh empty board so the room can
// vote again, hidden once more.
type VoteCleared struct {
	Day int
}

func (VoteCleared) isEvent()               {}
func (VoteCleared) Visibility() Visibility { return Public() }

// PlayerLynched records the result of a day vote that reached a strict
// majority. The role of the lynched player is NOT revealed; that
// information is withheld until GameEnded.
type PlayerLynched struct {
	PlayerID PlayerID
}

func (PlayerLynched) isEvent()               {}
func (PlayerLynched) Visibility() Visibility { return Public() }

// NoLynch records that a finalized day vote ended without a lynch: no
// single target reached a strict majority of the living population (a
// split vote, a plurality short of half, abstentions, or no votes at
// all). The day still resolves — the host proceeds to the next night —
// but nobody dies. Public so every viewer can narrate the outcome.
type NoLynch struct {
	Day int
}

func (NoLynch) isEvent()               {}
func (NoLynch) Visibility() Visibility { return Public() }

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
