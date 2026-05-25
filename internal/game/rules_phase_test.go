package game_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/malhar/mafia-the-game/internal/game"
)

// --- test fixtures ---------------------------------------------------------

// fixedRoster builds a deterministic roster + game where player IDs map
// to specific roles regardless of seed. This avoids depending on the
// shuffler's output: tests call this, then directly inspect/override
// roles via the public Players() snapshot ordering.
//
// Roster layout (5 players):
//
//	mafia1   -> RoleMafia
//	det      -> RoleDetective
//	doc      -> RoleDoctor
//	town1    -> RoleVillager
//	town2    -> RoleVillager
//
// We arrange this by trying many seeds until we find one where, after
// StartGame, the mapping matches what we want. Brute force is fine —
// 5! = 120 permutations and the shuffler is deterministic per seed.
func fixedRoster(t *testing.T) *game.Game {
	t.Helper()
	roster := []game.Role{
		game.RoleMafia, game.RoleDetective, game.RoleDoctor,
		game.RoleVillager, game.RoleVillager,
	}
	ids := []game.PlayerID{"mafia1", "det", "doc", "town1", "town2"}
	wanted := map[game.PlayerID]game.Role{
		"mafia1": game.RoleMafia,
		"det":    game.RoleDetective,
		"doc":    game.RoleDoctor,
		"town1":  game.RoleVillager,
		"town2":  game.RoleVillager,
	}

	for seed := int64(0); seed < 1000; seed++ {
		g := game.New()
		_, err := g.Apply(game.CreateGame{GameID: "g1", Roles: roster, Seed: seed})
		require.NoError(t, err)
		for _, id := range ids {
			_, err := g.Apply(game.AddPlayer{PlayerID: id, Name: string(id)})
			require.NoError(t, err)
		}
		_, err = g.Apply(game.StartGame{})
		require.NoError(t, err)

		match := true
		for _, p := range g.State().Players() {
			if wanted[p.ID()] != p.Role() {
				match = false
				break
			}
		}
		if match {
			return g
		}
	}
	t.Fatalf("could not find a seed yielding the fixed role assignment in 1000 attempts")
	return nil
}

// findEvent (generic): redeclared here so this file is self-contained
// (we can't import unexported identifiers from rules_test.go's package
// view even though both files are in package game_test — but identifiers
// declared at package level are shared. So we DON'T redeclare; the
// helper defined in rules_test.go is visible here too).

// --- NightAction validation -----------------------------------------------

