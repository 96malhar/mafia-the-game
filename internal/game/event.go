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
//   - Graveyard()            : only dead players (the spectating graveyard).
//
// We model this as a single value (rather than three separate interface
// types) because it's small, comparable, and easy to JSON-encode.
type Visibility struct {
	// Audience is one of "public", "player", "faction", "dead".
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

// Graveyard restricts visibility to dead players — the spectating
// graveyard that, once eliminated, watches the rest of the game with
// full knowledge. Used by RosterRevealed to hand the dead the complete
// player→role map without ever leaking it to the living.
func Graveyard() Visibility { return Visibility{Audience: "dead"} }

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

// VigilanteChanged records a host-driven toggle of the optional
// Vigilante role during PhaseLobby. Public so every observer sees the
// configured composition update in real time (it does NOT reveal who, if
// anyone, will be dealt the role — that stays secret until GameEnded).
type VigilanteChanged struct {
	Enabled bool
}

func (VigilanteChanged) isEvent()               {}
func (VigilanteChanged) Visibility() Visibility { return Public() }

// YakuzaChanged records a host-driven toggle of the optional Yakuza role
// during PhaseLobby. Public so every observer sees the configured
// composition update in real time (it does NOT reveal who, if anyone, will
// be dealt the role — that stays secret until GameEnded).
type YakuzaChanged struct {
	Enabled bool
}

func (YakuzaChanged) isEvent()               {}
func (YakuzaChanged) Visibility() Visibility { return Public() }

// TrackerChanged records a host-driven toggle of the optional Tracker role
// during PhaseLobby. Public so every observer sees the configured
// composition update in real time (it does NOT reveal who, if anyone, will
// be dealt the role — that stays secret until GameEnded).
type TrackerChanged struct {
	Enabled bool
}

func (TrackerChanged) isEvent()               {}
func (TrackerChanged) Visibility() Visibility { return Public() }

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
//
// Yakuza, when non-empty, is the PlayerID of the faction's Yakuza, so the
// faction can tell WHICH member is the Yakuza (the rest are interchangeable
// RoleMafia). It is set whenever a Yakuza was dealt — on the StartGame reveal
// and on every re-issue (a consort promotion, a recruit), both of which build
// the roster via currentMafiaRoster / yakuzaPlayerID, which returns the
// Yakuza's id even after it has died. So the Yakuza stays distinctly badged
// for the whole game, and the value is consistent across the live and
// rejoin-reconstructed views.
type MafiaRosterRevealed struct {
	Members []PlayerID
	Yakuza  PlayerID
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

	// Duration is the sub-phase's full length in millis (Deadline minus its
	// start instant). Like Deadline, the engine emits 0 and the room stamps
	// it. A client that joins or refreshes mid-sub-phase knows only the
	// absolute Deadline, so it can't tell how much of the window already
	// elapsed; Duration lets it render the countdown bar at the correct
	// proportion (remaining / Duration) instead of restarting it full.
	Duration int64

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

// WithTiming returns a copy of the event with its Deadline (unix-millis) and
// Duration (millis) stamped, but only if Deadline was still 0 (unstamped).
// Value receiver + return-copy keeps events immutable. The room layer calls
// this to stamp wall-clock timing onto the engine's timeless event before
// broadcasting (see stampNightDeadlines in internal/room).
func (e NightSubPhaseStarted) WithTiming(deadlineMs, durationMs int64) Event {
	if e.Deadline == 0 {
		e.Deadline = deadlineMs
		e.Duration = durationMs
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

// SpectatorNightAction reveals a submitted night action to the GRAVEYARD
// (dead players spectating). Unlike NightActionRecorded — which is scoped
// to the actor's own faction so the LIVING can't see across roles — this
// is Graveyard-only: it never reaches a living player, so it leaks nothing
// to the table, while letting the dead (who already hold the full roster
// via RosterRevealed) watch the night unfold. It carries both
// participants' roles so the client can render "Actor (role) targeted
// Target (role)" without depending on roster-delivery timing. Emitted
// alongside NightActionRecorded for every submitted action — one per role
// that acts — so the feed builds up across the night in turn order.
type SpectatorNightAction struct {
	Actor      PlayerID
	ActorRole  Role
	Target     PlayerID
	TargetRole Role
	// Recruit marks this as a Yakuza recruit rather than a kill/target
	// action, so the graveyard feed can render "recruited" instead of the
	// role's usual verb. Default false preserves the existing semantics for
	// every other action.
	Recruit bool
}

func (SpectatorNightAction) isEvent()               {}
func (SpectatorNightAction) Visibility() Visibility { return Graveyard() }

// RecruitRecorded acknowledges that the Yakuza locked a recruit during the
// Mafia turn. Faction-scoped (like the mafia kill ack) so co-mafia see that
// the night's faction action is a recruit — and thus no kill is coming —
// while it stays hidden from the town. The recruit TARGET does not see this
// (they are still town at submission time and only learn at resolution, via
// RoleAssigned / Recruited).
type RecruitRecorded struct {
	Yakuza PlayerID
	Target PlayerID
}

func (RecruitRecorded) isEvent()               {}
func (RecruitRecorded) Visibility() Visibility { return FactionOnly(FactionMafia) }

// Recruited tells a player the Yakuza has recruited them into the mafia AND
// that their own night power is suppressed for this night. Private to the
// recruited player; the town must never learn a conversion happened. Emitted
// for a recruit target that holds a night role at the start of their (now
// phantom) turn — mirroring the Blocked notice's timing — and at night
// resolution for a target with no night turn (a villager), who has no earlier
// beat to be told at.
type Recruited struct {
	PlayerID PlayerID
}

func (e Recruited) isEvent()               {}
func (e Recruited) Visibility() Visibility { return PrivateTo(e.PlayerID) }

// PlayerKilled is emitted at Night -> Day if the mafia's target was not
// saved by the doctor. Always public.
type PlayerKilled struct {
	PlayerID PlayerID
}

func (PlayerKilled) isEvent()               {}
func (PlayerKilled) Visibility() Visibility { return Public() }

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
// MafiaRosterRevealed (listing the full cabal — her plus her now-dead
// predecessors) is emitted alongside so her client recognizes its new
// faction and can badge those predecessors.
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

// TrackerResult delivers a tracking outcome privately to the Tracker: who
// the tracked Target visited that night. Visited is the id of the player
// the target acted against — or, when the target is a mafia-faction member,
// the faction's collective target (see GameState.trackedVisit). Visited is
// "" when the target took no action ("stayed home"). It carries no hint of
// WHAT the action was: the Tracker learns the visit, never the verb.
// Private to the Tracker; no one else may see it.
type TrackerResult struct {
	Tracker PlayerID
	Target  PlayerID
	Visited PlayerID
}

func (e TrackerResult) isEvent()               {}
func (e TrackerResult) Visibility() Visibility { return PrivateTo(e.Tracker) }

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

// VoteAbstained is emitted when a voter records an abstention (DayAbstain).
// Like the other per-voter vote events it is PRIVATE TO THE VOTER while the
// tally is hidden — only the abstainer's own UI reflects it; the room learns
// only that the running count went up (via VoteProgress). On reveal, an
// abstainer is simply a living player absent from the public tally (reveal
// is gated on everyone having decided, so "didn't vote for anyone" then
// unambiguously means "abstained").
type VoteAbstained struct {
	Voter PlayerID
}

func (VoteAbstained) isEvent()                 {}
func (e VoteAbstained) Visibility() Visibility { return PrivateTo(e.Voter) }

// VoteProgress is a PUBLIC running count of how many living players have a
// vote recorded in the current PhaseDayVote. It rides alongside each
// private VoteCast/VoteChanged/VoteRetracted so the WHOLE room — voters,
// non-voters, and the dead — can watch voting progress ("N of M voted",
// or all-in) WITHOUT learning who voted for whom. It deliberately carries
// only the aggregate Cast count, never any voter→target pair: this is the
// one running number that crosses the secrecy boundary the individual
// votes do not (see the note above VoteCast). M (the living count) is not
// carried — every client already knows the roster and computes it locally,
// the same way the lynch-threshold preview does.
type VoteProgress struct {
	Day  int
	Cast int
}

func (VoteProgress) isEvent()               {}
func (VoteProgress) Visibility() Visibility { return Public() }

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

// RosterRevealed hands the graveyard (dead players) the full
// player→role map so the eliminated can spectate the rest of the game
// with complete knowledge — "the dead see everything". It is scoped to
// the Graveyard audience, so the living never receive it and learn
// nothing.
//
// Emitted whenever the board changes in a way the dead should see: a
// kill, a lynch, or a consort promotion. Re-issued AFTER
// promoteConsortIfNeeded so a sleeper's takeover (consort → mafia) is
// reflected even for players who died before the promotion — they get a
// refreshed snapshot rather than a stale role. Because every role is
// dealt at StartGame, a single snapshot already names every player
// (including those still alive who may die later), so no per-future-death
// follow-up is needed beyond the promotion refresh.
//
// The map is a fresh copy (finalRolesSnapshot) so it stays immutable for
// replay.
type RosterRevealed struct {
	Roles map[PlayerID]Role
}

func (RosterRevealed) isEvent()               {}
func (RosterRevealed) Visibility() Visibility { return Graveyard() }

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

// ResetPlayer is the per-player snapshot carried by GameReset: just the
// stable identity (id + name), since a fresh lobby has no roles, no
// aliveness distinctions, and no other per-player state to preserve.
type ResetPlayer struct {
	ID   PlayerID
	Name string
}

// GameReset records a finished game returning to a fresh lobby in the same
// room (see the ResetGame command). It is a self-contained lobby snapshot:
// the room replaces its entire event log with this single event as the new
// baseline — the previous game's events are deliberately NOT carried over —
// so a player joining or rejoining after the reset reconstructs the whole
// lobby from GameReset alone. That is why it duplicates the GameCreated
// config (MinPlayers/MaxPlayers/MafiaCount) and the retained roster rather
// than relying on earlier events still being present.
//
// Public: a reset to lobby is not secret, and every connected client (living
// or dead in the prior game) must see it to drop back to the lobby view.
// Optional-role toggles are intentionally omitted — a reset clears them all
// to off, which is the client's lobby default.
type GameReset struct {
	Players    []ResetPlayer
	MinPlayers int
	MaxPlayers int
	MafiaCount int
}

func (GameReset) isEvent()               {}
func (GameReset) Visibility() Visibility { return Public() }
