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
	maxSeeds   int64
}

// fixedRosterMatching brute-forces seeds until StartGame deals exactly
// d.wanted, then walks BeginNight → opening → mafia narrate → mafia act,
// leaving the game on the mafia's act window. It is the shared core of
// every fixed-roster fixture (fixedRoster and the optional-role variants).
// With small rosters the permutation space is tiny, so a matching seed is
// found almost immediately.
func fixedRosterMatching(t *testing.T, d rosterDeal) *game.Game {
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
		addPlayers(t, g, d.ids...)
		_, err = g.Apply(game.StartGame{})
		require.NoError(t, err)

		if !rosterMatches(g, d.wanted) {
			continue
		}
		beginNightToMafiaAct(t, g)
		return g
	}
	t.Fatalf("no seed in %d attempts yielded the wanted roster", d.maxSeeds)
	return nil
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
