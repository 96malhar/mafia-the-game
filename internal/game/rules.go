package game

import (
	"fmt"
	"math/rand/v2"
	"strings"
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
	m := max(minPlayers/3, 1)
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
//   - PlayerID must be non-empty, and Name must be non-empty after
//     trimming leading/trailing whitespace (so " " is rejected, not
//     silently accepted as a blank row).
//   - PlayerID must not already be in the game.
//   - Name must not collide with any existing player's name (compared
//     case-insensitively, after trimming both sides). This is a UX
//     property: with PlayerIDs no longer rendered in the client UI,
//     two "Alice"s would be visually indistinguishable on the
//     Players panel. Storing the trimmed-but-case-preserved form
//     means " Alice " becomes "Alice" on the roster.
//   - The lobby must not already be at MaxPlayers.
func (g *Game) applyAddPlayer(c AddPlayer) ([]Event, error) {
	// Closing the lobby at StartGame (rather than BeginNight) is a
	// deliberate UX choice: once roles have been dealt, adding a new
	// player would require re-dealing the whole roster, which leaks
	// information (existing players would see new RoleAssigned events
	// and could infer their previous role was unstable). Bouncing the
	// joiner is the only sane option. requireLobbyOpen surfaces
	// ErrWrongPhase ("already in progress") for a dealt/started lobby
	// and ErrGameEnded ("game has already ended") for a finished game.
	if err := g.requireLobbyOpen(); err != nil {
		return nil, err
	}
	name := strings.TrimSpace(c.Name)
	if c.PlayerID == "" || name == "" {
		return nil, fmt.Errorf("game: AddPlayer requires non-empty PlayerID and Name")
	}
	if _, exists := g.state.findPlayer(c.PlayerID); exists {
		return nil, ErrDuplicatePlayer
	}
	// Case-insensitive name dedup. n is the lobby's max size (≤20 in
	// the default config), so the linear scan is fine and we don't
	// maintain a side index. EqualFold avoids allocating two
	// ToLower'd strings per comparison.
	for i := range g.state.players {
		if strings.EqualFold(g.state.players[i].name, name) {
			return nil, ErrDuplicateName
		}
	}
	if len(g.state.players) >= g.state.maxPlayers {
		return nil, ErrLobbyFull
	}

	g.state.players = append(g.state.players, Player{
		id:    c.PlayerID,
		name:  name,
		alive: true,
	})

	// Commands and events are conceptually distinct vocabularies even
	// when they happen to share field shapes; avoid a structural cast
	// here so the two stay independently evolvable.
	//nolint:gosimple // see comment above.
	return []Event{PlayerJoined{PlayerID: c.PlayerID, Name: name}}, nil
}

// applySetMafiaCount adjusts the planned mafia count during PhaseLobby.
// See SetMafiaCount in command.go for the validity envelope.
func (g *Game) applySetMafiaCount(c SetMafiaCount) ([]Event, error) {
	// Locking the picker at StartGame (rather than at BeginNight) is
	// a deliberate UX choice: once roles have been dealt, tweaking
	// the planned mafia count would do nothing — composeRoster has
	// already run and the per-player role assignments are committed.
	// requireLobbyOpen rejects so the host can't be fooled into
	// thinking a late adjustment took effect.
	if err := g.requireLobbyOpen(); err != nil {
		return nil, err
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

// applySetConsort toggles the optional Consort role during PhaseLobby.
// See SetConsort in command.go for the validity envelope. Like the mafia
// count, the toggle is locked once roles are dealt (composeRoster has
// already run), so a late flip is rejected rather than silently ignored.
//
// SetConsort and ConsortChanged are structurally identical (a single
// Enabled bool), so a direct conversion is clean; if either ever gains a
// field, this stops compiling and forces a re-check.
func (g *Game) applySetConsort(c SetConsort) ([]Event, error) {
	return g.applyLobbyToggle(c.Enabled, &g.state.consortEnabled, ConsortChanged(c))
}

// applySetVigilante toggles the optional Vigilante role during
// PhaseLobby. See SetVigilante in command.go for the validity envelope.
// Locked once roles are dealt, like every other lobby toggle.
func (g *Game) applySetVigilante(c SetVigilante) ([]Event, error) {
	return g.applyLobbyToggle(c.Enabled, &g.state.vigilanteEnabled, VigilanteChanged(c))
}

// applySetYakuza toggles the optional Yakuza role during PhaseLobby. See
// SetYakuza in command.go for the validity envelope. Locked once roles are
// dealt, like every other lobby toggle.
func (g *Game) applySetYakuza(c SetYakuza) ([]Event, error) {
	return g.applyLobbyToggle(c.Enabled, &g.state.yakuzaEnabled, YakuzaChanged(c))
}

// applySetTracker toggles the optional Tracker role during PhaseLobby. See
// SetTracker in command.go for the validity envelope. Locked once roles are
// dealt, like every other lobby toggle.
func (g *Game) applySetTracker(c SetTracker) ([]Event, error) {
	return g.applyLobbyToggle(c.Enabled, &g.state.trackerEnabled, TrackerChanged(c))
}

// applyStartGame deals roles and locks the lobby; the game stays in
// PhaseLobby until the host issues BeginNight.
//
// Validation:
//   - Game must exist and be in PhaseLobby.
//   - PlayerCount must be in [MinPlayers, MaxPlayers].
//   - MafiaCount must leave room for the reserved town core, i.e.
//     mafiaCount ≤ playerCount - reservedTownRoles - 1.
//   - At least one plain Villager must remain after the mafia, the
//     reserved town core, and every enabled optional role, i.e.
//     playerCount - mafiaCount - reservedTownRoles - #optionals ≥ 1.
//   - The dealt roster must not open at strict-mafia parity or worse, i.e.
//     we require 2*mafiaCount + mafiaAlignedOptionals < playerCount (town
//     is playerCount - mafiaCount - mafiaAlignedOptionals; a Consort takes
//     a villager slot but isn't town, and only the Consort is mafia-
//     aligned). This is INTENTIONALLY stricter than checkWin, which now
//     ends a mafia win only at strictMafia > town (plus the 1-vs-1
//     endgame): see the inline comment for why a parity open is still an
//     unfair, effectively-decided board we refuse to deal.
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
	// requireLobbyOpen also rejects a second StartGame (rolesDealt is set
	// the first time it succeeds) as a no-op error rather than a re-deal.
	if err := g.requireLobbyOpen(); err != nil {
		return nil, err
	}
	n := len(g.state.players)
	if n < g.state.minPlayers || n > g.state.maxPlayers {
		return nil, ErrRosterMismatch
	}
	maxMafia := n - reservedTownRoles - 1
	if g.state.mafiaCount < 1 || g.state.mafiaCount > maxMafia {
		return nil, ErrRosterMismatch
	}
	// Each optional role (Consort, Vigilante, …) takes the slot of a
	// villager. Require at least ONE plain villager to remain after the
	// mafia, the reserved town core, and every enabled optional role. A
	// zero-villager roster is degenerate (every town player has a special
	// power, removing the "vanilla" majority the social game leans on),
	// and when the optionals are over-stacked it would also leave
	// composeRoster short of n roles and panic the per-player assignment
	// loop. So the villager slots left must be >= 1.
	optionals := g.state.enabledOptionalRoles()
	if n-g.state.mafiaCount-reservedTownRoles-len(optionals) < 1 {
		return nil, ErrRosterMismatch
	}

	// Refuse to deal a roster that opens at strict-mafia parity or worse
	// (strictMafia >= town). This is a deliberately CONSERVATIVE guardrail,
	// stricter than checkWin: checkWin no longer ends the game at exact
	// parity (it ends a mafia win at strictMafia > town, plus the
	// 1-mafia-vs-1-town endgame), so a parity board would technically play
	// on. We still reject it because such an opening is effectively decided
	// for the mafia: the >=1-villager + reserved-core (det+doc) rules force
	// town >= 3, so parity means strictMafia >= 3 — either a mafia-aligned
	// voting majority (with a Consort the town can never lynch) or a board
	// where the first night kill alone pushes it to strictMafia > town. We
	// don't make players sit through a lost game. At deal time the strict
	// mafia count is mafiaCount and the town faction is everyone who is
	// neither mafia nor a mafia-aligned optional (the Consort), i.e.
	// town = n - mafiaCount - mafiaAlignedOptionals. "Below parity" is
	// mafiaCount < town, i.e. 2*mafiaCount + mafiaAlignedOptionals < n.
	mafiaAlignedOptionals := 0
	for _, r := range optionals {
		if r.Faction().MafiaAligned() {
			mafiaAlignedOptionals++
		}
	}
	if 2*g.state.mafiaCount+mafiaAlignedOptionals >= n {
		return nil, ErrRosterMismatch
	}

	dealt := composeRoster(n, g.state.mafiaCount, optionals)

	// Use rand/v2 PCG with a derived seed. We split the user-supplied
	// int64 into two uint64 halves of fresh entropy so two games that
	// happen to share Seed=0 (or any int64) still get a well-distributed
	// PCG state. Casting through uint64 avoids sign-extension bias.
	seed := uint64(g.state.seed)
	r := rand.New(rand.NewPCG(seed, ^seed))
	r.Shuffle(len(dealt), func(i, j int) { dealt[i], dealt[j] = dealt[j], dealt[i] })

	events := make([]Event, 0, len(dealt)+1)
	events = append(events, GameStarted{})

	var mafiaIDs []PlayerID
	var yakuzaID PlayerID
	for i := range g.state.players {
		g.state.players[i].role = dealt[i]
		events = append(events, RoleAssigned{
			PlayerID: g.state.players[i].id,
			Role:     dealt[i],
		})
		if dealt[i].Faction() == FactionMafia {
			mafiaIDs = append(mafiaIDs, g.state.players[i].id)
		}
		if dealt[i] == RoleYakuza {
			yakuzaID = g.state.players[i].id
		}
	}
	g.state.rolesDealt = true

	// Reveal the full mafia roster to the mafia faction so each member
	// knows their teammates (FactionOnly, so town never sees it). Emitted
	// after the RoleAssigned events so a client has every player's slot
	// before it learns which of them are allies. Yakuza names which member
	// is the Yakuza (empty when the role wasn't dealt) so the faction can
	// badge it distinctly.
	events = append(events, MafiaRosterRevealed{Members: mafiaIDs, Yakuza: yakuzaID})

	return events, nil
}

// applyResetGame returns a finished game to a fresh lobby in the same room,
// retaining the player roster (id + name) but wiping every per-game artifact.
// Valid only in PhaseEnded — the only terminal state a reset makes sense from.
//
// Unlike requireActiveGame-based handlers, this one deliberately ACCEPTS the
// ended game (that's its precondition), so it checks the phase directly
// rather than going through requirePhase (which rejects PhaseEnded).
//
// The emitted GameReset is a self-contained lobby snapshot: the room replaces
// its whole event log with it, so nothing from the previous game is replayed
// to future joiners. See the GameReset event doc.
func (g *Game) applyResetGame(c ResetGame) ([]Event, error) {
	if g.state.id == "" {
		return nil, ErrWrongPhase
	}
	if g.state.phase != PhaseEnded {
		// Not yet ended: a reset only makes sense once a game has finished.
		// (A mid-game "abandon and restart" would be a different command.)
		return nil, ErrWrongPhase
	}

	s := g.state

	// Fresh shuffle seed for the next deal (supplied by the room) so the
	// same roster doesn't redeal identical roles.
	s.seed = c.Seed

	// Reset config back to lobby defaults — the host re-tunes from scratch,
	// exactly like a brand-new room. minPlayers/maxPlayers are room identity
	// and stay; mafiaCount and every optional-role toggle reset.
	s.mafiaCount = defaultMafiaCount(s.minPlayers)
	s.consortEnabled = false
	s.vigilanteEnabled = false
	s.yakuzaEnabled = false
	s.trackerEnabled = false

	// Clear all per-game state. Roles are wiped and everyone is alive again;
	// the lobby reopens (rolesDealt == false) so new players can join before
	// the next deal.
	for i := range s.players {
		s.players[i].role = ""
		s.players[i].alive = true
	}
	s.phase = PhaseLobby
	s.day = 0
	s.rolesDealt = false
	s.vigilanteShotUsed = false
	s.recruitPending = false
	s.recruitYakuza = ""
	s.recruitTarget = ""
	s.pendingNight = nil
	s.votes = nil
	s.votesRevealed = false
	s.dayLynchResolved = false
	s.currentNightRole = ""
	s.currentNightSubPhase = ""
	s.nightTurnQueue = nil

	// Snapshot the retained roster for the self-contained GameReset event.
	retained := make([]ResetPlayer, len(s.players))
	for i, p := range s.players {
		retained[i] = ResetPlayer{ID: p.id, Name: p.name}
	}

	return []Event{GameReset{
		Players:    retained,
		MinPlayers: s.minPlayers,
		MaxPlayers: s.maxPlayers,
		MafiaCount: s.mafiaCount,
	}}, nil
}

// optionalRole describes a host-toggleable role that takes a villager
// slot at StartGame. The table is the single source of truth for the
// optional roster: enabledOptionalRoles and composeRoster both read it,
// so adding a new optional role is one entry plus its GameState flag.
type optionalRole struct {
	role    Role
	enabled func(*GameState) bool
}

var optionalRoles = []optionalRole{
	{role: RoleConsort, enabled: func(s *GameState) bool { return s.consortEnabled }},
	{role: RoleVigilante, enabled: func(s *GameState) bool { return s.vigilanteEnabled }},
	{role: RoleYakuza, enabled: func(s *GameState) bool { return s.yakuzaEnabled }},
	{role: RoleTracker, enabled: func(s *GameState) bool { return s.trackerEnabled }},
}

// enabledOptionalRoles returns the optional roles toggled on for the
// upcoming game, in table order. Each one takes the slot of a villager;
// the result drives both the StartGame roster-fit check and composeRoster.
func (s *GameState) enabledOptionalRoles() []Role {
	var out []Role
	for _, o := range optionalRoles {
		if o.enabled(s) {
			out = append(out, o.role)
		}
	}
	return out
}

// composeRoster builds the role multiset for a game with `n` players,
// `mafia` mafia, and the given enabled optional roles. Composition is
// fixed:
//
//	mafia × Mafia + 1 × Detective + 1 × Doctor +
//	  optionals… + (n - mafia - 2 - len(optionals)) × Villager
//
// Each optional role takes the slot of a villager. The caller (StartGame)
// must have validated that the mafia count plus the reserved town core
// plus the enabled optional roles leaves villager count ≥ 0.
func composeRoster(n, mafia int, optionals []Role) []Role {
	out := make([]Role, 0, n)
	for range mafia {
		out = append(out, RoleMafia)
	}
	out = append(out, RoleDetective, RoleDoctor)
	out = append(out, optionals...)
	reserved := reservedTownRoles + len(optionals)
	for range n - mafia - reserved {
		out = append(out, RoleVillager)
	}
	return out
}
