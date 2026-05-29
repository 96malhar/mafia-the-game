package game

// Player is the engine's per-player record. Fields are unexported so that
// only this package can mutate them; read access from outside is provided
// via accessors on GameState (added as needed).
type Player struct {
	id    PlayerID
	name  string
	role  Role // zero value (empty Role) until StartGame deals roles
	alive bool
}

// ID returns the player's identifier.
func (p Player) ID() PlayerID { return p.id }

// Name returns the player's display name.
func (p Player) Name() string { return p.name }

// Role returns the player's dealt role. It is the zero value Role("") in
// PhaseLobby (before StartGame).
func (p Player) Role() Role { return p.role }

// Alive reports whether the player is currently alive.
func (p Player) Alive() bool { return p.alive }

// GameState is the full, authoritative state of one game. It is mutated
// only through Game.Apply; outside callers may only read via accessors.
//
// We keep fields unexported so the compiler enforces this rule across the
// codebase — no one can write `state.phase = PhaseEnded` from another
// package even by accident.
//
// Roster model: instead of a fixed Roles slice configured at CreateGame,
// the engine carries (minPlayers, maxPlayers, mafiaCount). The actual
// per-player role list is composed at StartGame from
// (playerCount, mafiaCount): Mafia×N, Detective, Doctor, Villager×rest.
// This lets the host tune the game during the lobby without having to
// know up-front exactly how many friends will show up.
type GameState struct {
	id   GameID
	seed int64

	minPlayers int // minimum players required to call StartGame
	maxPlayers int // hard cap on AddPlayer
	mafiaCount int // number of Mafia roles to deal at StartGame

	phase   Phase
	day     int // day number; 0 in Lobby/first Night, 1 after first day starts
	players []Player

	// pendingNight stores night-action targets keyed by actor. Cleared
	// each time Night ends. Per-actor commit-once: re-submission is
	// rejected with ErrAlreadyActed.
	pendingNight map[PlayerID]PlayerID

	// votes stores the current PhaseDayVote tally as voter -> target.
	// Unlike night actions, votes are mutable; entries are overwritten
	// or deleted as players change or retract their vote.
	votes map[PlayerID]PlayerID

	// dayLynchResolved records whether this day has already had a vote
	// finalized. When true, the only legal host transition out of
	// PhaseDayDiscussion is BeginNight; OpenVoting is rejected. Reset
	// to false each time a fresh DayDiscussion begins (out of Night).
	dayLynchResolved bool

	// rolesDealt records whether StartGame has composed and assigned
	// the per-player roles. It gates the lobby-mutating commands
	// (AddPlayer, SetMafiaCount, and the lobby branch of BeginNight):
	// once roles exist, the lobby is closed even though the game stays
	// in PhaseLobby until BeginNight. This is an explicit flag rather
	// than the old "players[0].role != ''" probe, which coupled
	// correctness to player ordering and the assumption that dealing
	// always sets every role at once.
	rolesDealt bool

	// --- Night turn state ----------------------------------------------
	//
	// Nights are strictly turn-ordered: one role acts at a time, in
	// nightTurnQueue order, and each role's turn walks a small linear
	// sub-state machine (see NightSubPhase). The currently-active role
	// is at index 0 (currentNightRole); currentNightSubPhase says
	// where in that role's lifecycle we are. All are cleared (zero
	// values) any time the game is not in PhaseNight, or when the
	// queue is exhausted just before resolveNight runs.
	//
	// nightSubmitted records whether the active turn's actor submitted
	// an action (true) or the turn is going to/already timed out
	// (false). The room reads this when sizing the post-act ponder
	// duration: submitted → short fixed pause, timed-out → skip
	// straight to sleep, phantom → randomized "ponder" instead of an
	// act sub-phase at all.
	//
	// We keep these as ENGINE-OWNED state because the engine is the
	// authority on "whose turn is it AND what part of it". The engine
	// itself is timeless: wall-clock deadlines are NOT stored here.
	// The room layer owns timing entirely — it stamps an absolute
	// Deadline onto each Night*Started event before broadcasting and
	// arms its own timer against it (see internal/room/broadcast.go and
	// timers.go).
	currentNightRole     Role
	currentNightSubPhase NightSubPhase
	nightTurnQueue       []Role
	nightSubmitted       bool
}

// NightSubPhase is the sub-state during PhaseNight. Every night opens
// with a one-shot NightSubOpening ("City, go to sleep." + pre-wake
// silence), then each role's turn walks a five-step linear state
// machine:
//
//	opening ─▶ first role's narrate
//	narrate ─▶ act ─[submit]─▶ ponder(2s) ─▶ sleep ─▶ settle(2s) ─▶ next role
//	narrate ─▶ act ─[timer]──▶                sleep ─▶ settle(2s) ─▶ next role
//	narrate ──────────────────▶ ponder(5–10s)─▶ sleep ─▶ settle(2s) ─▶ next role   (phantom)
//
// Each transition is driven by a single AdvancePhase command from the
// room layer (whose timer fires at the end of the sub-phase) OR, for
// the act→ponder edge, by the actor submitting a NightAction. The
// engine itself is timeless; the room translates wall-clock into
// AdvancePhase. Phantom turns substitute `ponder` for `act` so the
// audio cues (narrate + sleep) still play, but no action can be
// submitted.
//
// Note that NightSubOpening is a NIGHT-scoped sub-phase (no
// currentNightRole during it); all others are role-scoped.
//
// The zero value NightSubPhase("") means "not in any night sub-phase";
// it's the value returned outside PhaseNight or between role turns.
type NightSubPhase string

