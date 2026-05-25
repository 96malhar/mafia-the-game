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
type GameState struct {
	id    GameID
	seed  int64
	roles []Role // configured roster: roles to be dealt at StartGame

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

	// dayVoteExtended records whether the current day has already used
	// its single re-vote extension. Reset each time a fresh day begins.
	dayVoteExtended bool
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