func TestNightAction_Validation(t *testing.T) {
	t.Run("rejected outside PhaseNight", func(t *testing.T) {
		g := game.New() // PhaseLobby
		_, err := g.Apply(game.NightAction{Actor: "mafia1", Target: "town1"})
		require.ErrorIs(t, err, game.ErrWrongPhase)
	})

	t.Run("villager has no night action", func(t *testing.T) {
		g := fixedRoster(t)
		_, err := g.Apply(game.NightAction{Actor: "town1", Target: "town2"})
		require.ErrorIs(t, err, game.ErrNotYourAction)
	})

	t.Run("unknown actor rejected", func(t *testing.T) {
		g := fixedRoster(t)
		_, err := g.Apply(game.NightAction{Actor: "ghost", Target: "town1"})
		require.ErrorIs(t, err, game.ErrUnknownPlayer)
	})

	t.Run("unknown target rejected", func(t *testing.T) {
		g := fixedRoster(t)
		_, err := g.Apply(game.NightAction{Actor: "mafia1", Target: "ghost"})
		require.ErrorIs(t, err, game.ErrUnknownPlayer)
	})

	t.Run("mafia cannot target mafia", func(t *testing.T) {
		// Only one mafia in this roster; mafia targeting self has two
		// reasons to fail. Use a roster with two mafia to isolate the
		// "same faction" rule.
		g := game.New()
		_, err := g.Apply(game.CreateGame{
			GameID: "g1",
			Roles: []game.Role{
				game.RoleMafia, game.RoleMafia, game.RoleDoctor,
				game.RoleVillager, game.RoleVillager,
			},
			Seed: 7,
		})
		require.NoError(t, err)
		for _, id := range []game.PlayerID{"a", "b", "c", "d", "e"} {
			_, err := g.Apply(game.AddPlayer{PlayerID: id, Name: string(id)})
			require.NoError(t, err)
		}
		_, err = g.Apply(game.StartGame{})
		require.NoError(t, err)

		// Find a mafia targeting another mafia.
		var mafias []game.PlayerID
		for _, p := range g.State().Players() {
			if p.Role() == game.RoleMafia {
				mafias = append(mafias, p.ID())
			}
		}
		require.Len(t, mafias, 2)
		_, err = g.Apply(game.NightAction{Actor: mafias[0], Target: mafias[1]})
		require.ErrorIs(t, err, game.ErrNotYourAction)
	})

	t.Run("detective cannot self-investigate", func(t *testing.T) {
		g := fixedRoster(t)
		_, err := g.Apply(game.NightAction{Actor: "det", Target: "det"})
		require.ErrorIs(t, err, game.ErrSelfTarget)
	})

	t.Run("doctor cannot self-save on first night", func(t *testing.T) {
		g := fixedRoster(t)
		_, err := g.Apply(game.NightAction{Actor: "doc", Target: "doc"})
		require.ErrorIs(t, err, game.ErrSelfTarget)
	})

	t.Run("doctor can self-save on later nights", func(t *testing.T) {
		g := fixedRoster(t)
		// First full night: mafia kills a villager so the game continues.
		_, err := g.Apply(game.NightAction{Actor: "mafia1", Target: "town1"})
		require.NoError(t, err)
		_, err = g.Apply(game.NightAction{Actor: "doc", Target: "town2"})
		require.NoError(t, err)
		// Run through Day cycle so we arrive at a second Night.
		_, _ = g.Apply(game.AdvancePhase{}) // Night -> DayDiscussion
		_, _ = g.Apply(game.AdvancePhase{}) // -> DayVote
		_, _ = g.Apply(game.AdvancePhase{}) // -> (no votes) extended
		_, _ = g.Apply(game.AdvancePhase{}) // -> (still none) Night
		require.Equal(t, game.PhaseNight, g.State().Phase())

		_, err = g.Apply(game.NightAction{Actor: "doc", Target: "doc"})
		require.NoError(t, err, "doctor self-save should be legal on night 2")
	})

	t.Run("re-submission is rejected (ErrAlreadyActed)", func(t *testing.T) {
		g := fixedRoster(t)
		_, err := g.Apply(game.NightAction{Actor: "mafia1", Target: "town1"})
		require.NoError(t, err)
		_, err = g.Apply(game.NightAction{Actor: "mafia1", Target: "town2"})
		require.ErrorIs(t, err, game.ErrAlreadyActed)
	})

	t.Run("empty target rejected", func(t *testing.T) {
		g := fixedRoster(t)
		_, err := g.Apply(game.NightAction{Actor: "mafia1", Target: ""})
		require.ErrorIs(t, err, game.ErrUnknownPlayer)
	})

	t.Run("dead target rejected", func(t *testing.T) {
		g := fixedRoster(t)
		_, _ = g.Apply(game.NightAction{Actor: "mafia1", Target: "town1"})
		_, _ = g.Apply(game.AdvancePhase{}) // night resolves; town1 dies; -> Day
		_, _ = g.Apply(game.AdvancePhase{}) // -> DayVote
		_, _ = g.Apply(game.AdvancePhase{}) // no votes -> extend
		_, _ = g.Apply(game.AdvancePhase{}) // still none -> Night 2
		require.Equal(t, game.PhaseNight, g.State().Phase())

		_, err := g.Apply(game.NightAction{Actor: "mafia1", Target: "town1"})
		require.ErrorIs(t, err, game.ErrPlayerDead)
	})

	t.Run("rejected before game is created", func(t *testing.T) {
		g := game.New()
		_, err := g.Apply(game.NightAction{Actor: "a", Target: "b"})
		require.ErrorIs(t, err, game.ErrWrongPhase)
	})

	t.Run("dead actor cannot act", func(t *testing.T) {
		g := fixedRoster(t)
		// Mafia kills town1, doctor saves nobody useful, game continues.
		_, _ = g.Apply(game.NightAction{Actor: "mafia1", Target: "town1"})
		_, _ = g.Apply(game.NightAction{Actor: "doc", Target: "town2"})
		_, _ = g.Apply(game.AdvancePhase{})
		_, _ = g.Apply(game.AdvancePhase{})
		// town1 is dead now, but town1 is a villager with no night action
		// anyway. Test the inverse: have the detective be killed and
		// then try to act on the next night.
		// Restart with detective as kill target.
		g = fixedRoster(t)
		_, _ = g.Apply(game.NightAction{Actor: "mafia1", Target: "det"})
		_, _ = g.Apply(game.AdvancePhase{}) // night resolves; det dies; ->day discussion
		_, _ = g.Apply(game.AdvancePhase{}) // -> day vote
		_, _ = g.Apply(game.AdvancePhase{}) // no votes -> extend
		_, _ = g.Apply(game.AdvancePhase{}) // still none -> night 2
		require.Equal(t, game.PhaseNight, g.State().Phase())

		_, err := g.Apply(game.NightAction{Actor: "det", Target: "town1"})
		require.ErrorIs(t, err, game.ErrPlayerDead)
	})
}

