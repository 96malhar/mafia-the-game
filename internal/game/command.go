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

// CreateGame initializes a new game with the given roster of roles and a
// deterministic random seed. The seed makes role assignment reproducible
// in tests and replays.
type CreateGame struct {
	GameID GameID
	// Roles is the multiset of roles to be dealt out, one per player.
	// len(Roles) must equal the number of players added before StartGame.
	Roles []Role
	Seed  int64
}

func (CreateGame) isCommand() {}

// AddPlayer joins a player to the lobby. Only valid in PhaseLobby.
type AddPlayer struct {
	PlayerID PlayerID
	Name     string
}

func (AddPlayer) isCommand() {}

// StartGame transitions Lobby -> Night, shuffles Roles, and assigns one
// role to each player using the seed from CreateGame.
type StartGame struct{}

func (StartGame) isCommand() {}

// NightAction is a generic per-role action submitted during PhaseNight:
//
//   - Mafia      -> Target is the player to kill.
//   - Doctor     -> Target is the player to save.
//   - Detective  -> Target is the player to investigate.
//
// Villagers have no night action. The engine resolves all submitted
// actions when AdvancePhase moves Night -> Day.
type NightAction struct {
	Actor  PlayerID
	Target PlayerID
}

func (NightAction) isCommand() {}

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
// ErrWrongPhase.
//
// The plurality target at phase end is lynched. If no unique plurality
// exists, the day is extended once for a re-vote; a second indecisive
// tally ends the day with no lynch.
type DayVote struct {
	Voter  PlayerID
	Target PlayerID
}

func (DayVote) isCommand() {}

// AdvancePhase forces the current phase to end and the next phase to
// begin, resolving any pending actions/votes. The engine is timeless;
// the room layer (with its own clock) decides when to emit this command.
type AdvancePhase struct{}

func (AdvancePhase) isCommand() {}
