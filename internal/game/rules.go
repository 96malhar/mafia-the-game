package game

import (
	"fmt"
	"math/rand/v2"
)

// Default lobby bounds applied when CreateGame leaves them zero. These
// match the application-level UX (host always plays in a 5-to-20 room)
// but live here so the engine's invariants are self-contained.
const (
	defaultMinPlayers = 5
	defaultMaxPlayers = 20

	// reservedTownRoles is the fixed non-mafia core that every roster
	// includes regardless of size: 1 Detective + 1 Doctor. The rest are
	// villagers. We name it so the +2 in validation reads intentionally.
	reservedTownRoles = 2
)

// defaultMafiaCount picks a reasonable starting mafia count for a given
// lobby bound. We tie the default to MinPlayers (not MaxPlayers) so that
// a game starting at the minimum is immediately playable without the
// host having to tune anything. For MinPlayers=5 this yields 1 mafia,
// for 12 it yields 4, etc. Always at least 1, and never so many that
// even at MinPlayers there'd be no room for Det + Doc + ≥1 villager.
func defaultMafiaCount(minPlayers int) int {
	m := minPlayers / 3
	if m < 1 {
		m = 1
	}
	if m > minPlayers-(reservedTownRoles+1) {
		m = minPlayers - (reservedTownRoles + 1)
	}
	return m
}

// applyCreateGame initializes a fresh game with a variable-roster lobby
// configuration. The actual per-player roles are not chosen until
// StartGame, so CreateGame stores only the bounds and a planned
// mafiaCount which the host can later tune via SetMafiaCount.
//
// Validation:
//   - The game must be in its pristine post-New() state.
//   - GameID must be non-empty.
//   - MinPlayers and MaxPlayers must form a valid bound with enough room
//     for at least one Mafia + Detective + Doctor + Villager (i.e.
//     MinPlayers ≥ 4) and MinPlayers ≤ MaxPlayers.
//   - MafiaCount, if non-zero, must satisfy 1 ≤ MafiaCount ≤
//     MaxPlayers - reservedTownRoles - 1. If zero, a sensible default is
//     chosen.
//
// Zero values for MinPlayers / MaxPlayers fall back to package defaults
// so callers that don't care can pass `CreateGame{GameID: id, Seed: s}`.
func (g *Game) applyCreateGame(c CreateGame) ([]Event, error) {
	if g.state.id != "" {
		return nil, ErrWrongPhase
	}
	if c.GameID == "" {
		return nil, fmt.Errorf("game: CreateGame.GameID must not be empty")
	}

	minP := c.MinPlayers
	if minP == 0 {
		minP = defaultMinPlayers
	}
	maxP := c.MaxPlayers
	if maxP == 0 {
		maxP = defaultMaxPlayers
	}

	if minP < reservedTownRoles+2 { // need ≥1 mafia + Det + Doc + ≥1 villager
		return nil, fmt.Errorf("game: MinPlayers must be ≥ %d, got %d", reservedTownRoles+2, minP)
	}
	if maxP < minP {
		return nil, fmt.Errorf("game: MaxPlayers (%d) must be ≥ MinPlayers (%d)", maxP, minP)
	}
	maxMafia := maxP - (reservedTownRoles + 1)

	mafia := c.MafiaCount
	if mafia == 0 {
		mafia = defaultMafiaCount(minP)
	}
	if mafia < 1 || mafia > maxMafia {
		return nil, fmt.Errorf("game: MafiaCount %d out of range [1, %d] for MaxPlayers=%d",
			mafia, maxMafia, maxP)
	}

	g.state.id = c.GameID
	g.state.seed = c.Seed
	g.state.minPlayers = minP
	g.state.maxPlayers = maxP
	g.state.mafiaCount = mafia

	return []Event{
		GameCreated{
			GameID:     c.GameID,
			MinPlayers: minP,
			MaxPlayers: maxP,
			MafiaCount: mafia,
			Seed:       c.Seed,
		},
	}, nil
}

// applyAddPlayer enrolls a player in the lobby.
//
// Validation:
//   - Game must exist and be in PhaseLobby.
//   - Roles must not have been dealt yet (StartGame closes the lobby
//     even though the game stays in PhaseLobby until BeginNight).
//   - PlayerID and Name must be non-empty.
//   - PlayerID must not already be in the game.
//   - The lobby must not already be at MaxPlayers.
func (g *Game) applyAddPlayer(c AddPlayer) ([]Event, error) {
	if g.state.id == "" {
		return nil, ErrWrongPhase
	}
	if g.state.phase != PhaseLobby {
		return nil, ErrWrongPhase
	}
	// Closing the lobby at StartGame (rather than BeginNight) is a
	// deliberate UX choice: once roles have been dealt, adding a new
	// player would require re-dealing the whole roster, which leaks
	// information (existing players would see new RoleAssigned events
	// and could infer their previous role was unstable). Bouncing the
	// joiner is the only sane option. The same wrong_phase error code
	// flows out to the client, which surfaces "This game is already
	// in progress" in the join lobby.
	if len(g.state.players) > 0 && g.state.players[0].role != "" {
		return nil, ErrWrongPhase
	}
	if c.PlayerID == "" || c.Name == "" {
		return nil, fmt.Errorf("game: AddPlayer requires non-empty PlayerID and Name")
	}
	if _, exists := g.state.findPlayer(c.PlayerID); exists {
		return nil, ErrDuplicatePlayer
	}
	if len(g.state.players) >= g.state.maxPlayers {
		return nil, ErrLobbyFull
	}

	g.state.players = append(g.state.players, Player{
		id:    c.PlayerID,
		name:  c.Name,
		alive: true,
	})

	// Commands and events are conceptually distinct vocabularies even
	// when they happen to share field shapes; avoid a structural cast
	// here so the two stay independently evolvable.
	//nolint:gosimple // see comment above.
	return []Event{PlayerJoined{PlayerID: c.PlayerID, Name: c.Name}}, nil
}