// --- Night resolution -----------------------------------------------------

func TestNightResolution(t *testing.T) {
	t.Run("mafia kill takes effect when not saved", func(t *testing.T) {
		g := fixedRoster(t)
		_, _ = g.Apply(game.NightAction{Actor: "mafia1", Target: "town1"})
		_, _ = g.Apply(game.NightAction{Actor: "doc", Target: "town2"})
		_, _ = g.Apply(game.NightAction{Actor: "det", Target: "mafia1"})

		evts, err := g.Apply(game.AdvancePhase{})
		require.NoError(t, err)

		killed, ok := findEvent[game.PlayerKilled](evts)
		require.True(t, ok)
		require.Equal(t, game.PlayerID("town1"), killed.PlayerID)

		for _, p := range g.State().Players() {
			if p.ID() == "town1" {
				require.False(t, p.Alive())
			}
		}
	})

	t.Run("doctor save cancels kill and emits private PlayerSaved", func(t *testing.T) {
		g := fixedRoster(t)
		_, _ = g.Apply(game.NightAction{Actor: "mafia1", Target: "town1"})
		_, _ = g.Apply(game.NightAction{Actor: "doc", Target: "town1"})

		evts, err := g.Apply(game.AdvancePhase{})
		require.NoError(t, err)

		_, killed := findEvent[game.PlayerKilled](evts)
		require.False(t, killed, "no PlayerKilled when saved")

		saved, ok := findEvent[game.PlayerSaved](evts)
		require.True(t, ok)
		require.Equal(t, game.PlayerID("town1"), saved.PlayerID)
		require.Equal(t, game.PlayerID("doc"), saved.Doctor)
		require.Equal(t, "player", saved.Visibility().Audience)
		require.Equal(t, game.PlayerID("doc"), saved.Visibility().Player)

		for _, p := range g.State().Players() {
			if p.ID() == "town1" {
				require.True(t, p.Alive(), "saved player should still be alive")
			}
		}
	})

	t.Run("detective result is private and correct", func(t *testing.T) {
		g := fixedRoster(t)
		_, _ = g.Apply(game.NightAction{Actor: "mafia1", Target: "town2"})
		_, _ = g.Apply(game.NightAction{Actor: "det", Target: "mafia1"})

		evts, err := g.Apply(game.AdvancePhase{})
		require.NoError(t, err)
		res, ok := findEvent[game.DetectiveResult](evts)
		require.True(t, ok)
		require.Equal(t, game.PlayerID("det"), res.Detective)
		require.Equal(t, game.PlayerID("mafia1"), res.Target)
		require.True(t, res.IsMafia)
		require.Equal(t, "player", res.Visibility().Audience)
		require.Equal(t, game.PlayerID("det"), res.Visibility().Player)
	})
}

// --- DayVote state table -------------------------------------------------