const (
	// NightSubOpening is the one-shot start-of-night beat that happens
	// AFTER PhaseChanged→night and BEFORE the first role's narrate.
	// It's the "City, go to sleep." cue plus a fixed pre-wake silence
	// so the room has time to settle before any role is named. No
	// action accepted, currentNightRole is empty.
	NightSubOpening NightSubPhase = "opening"

	// NightSubNarrate is the opening audio cue ("Mafia, wake up...").
	// No action accepted. Duration is sized to cover the spoken cue.
	NightSubNarrate NightSubPhase = "narrate"

	// NightSubAct is the actor's decision window. Real turns only;
	// phantom turns substitute NightSubPonder. NightAction submissions
	// are accepted only during this sub-phase (engine returns
	// ErrNotYourTurn otherwise).
	NightSubAct NightSubPhase = "act"

	// NightSubPonder is a short pause between the actor finishing and
	// the "go to sleep" outro. For real turns it gives the room a
	// breath to absorb that an action was just submitted; for phantom
	// turns it stands in for the missing act window so the cadence
	// can't be used to deduce that a role is dead. No action accepted.
	NightSubPonder NightSubPhase = "ponder"

	// NightSubSleep is the closing audio cue ("Mafia, go to sleep.").
	// No action accepted. Sized to cover the spoken cue.
	NightSubSleep NightSubPhase = "sleep"

	// NightSubSettle is a short fixed pause after sleep before the
	// next role's narrate (or, for the last role, before the
	// night→day transition). Lets the "go to sleep" cue land cleanly
	// before the next narrator line begins. No action accepted.
	NightSubSettle NightSubPhase = "settle"
)

// newState constructs an empty state in PhaseLobby. Unexported because
// callers should always go through Game.Apply(CreateGame{...}).
func newState() *GameState {
	return &GameState{
		phase: PhaseLobby,
	}
}

// --- read-only accessors ---------------------------------------------------

// ID returns the game identifier.
func (s *GameState) ID() GameID { return s.id }

// Phase returns the current phase.
func (s *GameState) Phase() Phase { return s.phase }

// Day returns the current day number (0 before the first day starts).
func (s *GameState) Day() int { return s.day }

// Players returns a copy of the player list in join order. The copy keeps
// callers from accidentally mutating the engine's slice.
func (s *GameState) Players() []Player {
	out := make([]Player, len(s.players))
	copy(out, s.players)
	return out
}

// PlayerCount returns the number of players in the game (alive or dead).
func (s *GameState) PlayerCount() int { return len(s.players) }

// MinPlayers returns the minimum player count required to start.
func (s *GameState) MinPlayers() int { return s.minPlayers }

// MaxPlayers returns the hard cap on AddPlayer.
func (s *GameState) MaxPlayers() int { return s.maxPlayers }

// MafiaCount returns the current configured number of mafia for the
// upcoming game. May still be adjusted (via SetMafiaCount) while the
// game is in PhaseLobby.
func (s *GameState) MafiaCount() int { return s.mafiaCount }

// DayLynchResolved reports whether the current day has already had a
// vote finalized (i.e. a lynch has been resolved or the day was
// otherwise concluded). Used by the UI to decide which host buttons
// to surface — pre-finalize the host gets Open/Clear/Finalize voting,
// post-finalize they only get Begin Night.
func (s *GameState) DayLynchResolved() bool { return s.dayLynchResolved }

// RolesDealt reports whether StartGame has dealt per-player roles. Once
// true the lobby is closed to new players and config changes, even
// while the game remains in PhaseLobby awaiting the host's BeginNight.
func (s *GameState) RolesDealt() bool { return s.rolesDealt }

// CurrentNightRole returns the role whose turn it currently is during
// PhaseNight, or the empty Role between turns / outside of Night.
func (s *GameState) CurrentNightRole() Role { return s.currentNightRole }

// CurrentNightSubPhase returns the active sub-phase within the current
// role's night turn (narrate / act / ponder / sleep / settle), or the
// empty NightSubPhase outside of an active turn. See NightSubPhase
// for the per-role state machine.
func (s *GameState) CurrentNightSubPhase() NightSubPhase { return s.currentNightSubPhase }

// NightTurnSubmitted reports whether an actor has submitted a
// NightAction during the current role's act window. Used by the room
// when sizing the post-act ponder duration: true → short fixed beat
// to absorb the submission; false → ponder is skipped (timeout) or
// uses the phantom-substitute random window. Always returns false
// outside of PhaseNight.
func (s *GameState) NightTurnSubmitted() bool { return s.nightSubmitted }

// HasLivingRole reports whether at least one living player holds the
// given role. Exported so the room layer can size phantom-vs-real
// ponder durations without re-walking the player list. Mirrors the
// engine's internal hasLivingRole helper.
func (s *GameState) HasLivingRole(r Role) bool {
	for i := range s.players {
		if s.players[i].alive && s.players[i].role == r {
			return true
		}
	}
	return false
}

// findPlayer returns a pointer to the player record and true, or nil and
// false if no such player exists. Unexported: rule helpers use it; the
// outside world only sees the value type via Players().
func (s *GameState) findPlayer(id PlayerID) (*Player, bool) {
	for i := range s.players {
		if s.players[i].id == id {
			return &s.players[i], true
		}
	}
	return nil, false
}

// factionLivingCount returns the number of currently alive members of f.
func (s *GameState) factionLivingCount(f Faction) int {
	n := 0
	for i := range s.players {
		if s.players[i].alive && s.players[i].role.Faction() == f {
			n++
		}
	}
	return n
}

// finalRolesSnapshot copies player -> role for the GameEnded event. Only
// safe to call at game end since it exposes every role publicly.
func (s *GameState) finalRolesSnapshot() map[PlayerID]Role {
	out := make(map[PlayerID]Role, len(s.players))
	for _, p := range s.players {
		out[p.id] = p.role
	}
	return out
}