// applySetMafiaCount adjusts the planned mafia count during PhaseLobby.
// See SetMafiaCount in command.go for the validity envelope.
func (g *Game) applySetMafiaCount(c SetMafiaCount) ([]Event, error) {
	if g.state.id == "" {
		return nil, ErrWrongPhase
	}
	if g.state.phase != PhaseLobby {
		return nil, ErrWrongPhase
	}
	maxMafia := g.state.maxPlayers - (reservedTownRoles + 1)
	if c.Count < 1 || c.Count > maxMafia {
		// Wrap ErrRosterMismatch so room.errorFor maps this to
		// wire.ErrCodeRosterMismatch (consistent with the matching
		// rejection in applyStartGame) while still surfacing the
		// concrete count and bound in the message for logs and the
		// UI's error banner.
		return nil, fmt.Errorf("%w: mafia count %d out of range [1, %d]",
			ErrRosterMismatch, c.Count, maxMafia)
	}
	if c.Count == g.state.mafiaCount {
		return nil, ErrNoChange
	}

	prev := g.state.mafiaCount
	g.state.mafiaCount = c.Count
	return []Event{MafiaCountChanged{From: prev, To: c.Count}}, nil
}

// applyStartGame deals roles and locks the lobby; the game stays in
// PhaseLobby until the host issues BeginNight.
//
// Validation:
//   - Game must exist and be in PhaseLobby.
//   - PlayerCount must be in [MinPlayers, MaxPlayers].
//   - MafiaCount must leave room for the reserved town core, i.e.
//     mafiaCount ≤ playerCount - reservedTownRoles - 1.
//   - StartGame is rejected if roles have already been dealt (we detect
//     this by checking whether the first player has a non-empty role).
//
// Composition: with N players and M mafia, the dealt roles are
//
//	[Mafia × M, Detective, Doctor, Villager × (N - M - 2)]
//
// shuffled by a PCG seeded from state.seed so replay is deterministic.
//
// Emits GameStarted + one RoleAssigned per player. No PhaseChanged —
// the transition to PhaseNight is the host's BeginNight job.
func (g *Game) applyStartGame(_ StartGame) ([]Event, error) {
	if g.state.id == "" {
		return nil, ErrWrongPhase
	}
	if g.state.phase != PhaseLobby {
		return nil, ErrWrongPhase
	}
	// If any player already has a role, StartGame has already run.
	if len(g.state.players) > 0 && g.state.players[0].role != "" {
		return nil, ErrWrongPhase
	}
	n := len(g.state.players)
	if n < g.state.minPlayers || n > g.state.maxPlayers {
		return nil, ErrRosterMismatch
	}
	maxMafia := n - reservedTownRoles - 1
	if g.state.mafiaCount < 1 || g.state.mafiaCount > maxMafia {
		return nil, ErrRosterMismatch
	}

	dealt := composeRoster(n, g.state.mafiaCount)

	// Use rand/v2 PCG with a derived seed. We split the user-supplied
	// int64 into two uint64 halves of fresh entropy so two games that
	// happen to share Seed=0 (or any int64) still get a well-distributed
	// PCG state. Casting through uint64 avoids sign-extension bias.
	seed := uint64(g.state.seed)
	r := rand.New(rand.NewPCG(seed, ^seed))
	r.Shuffle(len(dealt), func(i, j int) { dealt[i], dealt[j] = dealt[j], dealt[i] })

	events := make([]Event, 0, len(dealt)+1)
	events = append(events, GameStarted{})

	for i := range g.state.players {
		g.state.players[i].role = dealt[i]
		events = append(events, RoleAssigned{
			PlayerID: g.state.players[i].id,
			Role:     dealt[i],
		})
	}

	return events, nil
}

// composeRoster builds the role multiset for a game with `n` players
// and `mafia` mafia. Composition is fixed:
//
//	mafia × Mafia + 1 × Detective + 1 × Doctor + (n - mafia - 2) × Villager
//
// Caller must have validated that the inputs satisfy
// 1 ≤ mafia ≤ n - reservedTownRoles - 1.
func composeRoster(n, mafia int) []Role {
	out := make([]Role, 0, n)
	for i := 0; i < mafia; i++ {
		out = append(out, RoleMafia)
	}
	out = append(out, RoleDetective, RoleDoctor)
	for i := 0; i < n-mafia-reservedTownRoles; i++ {
		out = append(out, RoleVillager)
	}
	return out
}