// TestDayVote_Validation covers the input-validation branches of
// applyDayVote that the state-table tests don't exercise.
func TestDayVote_Validation(t *testing.T) {
	intoDayVote := func(t *testing.T) *game.Game {
		t.Helper()
		g := fixedRoster(t)
		_, _ = g.Apply(game.NightAction{Actor: "mafia1", Target: "town1"})
		_, _ = g.Apply(game.NightAction{Actor: "doc", Target: "town1"}) // save
		_, _ = g.Apply(game.AdvancePhase{})                             // -> DayDiscussion
		_, _ = g.Apply(game.AdvancePhase{})                             // -> DayVote
		return g
	}

	t.Run("voter unknown", func(t *testing.T) {
		g := intoDayVote(t)
		_, err := g.Apply(game.DayVote{Voter: "ghost", Target: "mafia1"})
		require.ErrorIs(t, err, game.ErrUnknownPlayer)
	})

	t.Run("target unknown", func(t *testing.T) {
		g := intoDayVote(t)
		_, err := g.Apply(game.DayVote{Voter: "town1", Target: "ghost"})
		require.ErrorIs(t, err, game.ErrUnknownPlayer)
	})

	t.Run("voter dead", func(t *testing.T) {
		// Reach a second day with one player killed.
		g := fixedRoster(t)
		_, _ = g.Apply(game.NightAction{Actor: "mafia1", Target: "town1"}) // unsaved
		_, _ = g.Apply(game.AdvancePhase{})                                // night resolves -> Day
		_, _ = g.Apply(game.AdvancePhase{})                                // -> DayVote
		require.Equal(t, game.PhaseDayVote, g.State().Phase())

		_, err := g.Apply(game.DayVote{Voter: "town1", Target: "mafia1"})
		require.ErrorIs(t, err, game.ErrPlayerDead)
	})

	t.Run("target dead", func(t *testing.T) {
		g := fixedRoster(t)
		_, _ = g.Apply(game.NightAction{Actor: "mafia1", Target: "town1"})
		_, _ = g.Apply(game.AdvancePhase{}) // night resolves; town1 dies; -> Day
		_, _ = g.Apply(game.AdvancePhase{}) // -> DayVote

		_, err := g.Apply(game.DayVote{Voter: "town2", Target: "town1"})
		require.ErrorIs(t, err, game.ErrPlayerDead)
	})

	t.Run("rejected before game is created", func(t *testing.T) {
		g := game.New()
		_, err := g.Apply(game.DayVote{Voter: "a", Target: "b"})
		require.ErrorIs(t, err, game.ErrWrongPhase)
	})
}

func TestDayVote_StateTable(t *testing.T) {
	// Advance a fresh fixedRoster game into PhaseDayVote with all
	// players alive (no kill).
	intoDayVote := func(t *testing.T) *game.Game {
		t.Helper()
		g := fixedRoster(t)
		_, _ = g.Apply(game.NightAction{Actor: "mafia1", Target: "town1"})
		_, _ = g.Apply(game.NightAction{Actor: "doc", Target: "town1"}) // save
		_, _ = g.Apply(game.AdvancePhase{})                             // -> DayDiscussion
		_, _ = g.Apply(game.AdvancePhase{})                             // -> DayVote
		require.Equal(t, game.PhaseDayVote, g.State().Phase())
		return g
	}

	t.Run("first vote emits VoteCast", func(t *testing.T) {
		g := intoDayVote(t)
		evts, err := g.Apply(game.DayVote{Voter: "town1", Target: "mafia1"})
		require.NoError(t, err)
		v, ok := findEvent[game.VoteCast](evts)
		require.True(t, ok)
		require.Equal(t, game.PlayerID("town1"), v.Voter)
		require.Equal(t, game.PlayerID("mafia1"), v.Target)
	})

	t.Run("change emits VoteChanged{From,To}", func(t *testing.T) {
		g := intoDayVote(t)
		_, _ = g.Apply(game.DayVote{Voter: "town1", Target: "mafia1"})
		evts, err := g.Apply(game.DayVote{Voter: "town1", Target: "det"})
		require.NoError(t, err)
		v, ok := findEvent[game.VoteChanged](evts)
		require.True(t, ok)
		require.Equal(t, game.PlayerID("mafia1"), v.From)
		require.Equal(t, game.PlayerID("det"), v.To)
	})

	t.Run("retract emits VoteRetracted", func(t *testing.T) {
		g := intoDayVote(t)
		_, _ = g.Apply(game.DayVote{Voter: "town1", Target: "mafia1"})
		evts, err := g.Apply(game.DayVote{Voter: "town1", Target: ""})
		require.NoError(t, err)
		r, ok := findEvent[game.VoteRetracted](evts)
		require.True(t, ok)
		require.Equal(t, game.PlayerID("mafia1"), r.Was)
	})

	t.Run("identical re-vote rejected ErrNoChange", func(t *testing.T) {
		g := intoDayVote(t)
		_, _ = g.Apply(game.DayVote{Voter: "town1", Target: "mafia1"})
		_, err := g.Apply(game.DayVote{Voter: "town1", Target: "mafia1"})
		require.ErrorIs(t, err, game.ErrNoChange)
	})

	t.Run("retract without prior rejected ErrNoChange", func(t *testing.T) {
		g := intoDayVote(t)
		_, err := g.Apply(game.DayVote{Voter: "town1", Target: ""})
		require.ErrorIs(t, err, game.ErrNoChange)
	})

	t.Run("self-vote rejected", func(t *testing.T) {
		g := intoDayVote(t)
		_, err := g.Apply(game.DayVote{Voter: "town1", Target: "town1"})
		require.ErrorIs(t, err, game.ErrSelfTarget)
	})

	t.Run("vote during discussion rejected", func(t *testing.T) {
		g := fixedRoster(t)
		_, _ = g.Apply(game.NightAction{Actor: "mafia1", Target: "town1"})
		_, _ = g.Apply(game.NightAction{Actor: "doc", Target: "town1"})
		_, _ = g.Apply(game.AdvancePhase{}) // -> DayDiscussion
		_, err := g.Apply(game.DayVote{Voter: "town1", Target: "mafia1"})
		require.ErrorIs(t, err, game.ErrWrongPhase)
	})
}

