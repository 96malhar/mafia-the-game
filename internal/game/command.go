package game

// Command is a player or system intent submitted to the game engine. The
// engine's Apply function returns a slice of Events (facts) or an error
// (the command was rejected).
//
// Command is a CLOSED interface: the unexported isCommand() method means
// only types within this package can satisfy it. Callers must use one of
// the concrete command types defined below; this prevents downstream code
// from sneaking new command shapes into the engine.
type Command interface {
	isCommand()
}

// CreateGame initializes a new game with a variable-roster configuration
// and a deterministic random seed. The actual roster (which roles get
// dealt to whom) is composed at StartGame from the current player count
// and mafiaCount; see applyStartGame for the formula.
//
// MinPlayers and MaxPlayers bound the lobby:
//   - AddPlayer is rejected once playerCount == MaxPlayers.
//   - StartGame is rejected unless MinPlayers ≤ playerCount ≤ MaxPlayers.
//
// MafiaCount is the initial configured mafia count. It can be tuned in
// lobby via SetMafiaCount. Constraints are validated at StartGame against
// the final player count (1 ≤ mafia ≤ playerCount - 3, reserving 1
// Detective + 1 Doctor + ≥1 Villager).
//
// Zero values are normalized in applyCreateGame: MinPlayers defaults to
// 5, MaxPlayers to 20, MafiaCount to a sensible default clamped to range.
type CreateGame struct {
	GameID     GameID
	MinPlayers int
	MaxPlayers int
	MafiaCount int
	Seed       int64
}

func (CreateGame) isCommand() {}

// SetMafiaCount adjusts the number of Mafia roles that will be dealt at
// StartGame. Valid only in PhaseLobby. The value must be ≥ 1 and ≤
// MaxPlayers - 3 (so that even a fully-loaded lobby leaves room for the
// fixed Det + Doc + ≥1 Villager).
//
// We deliberately do NOT validate against the current player count here,
// because the host typically tunes the count BEFORE the lobby is full.
// Final validity is rechecked at StartGame.
type SetMafiaCount struct {
	Count int
}

func (SetMafiaCount) isCommand() {}

// SetConsort toggles the optional Consort role for the upcoming game.
// Valid only in PhaseLobby (before roles are dealt). Setting it to its
// current value is a no-op (ErrNoChange). When enabled, StartGame deals
// exactly one RoleConsort, taking the slot of a villager — so the same
// 1 ≤ mafia ≤ playerCount-3 envelope still applies. Host-only at the
// transport layer.
type SetConsort struct {
	Enabled bool
}

func (SetConsort) isCommand() {}

// SetVigilante toggles the optional Vigilante role for the upcoming
// game. Valid only in PhaseLobby (before roles are dealt). Setting it to
// its current value is a no-op (ErrNoChange). When enabled, StartGame
// deals exactly one RoleVigilante, taking the slot of a villager — so
// the same 1 ≤ mafia ≤ playerCount-3 envelope still applies, but
// StartGame additionally rejects a composition where the mafia plus the
// enabled optional roles would leave fewer than one plain villager.
// Host-only at the transport layer.
type SetVigilante struct {
	Enabled bool
}

func (SetVigilante) isCommand() {}

// SetYakuza toggles the optional Yakuza role for the upcoming game. Valid
// only in PhaseLobby (before roles are dealt). Setting it to its current
// value is a no-op (ErrNoChange). When enabled, StartGame deals exactly one
// RoleYakuza, taking the slot of a villager — so the same 1 ≤ mafia ≤
// playerCount-3 envelope still applies, and because the Yakuza is mafia-
// aligned it counts toward the StartGame parity guard like a mafioso.
// Host-only at the transport layer.
type SetYakuza struct {
	Enabled bool
}

func (SetYakuza) isCommand() {}

// AddPlayer joins a player to the lobby. Only valid in PhaseLobby.
type AddPlayer struct {
	PlayerID PlayerID
	Name     string
}

func (AddPlayer) isCommand() {}

// StartGame deals roles and locks the roster. Valid only in PhaseLobby.
// It does NOT transition to Night — the game remains in PhaseLobby with
// every player's role assigned. The host then issues BeginNight to
// actually start play. This split exists so the host can verbally walk
// the room through "Everyone close your eyes" / role reveals before
// the engine starts running night turns.
type StartGame struct{}

func (StartGame) isCommand() {}

// BeginNight transitions PhaseLobby (after StartGame) or
// PhaseDayDiscussion (after a finalized vote) into PhaseNight. The
// engine immediately begins the night's turn sequence (Mafia → Det →
// Doctor, with phantom turns where roles are dead). Host-only at the
// transport layer.
type BeginNight struct{}

func (BeginNight) isCommand() {}

// OpenVoting transitions PhaseDayDiscussion into PhaseDayVote, clearing
// any stale vote tally. Valid only in PhaseDayDiscussion AND only
// while the day has not yet had a vote finalized (i.e.
// dayLynchResolved == false). Host-only.
type OpenVoting struct{}

func (OpenVoting) isCommand() {}

// ClearVotes wipes the current PhaseDayVote tally so the room can
// re-vote from scratch. Stays in PhaseDayVote. Host-only.
type ClearVotes struct{}

