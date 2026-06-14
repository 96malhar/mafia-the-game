package game

import "errors"

// Sentinel errors returned by Apply when a command is rejected. They are
// values (not types) so callers can use errors.Is for matching while we
// keep the option of wrapping with extra context (e.g. fmt.Errorf with %w)
// inside the engine.
//
// These names are deliberately *behavioural* (what went wrong) rather
// than referencing the specific command — "wrong phase" is reusable
// across many commands, whereas "ErrCannotJoinAfterStart" would not be.
var (
	// ErrWrongPhase is returned when a command is submitted in a phase
	// where it is not legal (e.g. DayVote during Night, AddPlayer after
	// the game has started).
	ErrWrongPhase = errors.New("game: command not allowed in current phase")

	// ErrUnknownPlayer is returned when a command references a player ID
	// that is not part of this game.
	ErrUnknownPlayer = errors.New("game: unknown player")

	// ErrDuplicatePlayer is returned when AddPlayer is called with an ID
	// that already exists in the lobby.
	ErrDuplicatePlayer = errors.New("game: player already in game")

	// ErrDuplicateName is returned when AddPlayer is called with a Name
	// that already belongs to another player in the lobby, compared
	// case-insensitively and with leading/trailing whitespace trimmed.
	// The client surface for this is the join handshake (see
	// room.joinErrorFor); from the user's perspective it reads as
	// "that name is taken, pick another."
	ErrDuplicateName = errors.New("game: name already taken")

	// ErrPlayerDead is returned when a command requires the actor or
	// target to be alive but they are not.
	ErrPlayerDead = errors.New("game: player is dead")

	// ErrNotYourAction is returned when a player submits a night action
	// their role does not permit (e.g. a villager calling NightAction).
	ErrNotYourAction = errors.New("game: action not permitted for this role")

	// ErrSelfTarget is returned when a player targets themselves in a
	// context that forbids it (e.g. detective investigating self).
	ErrSelfTarget = errors.New("game: cannot target self")

	// ErrRosterMismatch is returned by StartGame / SetMafiaCount when the
	// roster is structurally invalid: the player count is outside
	// [minPlayers, maxPlayers], the mafia count is < 1, or the mafia count
	// plus the reserved town core (Det + Doc) plus every enabled optional
	// role exceeds the available seats (the composition can't be built).
	// Zero plain villagers is allowed — only a NEGATIVE villager slot count
	// (roles over-subscribing the seats) is rejected here.
	ErrRosterMismatch = errors.New("game: roster (player count or mafia count) is not valid for starting the game")

	// ErrTownNotMajority is returned by StartGame when the town faction
	// would not hold a strict majority of the seats — i.e. the mafia-aligned
	// players (Mafia + Yakuza + Consort) make up half or more of the roster.
	// Such a board opens effectively decided for the mafia, so we refuse to
	// deal it; the host fixes it by lowering the mafia count or disabling a
	// mafia-aligned optional role.
	ErrTownNotMajority = errors.New("game: town faction must hold more than half the seats")

	// ErrLobbyFull is returned by AddPlayer when the lobby has already
	// reached MaxPlayers and cannot accept another joiner.
	ErrLobbyFull = errors.New("game: lobby is full")

	// ErrGameEnded is returned when any command (other than inspection)
	// is submitted after PhaseEnded.
	ErrGameEnded = errors.New("game: game has ended")

	// ErrNoChange is returned when a command would not alter state — for
	// example, re-submitting an identical vote or retracting a vote that
	// was never cast. We reject rather than silently no-op so that the
	// event log isn't spammed with non-events.
	ErrNoChange = errors.New("game: command would not change state")

	// ErrAlreadyActed is returned when a player who has already submitted
	// a NightAction in the current night tries to submit another. Night
	// actions are commit-once per night (unlike day votes which are
	// changeable until the timer expires).
	ErrAlreadyActed = errors.New("game: night action already submitted this night")

	// ErrNotYourTurn is returned when a player submits a NightAction
	// during PhaseNight but their role does not match the role whose
	// turn it currently is. Used by the strict turn-order rule that
	// makes Nights playable in person ("Mafia wake up… now Detective").
	ErrNotYourTurn = errors.New("game: it is not your role's turn")

	// ErrBlocked is returned when a roleblocked NON-mafia actor (a town
	// info role the Consort distracted this night) tries to submit a
	// night action. A correct client never reaches this — it's told it's
	// blocked at the start of the turn (the Blocked event) and hides the
	// target picker — so this rejects a client that bypasses the UI.
	// Mafia are immune to the block and never see this error.
	ErrBlocked = errors.New("game: your night action is blocked tonight")

	// ErrVotingIncomplete is returned by RevealVotes when not every living
	// player has yet cast a decision (a vote or an abstention). Reveal is
	// gated so the host can't open the box on a partial tally; the count
	// is surfaced publicly (VoteProgress) so the room can see who is still
	// outstanding.
	ErrVotingIncomplete = errors.New("game: not every living player has voted yet")
)