// --- Vote resolution and extension ---------------------------------------

func TestVoteResolution(t *testing.T) {
	intoDayVote := func(t *testing.T) *game.Game {
		t.Helper()
		g := fixedRoster(t)
		_, _ = g.Apply(game.NightAction{Actor: "mafia1", Target: "town1"})
		_, _ = g.Apply(game.NightAction{Actor: "doc", Target: "town1"}) // save -> nobody dies
		_, _ = g.Apply(game.AdvancePhase{})                             // -> DayDiscussion
		_, _ = g.Apply(game.AdvancePhase{})                             // -> DayVote
		return g
	}

	t.Run("decisive plurality lynches target", func(t *testing.T) {
		g := intoDayVote(t)
		_, _ = g.Apply(game.DayVote{Voter: "town1", Target: "mafia1"})
		_, _ = g.Apply(game.DayVote{Voter: "town2", Target: "mafia1"})
		_, _ = g.Apply(game.DayVote{Voter: "det", Target: "mafia1"})

		evts, err := g.Apply(game.AdvancePhase{})
		require.NoError(t, err)
		l, ok := findEvent[game.PlayerLynched](evts)
		require.True(t, ok)
		require.Equal(t, game.PlayerID("mafia1"), l.PlayerID)

		// PlayerLynched must NOT carry role info (only ID).
		// (Compile-time guarantee from the struct, but assert anyway.)
		require.Equal(t, "public", l.Visibility().Audience)
	})

	t.Run("tie triggers VoteExtended exactly once", func(t *testing.T) {
		g := intoDayVote(t)
		// 1 vs 1 -> tie.
		_, _ = g.Apply(game.DayVote{Voter: "town1", Target: "mafia1"})
		_, _ = g.Apply(game.DayVote{Voter: "town2", Target: "det"})

		evts, err := g.Apply(game.AdvancePhase{})
		require.NoError(t, err)
		_, ext := findEvent[game.VoteExtended](evts)
		require.True(t, ext, "first tie should extend")
		_, lynch := findEvent[game.PlayerLynched](evts)
		require.False(t, lynch, "no lynch on extension")
		require.Equal(t, game.PhaseDayVote, g.State().Phase(), "still in DayVote")
	})

	t.Run("second tie ends day with no lynch", func(t *testing.T) {
		g := intoDayVote(t)
		_, _ = g.Apply(game.DayVote{Voter: "town1", Target: "mafia1"})
		_, _ = g.Apply(game.DayVote{Voter: "town2", Target: "det"})
		_, _ = g.Apply(game.AdvancePhase{}) // extend
		// Re-tie.
		_, _ = g.Apply(game.DayVote{Voter: "town1", Target: "mafia1"})
		_, _ = g.Apply(game.DayVote{Voter: "town2", Target: "det"})

		evts, err := g.Apply(game.AdvancePhase{})
		require.NoError(t, err)
		_, ext := findEvent[game.VoteExtended](evts)
		require.False(t, ext, "no second extension")
		_, lynch := findEvent[game.PlayerLynched](evts)
		require.False(t, lynch, "no lynch when still tied")
		require.Equal(t, game.PhaseNight, g.State().Phase(), "day ends -> Night")
	})

	t.Run("decisive vote after extension lynches", func(t *testing.T) {
		g := intoDayVote(t)
		_, _ = g.Apply(game.DayVote{Voter: "town1", Target: "mafia1"})
		_, _ = g.Apply(game.DayVote{Voter: "town2", Target: "det"})
		_, _ = g.Apply(game.AdvancePhase{}) // extend
		// Town2 swings to mafia1; town1 keeps mafia1; det votes mafia1.
		_, _ = g.Apply(game.DayVote{Voter: "town1", Target: "mafia1"})
		_, _ = g.Apply(game.DayVote{Voter: "town2", Target: "mafia1"})
		_, _ = g.Apply(game.DayVote{Voter: "det", Target: "mafia1"})

		evts, err := g.Apply(game.AdvancePhase{})
		require.NoError(t, err)
		l, ok := findEvent[game.PlayerLynched](evts)
		require.True(t, ok)
		require.Equal(t, game.PlayerID("mafia1"), l.PlayerID)
	})
}