func (ClearVotes) isCommand() {}

// RevealVotes flips the current PhaseDayVote tally from hidden to
// public. Until the host reveals, vote counts and targets are private
// to each voter (a voter sees only their own choice); revealing emits a
// VotesRevealed event carrying the full voter→target map to everyone,
// including dead players. Once revealed, voting is locked (DayVote is
// rejected) until the host either FinalizeVotes or ClearVotes (the
// latter wipes the tally and reopens voting, hidden again). Valid only
// in PhaseDayVote and only once per tally (re-reveal is ErrNoChange).
// Host-only.
type RevealVotes struct{}

func (RevealVotes) isCommand() {}

// FinalizeVotes resolves the current vote tally and ends PhaseDayVote.
// Valid only when the tally has a unique plurality (decisive lynch);
// otherwise rejected with ErrNoChange — the host must ClearVotes first
// or, if everyone agrees not to lynch, just keep things deadlocked
// while still in DayVote (a future "skip lynch" command could be
// added). On success, transitions to PhaseDayDiscussion (post-lynch)
// or PhaseEnded if the lynch ends the game. Host-only.
//
// The intended flow is RevealVotes → FinalizeVotes, surfaced by the
// host UI; the engine does not hard-require a prior reveal so existing
// host tooling and tests stay simple.
type FinalizeVotes struct{}

func (FinalizeVotes) isCommand() {}

// NightAction is a generic per-role action submitted during PhaseNight:
//
//   - Mafia      -> Target is the player to kill.
//   - Doctor     -> Target is the player to save.
//   - Detective  -> Target is the player to investigate.
//   - Consort    -> Target is the player to block.
//   - Vigilante  -> Target is the player to shoot (one-shot for the game).
//
// Villagers have no night action. The engine resolves all submitted
// actions when AdvancePhase moves Night -> Day.
type NightAction struct {
	Actor  PlayerID
	Target PlayerID
}

func (NightAction) isCommand() {}

// NightPass is an explicit "I choose NOT to act this turn" submitted
// during PhaseNight's act window. It ends the actor's act window early
// (advancing straight to ponder) WITHOUT recording an action — and,
// crucially, without spending any one-shot resource.
//
// Only roles whose nightActionSpec opts in (AllowPass) accept it; today
// that's the Vigilante, for whom "hold fire" preserves his single bullet
// for a later night and spares the table a full 60s wait. Every other
// role rejects NightPass with ErrNotYourAction. Mafia in particular are
// excluded: their turn is faction-collective, so one mafioso passing must
// not end the kill window for the whole faction.
//
// Like NightAction, Actor is filled in server-side from the authenticated
// connection; clients never set it.
type NightPass struct {
	Actor PlayerID
}

func (NightPass) isCommand() {}

// Recruit is the Yakuza's one-shot conversion, submitted during the Mafia
// turn's act window as an alternative to the faction kill. Target is the
// player to recruit into the mafia; Actor is filled in server-side from the
// authenticated connection (clients never set it), like NightAction.
//
// On success the act window closes (so the faction cannot also kill this
// night). At night resolution: Target is converted to full RoleMafia, the
// Yakuza dies (a self-sacrifice the doctor cannot prevent), and the Target's
// own night power is suppressed for the night. Only a living RoleYakuza may
// issue it, only during the Mafia turn's act sub-phase, and only against a
// living non-RoleMafia player (the Consort included). Any other actor,
// phase, or target is rejected.
type Recruit struct {
	Actor  PlayerID
	Target PlayerID
}

func (Recruit) isCommand() {}

// DayVote is a public vote during PhaseDayVote. Votes are MUTABLE — a
// player may change or retract their vote any number of times until the
// vote phase ends, and the most recent value is what counts toward the
// final tally.
//
//   - Target == "" with an active prior vote   -> retract (VoteRetracted)
//   - Target == "" with no prior vote          -> ErrNoChange
//   - Target == priorTarget                    -> ErrNoChange (no-op spam)
//   - Target != priorTarget, no prior          -> VoteCast
//   - Target != priorTarget, had prior         -> VoteChanged{From, To}
//
// Self-voting is forbidden (ErrSelfTarget). Voter and Target (when set)
// must be alive. Votes submitted in PhaseDayDiscussion are rejected with
// ErrWrongPhase, as are votes submitted after the host has revealed the
// tally (RevealVotes locks voting until a ClearVotes reopens it).
//
// The vote phase is host-driven: the host calls FinalizeVotes when the
// room has reached verbal consensus. The plurality target at finalize
// time is lynched; if no unique plurality exists, FinalizeVotes is
// rejected and the host can ClearVotes to start a fresh re-vote.
type DayVote struct {
	Voter  PlayerID
	Target PlayerID
}

func (DayVote) isCommand() {}

// AdvancePhase forces the current PhaseNight's active role-turn to end
// without an action (timeout semantics). It is INTERNAL: only the room
// layer's per-turn timer should emit it, and the transport layer
// refuses to forward it from clients. Other phases reject AdvancePhase
// with ErrWrongPhase — daytime pacing is driven entirely by the host
// commands (BeginNight, OpenVoting, ClearVotes, FinalizeVotes).
type AdvancePhase struct{}

func (AdvancePhase) isCommand() {}
