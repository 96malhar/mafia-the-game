package game_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/96malhar/mafia-the-game/internal/game"
)

// Shared test helpers for the game_test package. Fixtures and night/day
// orchestration that more than one *_test.go file needs live here so the
// individual suites stay focused on assertions rather than plumbing.

// addPlayers joins each id to the lobby (Name == string(id)), failing the
// test on any rejection. It collapses the repeated AddPlayer fill loops.
func addPlayers(t *testing.T, g *game.Game, ids ...game.PlayerID) {
	t.Helper()
	for _, id := range ids {
		_, err := g.Apply(game.AddPlayer{PlayerID: id, Name: string(id)})
		require.NoError(t, err)
	}
}

// fillLobbyN creates a standard 5..20 game (seed-controlled) and joins n
// players named a, b, c, … It returns the game still in PhaseLobby, ready
// for StartGame.
func fillLobbyN(t *testing.T, seed int64, n int) *game.Game {
	t.Helper()
	g := game.New()
	_, err := g.Apply(standardCreate("g1", seed))
	require.NoError(t, err)
	for i := range n {
		pid := game.PlayerID(string(rune('a' + i)))
		_, err := g.Apply(game.AddPlayer{PlayerID: pid, Name: string(pid)})
		require.NoError(t, err)
	}
	return g
}

// rosterDeal describes a deterministic role assignment to brute-force a
// seed for. ids is the join order (and the roster size); wanted maps each
// id to the role it must be dealt. The optional toggles take a villager
// slot, exactly as in a real game.
type rosterDeal struct {
	ids        []game.PlayerID
	wanted     map[game.PlayerID]game.Role
	mafiaCount int
	consort    bool
	vigilante  bool
	yakuza     bool
	maxSeeds   int64
}

// fixedRosterDealt brute-forces seeds until StartGame deals exactly d.wanted,
// then returns the game (with roles dealt, still in PhaseLobby) together with
// the raw StartGame event batch. It is the seed-search core shared by
// fixedRosterMatching and by the rare test that must assert on the
// StartGame-time batch itself (e.g. the MafiaRosterRevealed reveal). With
// small rosters the permutation space is tiny, so a matching seed is found
// almost immediately.
func fixedRosterDealt(t *testing.T, d rosterDeal) (*game.Game, []game.Event) {
	t.Helper()
	for seed := range d.maxSeeds {
		g := game.New()
		_, err := g.Apply(game.CreateGame{
			GameID:     "g1",
			MinPlayers: len(d.ids),
			MaxPlayers: 20,
			MafiaCount: d.mafiaCount,
			Seed:       seed,
		})
		require.NoError(t, err)
		if d.consort {
			_, err = g.Apply(game.SetConsort{Enabled: true})
			require.NoError(t, err)
		}
		if d.vigilante {
			_, err = g.Apply(game.SetVigilante{Enabled: true})
			require.NoError(t, err)
		}
		if d.yakuza {
			_, err = g.Apply(game.SetYakuza{Enabled: true})
			require.NoError(t, err)
		}
		addPlayers(t, g, d.ids...)
		start, err := g.Apply(game.StartGame{})
		require.NoError(t, err)

		if !rosterMatches(g, d.wanted) {
			continue
		}
		return g, start
	}
	t.Fatalf("no seed in %d attempts yielded the wanted roster", d.maxSeeds)
	return nil, nil
}

// fixedRosterMatching brute-forces seeds until StartGame deals exactly
// d.wanted (via fixedRosterDealt), then walks BeginNight → opening → mafia
// narrate → mafia act, leaving the game on the mafia's act window. It is the
// shared core of every fixed-roster fixture (fixedRoster and the optional-role
// variants).
func fixedRosterMatching(t *testing.T, d rosterDeal) *game.Game {
	t.Helper()
	g, _ := fixedRosterDealt(t, d)
	beginNightToMafiaAct(t, g)
	return g
}

// fixedRoster2Mafia builds a deterministic 5-player, 2-mafia game:
//
//	mafia1 -> RoleMafia
//	mafia2 -> RoleMafia
//	det    -> RoleDetective
//	doc    -> RoleDoctor
//	town1  -> RoleVillager
//
// Town faction is det+doc+town1 (3) vs 2 mafia-aligned, so a single
// unsaved mafia kill drops the town to 2 and reaches parity (mafia win).
// On return the game is on the mafia's act window. Used by mafia tests
// that need a second mafioso (faction-collective behaviour) or a fast
// parity win.
func fixedRoster2Mafia(t *testing.T) *game.Game {
	t.Helper()
	return fixedRosterMatching(t, rosterDeal{
		ids: []game.PlayerID{"mafia1", "mafia2", "det", "doc", "town1"},
		wanted: map[game.PlayerID]game.Role{
			"mafia1": game.RoleMafia,
			"mafia2": game.RoleMafia,
			"det":    game.RoleDetective,
			"doc":    game.RoleDoctor,
			"town1":  game.RoleVillager,
		},
		mafiaCount: 2,
		maxSeeds:   5000,
	})
}

