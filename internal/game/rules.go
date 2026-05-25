package game

import (
	"fmt"
	"math/rand/v2"
)

// applyCreateGame initializes a fresh game with the given roster and
// seed. The engine must not have been created before (state.id == "").
//
// Validation:
//   - The game must be in its pristine post-New() state.
//   - Every role in Roles must be a known role.
//   - The roster must contain at least one mafia and at least one town
//     player; otherwise the game has no possible win condition.
func (g *Game) applyCreateGame(c CreateGame) ([]Event, error) {
	if g.state.id != "" {
		return nil, ErrWrongPhase
	}
	if c.GameID == "" {
		return nil, fmt.Errorf("game: CreateGame.GameID must not be empty")
	}
	if len(c.Roles) < 3 {
		return nil, fmt.Errorf("game: need at least 3 roles to form a game, got %d", len(c.Roles))
	}

	var mafia, town int
	for _, r := range c.Roles {
		if !r.Valid() {
			return nil, fmt.Errorf("game: invalid role %q in roster", r)
		}
		if r.Faction() == FactionMafia {
			mafia++
		} else {
			town++
		}
	}
	if mafia == 0 || town == 0 {
		return nil, fmt.Errorf("game: roster needs at least one mafia and one town (got %d mafia, %d town)", mafia, town)
	}

	g.state.id = c.GameID
	g.state.seed = c.Seed
	g.state.roles = append([]Role(nil), c.Roles...) // defensive copy

	return []Event{
		GameCreated{
			GameID: c.GameID,
			Roles:  append([]Role(nil), c.Roles...),
			Seed:   c.Seed,
		},
	}, nil
}

// applyAddPlayer enrolls a player in the lobby.
//
// Validation:
//   - Game must exist and be in PhaseLobby.
//   - PlayerID and Name must be non-empty.
//   - PlayerID must not already be in the game.
//   - The lobby must not already be full (player count < roster size).
func (g *Game) applyAddPlayer(c AddPlayer) ([]Event, error) {
	if g.state.id == "" {
		return nil, ErrWrongPhase
	}
	if g.state.phase != PhaseLobby {
		return nil, ErrWrongPhase
	}
	if c.PlayerID == "" || c.Name == "" {
		return nil, fmt.Errorf("game: AddPlayer requires non-empty PlayerID and Name")
	}
	if _, exists := g.state.findPlayer(c.PlayerID); exists {
		return nil, ErrDuplicatePlayer
	}
	if len(g.state.players) >= len(g.state.roles) {
		return nil, fmt.Errorf("game: lobby full (%d/%d)", len(g.state.players), len(g.state.roles))
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

// applyStartGame transitions Lobby -> Night 0 and deals roles.
//
// Validation:
//   - Game must exist and be in PhaseLobby.
//   - Player count must exactly match the configured roster size.
//
// Determinism: we shuffle a fresh copy of state.roles using a Rand
// seeded from state.seed (math/rand/v2). Same seed + same join order
// always yields the same assignment, which makes replays and tests
// reproducible without exposing seeded randomness to callers.
func (g *Game) applyStartGame(_ StartGame) ([]Event, error) {
	if g.state.id == "" {
		return nil, ErrWrongPhase
	}
	if g.state.phase != PhaseLobby {
		return nil, ErrWrongPhase
	}
	if len(g.state.players) != len(g.state.roles) {
		return nil, ErrRosterMismatch
	}

	// Use rand/v2 PCG with a derived seed. We split the user-supplied
	// int64 into two uint64 halves of fresh entropy so two games that
	// happen to share Seed=0 (or any int64) still get a well-distributed
	// PCG state. Casting through uint64 avoids sign-extension bias.
	seed := uint64(g.state.seed)
	r := rand.New(rand.NewPCG(seed, ^seed))

	dealt := append([]Role(nil), g.state.roles...)
	r.Shuffle(len(dealt), func(i, j int) { dealt[i], dealt[j] = dealt[j], dealt[i] })

	events := make([]Event, 0, len(dealt)+2)
	events = append(events, GameStarted{})

	for i := range g.state.players {
		g.state.players[i].role = dealt[i]
		events = append(events, RoleAssigned{
			PlayerID: g.state.players[i].id,
			Role:     dealt[i],
		})
	}

	from := g.state.phase
	g.state.phase = PhaseNight
	g.state.day = 0
	events = append(events, PhaseChanged{From: from, To: PhaseNight, Day: 0})

	return events, nil
}
