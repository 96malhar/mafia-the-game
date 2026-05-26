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

	// --- Night turn state ----------------------------------------------
	//
	// Nights are strictly turn-ordered: one role acts at a time, in
	// nightTurnQueue order. The currently-active role is at index 0
	// (currentNightRole), and the deadline below is when its turn
	// auto-passes if no action arrives. Cleared (zero values) any time
	// the game is not in PhaseNight, or when the queue is exhausted
	// just before resolveNight runs.
	//
	// We keep these as ENGINE-OWNED state because the engine is the
	// authority on "whose turn is it"; the room layer only schedules
	// wall-clock callbacks against the deadline. The deadline is stored
	// as unix-millis to keep the engine free of time.Time imports and
	// to keep the wire encoding trivial.
	currentNightRole        Role
	nightTurnDeadlineMillis int64
	nightTurnQueue          []Role
}

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

// CurrentNightRole returns the role whose turn it currently is during
// PhaseNight, or the empty Role between turns / outside of Night.
func (s *GameState) CurrentNightRole() Role { return s.currentNightRole }

// NightTurnDeadlineMillis returns the unix-millis deadline at which the
// current night-turn auto-passes, or 0 if no turn is active.
func (s *GameState) NightTurnDeadlineMillis() int64 { return s.nightTurnDeadlineMillis }

// CurrentNightTurnIsPhantom reports whether the active night turn has
// no living player holding its role. Such turns are still emitted (so
// the moderator audio doesn't leak which role is dead) but accept no
// NightAction. Returns false when there is no active turn or when at
// least one player of the current role is alive.
func (s *GameState) CurrentNightTurnIsPhantom() bool {
	if s.currentNightRole == "" {
		return false
	}
	for i := range s.players {
		if s.players[i].alive && s.players[i].role == s.currentNightRole {
			return false
		}
	}
	return true
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