// rosterMatches reports whether every dealt player's role equals wanted.
func rosterMatches(g *game.Game, wanted map[game.PlayerID]game.Role) bool {
	for _, p := range g.State().Players() {
		if wanted[p.ID()] != p.Role() {
			return false
		}
	}
	return true
}

// beginNightToMafiaAct issues BeginNight and walks opening → mafia narrate
// → mafia act, leaving the game on the mafia's act window. It is the
// common postcondition shared by every fixture and by toNextNight.
func beginNightToMafiaAct(t *testing.T, g *game.Game) {
	t.Helper()
	_, err := g.Apply(game.BeginNight{})
	require.NoError(t, err)
	advanceToMafiaAct(t, g)
}

// advanceToMafiaAct walks the engine from "just entered PhaseNight" (i.e.
// sub-phase = opening, just after BeginNight) to the mafia's act window.
func advanceToMafiaAct(t *testing.T, g *game.Game) {
	t.Helper()
	require.Equal(t, game.NightSubOpening, g.State().CurrentNightSubPhase())
	advancePhase(t, g) // opening → mafia narrate
	require.Equal(t, game.RoleMafia, g.State().CurrentNightRole())
	require.Equal(t, game.NightSubNarrate, g.State().CurrentNightSubPhase())
	advancePhase(t, g) // mafia narrate → mafia act
	require.Equal(t, game.NightSubAct, g.State().CurrentNightSubPhase(),
		"fixture must leave the game on the mafia act window")
}

// runNightToDay walks from the mafia act window through the whole night in
// the given role order, submitting the per-role actions; any role missing
// from the map (or arriving on a phantom ponder rather than an act window)
// times out. It returns every event emitted across all sub-phase
// transitions and the resolve batch. The game ends on PhaseDayDiscussion
// (or PhaseEnded if the night resolved a win).
func runNightToDay(t *testing.T, g *game.Game, order []game.Role, actions map[game.Role]game.PlayerID) []game.Event {
	t.Helper()
	var all []game.Event
	for _, r := range order {
		if g.State().Phase() != game.PhaseNight {
			return all
		}
		require.Equal(t, r, g.State().CurrentNightRole(),
			"expected %s's turn but got %s", r, g.State().CurrentNightRole())
		if target, ok := actions[r]; ok && g.State().CurrentNightSubPhase() == game.NightSubAct {
			actor := livingHolder(t, g, r)
			all = append(all, nightAction(t, g, actor, target)...)
		}
		all = append(all, walkRestOfTurn(t, g)...)
	}
	return all
}

// finishNight walks any remaining night sub-phases until the game leaves
// PhaseNight, returning every event emitted along the way. Used by tests
// that submit (or reject) an action mid-night and then need the night to
// resolve.
func finishNight(t *testing.T, g *game.Game) []game.Event {
	t.Helper()
	var evts []game.Event
	for g.State().Phase() == game.PhaseNight {
		evts = append(evts, walkRestOfTurn(t, g)...)
	}
	return evts
}

// livingHolder returns the first living player holding role r.
func livingHolder(t *testing.T, g *game.Game, r game.Role) game.PlayerID {
	t.Helper()
	for _, p := range g.State().Players() {
		if p.Alive() && p.Role() == r {
			return p.ID()
		}
	}
	t.Fatalf("no living holder of role %s", r)
	return ""
}

// livingByID reports whether the player with the given id is alive.
func livingByID(g *game.Game, id game.PlayerID) bool {
	for _, p := range g.State().Players() {
		if p.ID() == id {
			return p.Alive()
		}
	}
	return false
}

// roleByID returns the role of the player with the given id, or the zero
// Role if no such player exists. Mirrors livingByID for the suites that
// assert on a single named player's role.
func roleByID(g *game.Game, id game.PlayerID) game.Role {
	for _, p := range g.State().Players() {
		if p.ID() == id {
			return p.Role()
		}
	}
	return ""
}

// finalizeLynch drives the current DayDiscussion to a lynch of target: it
// opens voting, has every living player except the target vote for the
// target (always a strict majority), and finalizes. It returns the
// FinalizeVotes events. The game may end (a win) or return to
// DayDiscussion with the lynch resolved.
func finalizeLynch(t *testing.T, g *game.Game, target game.PlayerID) []game.Event {
	t.Helper()
	require.Equal(t, game.PhaseDayDiscussion, g.State().Phase())
	_, err := g.Apply(game.OpenVoting{})
	require.NoError(t, err)
	for _, p := range g.State().Players() {
		if !p.Alive() || p.ID() == target {
			continue
		}
		_, err := g.Apply(game.DayVote{Voter: p.ID(), Target: target})
		require.NoError(t, err)
	}
	evts, err := g.Apply(game.FinalizeVotes{})
	require.NoError(t, err)
	return evts
}