// --- Phase machine guard rails -------------------------------------------

func TestAdvancePhase_Guards(t *testing.T) {
	t.Run("Lobby cannot advance via AdvancePhase", func(t *testing.T) {
		g := game.New()
		_, err := g.Apply(game.AdvancePhase{})
		require.ErrorIs(t, err, game.ErrWrongPhase)
	})

	t.Run("Ended rejects further commands", func(t *testing.T) {
		// Play a tiny game where mafia wins after one night to reach Ended.
		// Mafia kills town1; doctor saves nobody useful; mafia (1) vs town
		// (det, doc, town2) = mafia<town, game continues. We need a roster
		// where mafia immediately wins; construct a 3-player game.
		g := game.New()
		_, err := g.Apply(game.CreateGame{
			GameID: "g1",
			Roles:  []game.Role{game.RoleMafia, game.RoleVillager, game.RoleVillager},
			Seed:   1,
		})
		require.NoError(t, err)
		for _, id := range []game.PlayerID{"a", "b", "c"} {
			_, err := g.Apply(game.AddPlayer{PlayerID: id, Name: string(id)})
			require.NoError(t, err)
		}
		_, err = g.Apply(game.StartGame{})
		require.NoError(t, err)

		// Find the mafia player and a townie.
		var mafia, town game.PlayerID
		for _, p := range g.State().Players() {
			if p.Role() == game.RoleMafia {
				mafia = p.ID()
			} else if town == "" {
				town = p.ID()
			}
		}

		_, _ = g.Apply(game.NightAction{Actor: mafia, Target: town})
		evts, err := g.Apply(game.AdvancePhase{})
		require.NoError(t, err)

		// After killing one villager (mafia=1, town=1), mafia >= town -> mafia wins.
		ge, ok := findEvent[game.GameEnded](evts)
		require.True(t, ok, "GameEnded must fire")
		require.Equal(t, game.FactionMafia, ge.Winner)
		require.Equal(t, game.PhaseEnded, g.State().Phase())
		require.Len(t, ge.FinalRoles, 3, "FinalRoles reveals every role")

		// Further AdvancePhase is rejected.
		_, err = g.Apply(game.AdvancePhase{})
		require.ErrorIs(t, err, game.ErrGameEnded)
	})
}

// --- Win conditions -------------------------------------------------------

func TestWinConditions(t *testing.T) {
	t.Run("town wins when last mafia is lynched", func(t *testing.T) {
		g := fixedRoster(t)
		_, _ = g.Apply(game.NightAction{Actor: "mafia1", Target: "town1"})
		_, _ = g.Apply(game.NightAction{Actor: "doc", Target: "town1"}) // save
		_, _ = g.Apply(game.AdvancePhase{})                             // -> DayDiscussion
		_, _ = g.Apply(game.AdvancePhase{})                             // -> DayVote

		// Everyone votes the mafia.
		_, _ = g.Apply(game.DayVote{Voter: "town1", Target: "mafia1"})
		_, _ = g.Apply(game.DayVote{Voter: "town2", Target: "mafia1"})
		_, _ = g.Apply(game.DayVote{Voter: "det", Target: "mafia1"})
		_, _ = g.Apply(game.DayVote{Voter: "doc", Target: "mafia1"})

		evts, err := g.Apply(game.AdvancePhase{})
		require.NoError(t, err)
		ge, ok := findEvent[game.GameEnded](evts)
		require.True(t, ok)
		require.Equal(t, game.FactionTown, ge.Winner)
		require.Equal(t, game.PhaseEnded, g.State().Phase())
	})

	t.Run("FinalRoles is only present at game end", func(t *testing.T) {
		g := fixedRoster(t)
		_, _ = g.Apply(game.NightAction{Actor: "mafia1", Target: "town1"})
		evts, err := g.Apply(game.AdvancePhase{})
		require.NoError(t, err)
		_, ended := findEvent[game.GameEnded](evts)
		require.False(t, ended, "with mafia=1 town=3 alive, no win yet")
	})
}