// noLynchDay advances a PhaseDayDiscussion to the next-night boundary with
// no lynch: open voting, finalize an empty tally (NoLynch).
func noLynchDay(t *testing.T, g *game.Game) {
	t.Helper()
	require.Equal(t, game.PhaseDayDiscussion, g.State().Phase())
	_, err := g.Apply(game.OpenVoting{})
	require.NoError(t, err)
	_, err = g.Apply(game.FinalizeVotes{})
	require.NoError(t, err)
}

// intoDayVote returns the standard 5-player fixedRoster walked into
// PhaseDayVote with the mafia's kill saved by the doctor (so all five
// players remain alive to vote). It's the shared setup for the DayVote /
// VoteResolution / RevealVotes suites.
func intoDayVote(t *testing.T) *game.Game {
	t.Helper()
	g := fixedRoster(t)
	toDayVote(t, g, map[game.Role]game.PlayerID{
		game.RoleMafia:  "town1",
		game.RoleDoctor: "town1", // save -> nobody dies
	})
	return g
}

// mkEndedGame drives a fresh 5-player, 2-mafia game all the way to
// PhaseEnded (a mafia win) and returns it. Town is {detective, doctor,
// villager}; exact parity (2-vs-2) plays on, so it takes TWO unsaved night
// kills for the mafia to strictly outnumber the town and win. The returned
// game is asserted to be in PhaseEnded. Shared by the tests that exercise
// behaviour against a finished game.
func mkEndedGame(t *testing.T) *game.Game {
	t.Helper()
	g := game.New()
	_, err := g.Apply(game.CreateGame{
		GameID: "g1", MinPlayers: 5, MaxPlayers: 20, MafiaCount: 2, Seed: 1,
	})
	require.NoError(t, err)
	addPlayers(t, g, "a", "b", "c", "d", "e")
	_, err = g.Apply(game.StartGame{})
	require.NoError(t, err)
	_, err = g.Apply(game.BeginNight{})
	require.NoError(t, err)
	advanceToMafiaAct(t, g)

	// mafiaKill picks an arbitrary living town target and walks the canonical
	// 3-role night (mafia → det → doc) to resolution via playNight, which
	// finds the living mafioso actor itself and returns once the night
	// resolves.
	mafiaKill := func() {
		var victim game.PlayerID
		for _, p := range g.State().Players() {
			if p.Alive() && p.Role() != game.RoleMafia {
				victim = p.ID()
				break
			}
		}
		require.NotEmpty(t, victim)
		playNight(t, g, map[game.Role]game.PlayerID{game.RoleMafia: victim})
	}

	mafiaKill() // 2 mafia vs 2 town — parity, game continues
	require.Equal(t, game.PhaseDayDiscussion, g.State().Phase())

	noLynchDay(t, g)
	beginNightToMafiaAct(t, g)

	mafiaKill() // 2 mafia vs 1 town — mafia outnumber, game ends
	require.Equal(t, game.PhaseEnded, g.State().Phase(),
		"precondition: game must be in PhaseEnded")
	return g
}

// findAllEvents returns every event matching the concrete type T, in
// order. Used by tests that assert on the COUNT of an event (e.g. exactly
// one PlayerKilled across two shots at one target).
func findAllEvents[T game.Event](events []game.Event) []T {
	var out []T
	for _, e := range events {
		if v, ok := e.(T); ok {
			out = append(out, v)
		}
	}
	return out
}

// indexOfEvent returns the position of the first event of concrete type T
// in events, or -1 if none is present. Pairs with requireEventOrder to pin
// the relative emission order of a batch (e.g. a death-resolution batch).
func indexOfEvent[T game.Event](events []game.Event) int {
	for i, e := range events {
		if _, ok := e.(T); ok {
			return i
		}
	}
	return -1
}

// orderedEvent pairs a human-readable label with the index returned by
// indexOfEvent, so requireEventOrder can name the offender on a failure.
type orderedEvent struct {
	label string
	at    int
}

// requireEventOrder asserts that every listed event is present in its batch
// (index >= 0) AND that the events appear in exactly the given order
// (strictly increasing indices). Use it with indexOfEvent to lock down an
// emission sequence that presence-only checks (findEvent) would miss — e.g.
// kill → promote → reveal → phase-transition in a resolution batch.
func requireEventOrder(t *testing.T, events ...orderedEvent) {
	t.Helper()
	for _, e := range events {
		require.NotEqualf(t, -1, e.at, "batch must contain %s", e.label)
	}
	for i := 1; i < len(events); i++ {
		require.Lessf(t, events[i-1].at, events[i].at,
			"%s must appear before %s", events[i-1].label, events[i].label)
	}
}
