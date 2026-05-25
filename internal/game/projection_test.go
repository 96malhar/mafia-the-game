package game_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/malhar/mafia-the-game/internal/game"
)

// projectionFixture builds a started game in a known state, then returns
// a list of events with mixed visibility for projection tests to filter.
// Roles after fixedRoster(t): mafia1, det, doc, town1, town2.
type projectionFixture struct {
	g      *game.Game
	events []game.Event
}

func newProjectionFixture(t *testing.T) projectionFixture {
	t.Helper()
	g := fixedRoster(t)

	events := []game.Event{
		game.PlayerJoined{PlayerID: "town1", Name: "town1"}, // public
		game.RoleAssigned{PlayerID: "mafia1", Role: game.RoleMafia},
		game.RoleAssigned{PlayerID: "det", Role: game.RoleDetective},
		game.NightActionRecorded{
			Actor: "mafia1", Target: "town1", Faction: game.FactionMafia,
		},
		game.DetectiveResult{
			Detective: "det", Target: "mafia1", IsMafia: true,
		},
		game.PlayerSaved{PlayerID: "town1", Doctor: "doc"},
		game.PlayerKilled{PlayerID: "town2"}, // public
	}
	return projectionFixture{g: g, events: events}
}

func TestProjection_PublicEventsAlwaysVisible(t *testing.T) {
	f := newProjectionFixture(t)
	// PlayerJoined and PlayerKilled are the public ones in our fixture.
	wantPublic := []game.Event{
		game.PlayerJoined{PlayerID: "town1", Name: "town1"},
		game.PlayerKilled{PlayerID: "town2"},
	}

	cases := []struct {
		name   string
		viewer game.PlayerID
	}{
		{"alive mafia", "mafia1"},
		{"alive detective", "det"},
		{"alive doctor", "doc"},
		{"alive villager", "town1"},
		{"unknown viewer", "stranger"},
		{"empty viewer", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := game.Project(tc.viewer, f.events, f.g.State())
			for _, w := range wantPublic {
				require.Contains(t, got, w, "viewer %q should see public event %T", tc.viewer, w)
			}
		})
	}
}

func TestProjection_PrivateEventsOnlyForOwner(t *testing.T) {
	f := newProjectionFixture(t)

	t.Run("detective sees own DetectiveResult", func(t *testing.T) {
		out := game.Project("det", f.events, f.g.State())
		var found bool
		for _, e := range out {
			if _, ok := e.(game.DetectiveResult); ok {
				found = true
			}
		}
		require.True(t, found, "detective must see DetectiveResult")
	})

	t.Run("non-detective does NOT see DetectiveResult", func(t *testing.T) {
		for _, viewer := range []game.PlayerID{"mafia1", "doc", "town1", "town2"} {
			out := game.Project(viewer, f.events, f.g.State())
			for _, e := range out {
				_, leaked := e.(game.DetectiveResult)
				require.False(t, leaked, "viewer %q must not see DetectiveResult", viewer)
			}
		}
	})

	t.Run("doctor sees own PlayerSaved; nobody else does", func(t *testing.T) {
		out := game.Project("doc", f.events, f.g.State())
		var found bool
		for _, e := range out {
			if _, ok := e.(game.PlayerSaved); ok {
				found = true
			}
		}
		require.True(t, found, "doctor must see PlayerSaved")

		for _, viewer := range []game.PlayerID{"mafia1", "det", "town1", "town2"} {
			out := game.Project(viewer, f.events, f.g.State())
			for _, e := range out {
				_, leaked := e.(game.PlayerSaved)
				require.False(t, leaked, "viewer %q must not see PlayerSaved", viewer)
			}
		}
	})

	t.Run("each RoleAssigned is private to its subject", func(t *testing.T) {
		// mafia1 should see only their own RoleAssigned, never det's.
		mafiaView := game.Project("mafia1", f.events, f.g.State())
		var sawOwnRole, sawOthersRole bool
		for _, e := range mafiaView {
			ra, ok := e.(game.RoleAssigned)
			if !ok {
				continue
			}
			if ra.PlayerID == "mafia1" {
				sawOwnRole = true
			} else {
				sawOthersRole = true
			}
		}
		require.True(t, sawOwnRole, "mafia1 should see their own RoleAssigned")
		require.False(t, sawOthersRole, "mafia1 must not see other players' RoleAssigned")
	})
}

func TestProjection_FactionEventsRequireAliveMembership(t *testing.T) {
	f := newProjectionFixture(t)

	t.Run("alive mafia member sees mafia-only event", func(t *testing.T) {
		out := game.Project("mafia1", f.events, f.g.State())
		var found bool
		for _, e := range out {
			if _, ok := e.(game.NightActionRecorded); ok {
				found = true
			}
		}
		require.True(t, found)
	})

	t.Run("non-mafia does NOT see mafia-only event", func(t *testing.T) {
		for _, viewer := range []game.PlayerID{"det", "doc", "town1", "town2"} {
			out := game.Project(viewer, f.events, f.g.State())
			for _, e := range out {
				_, leaked := e.(game.NightActionRecorded)
				require.False(t, leaked, "viewer %q must not see NightActionRecorded", viewer)
			}
		}
	})

	t.Run("dead mafia loses faction visibility", func(t *testing.T) {
		// Construct a state where the mafia is dead. Build a fresh game
		// where mafia gets lynched, then project the old fixture events
		// against the new (dead-mafia) state.
		g := fixedRoster(t)
		_, _ = g.Apply(game.NightAction{Actor: "mafia1", Target: "town1"})
		_, _ = g.Apply(game.NightAction{Actor: "doc", Target: "town1"}) // save
		_, _ = g.Apply(game.AdvancePhase{})                             // -> DayDiscussion
		_, _ = g.Apply(game.AdvancePhase{})                             // -> DayVote
		_, _ = g.Apply(game.DayVote{Voter: "town1", Target: "mafia1"})
		_, _ = g.Apply(game.DayVote{Voter: "town2", Target: "mafia1"})
		_, _ = g.Apply(game.DayVote{Voter: "det", Target: "mafia1"})
		_, _ = g.Apply(game.DayVote{Voter: "doc", Target: "mafia1"})
		_, _ = g.Apply(game.AdvancePhase{}) // -> game ends (town wins)

		// Sanity check: mafia1 is dead in the new state.
		var mafiaAlive bool
		for _, p := range g.State().Players() {
			if p.ID() == "mafia1" {
				mafiaAlive = p.Alive()
			}
		}
		require.False(t, mafiaAlive, "test precondition: mafia1 should be dead")

		out := game.Project("mafia1", f.events, g.State())
		for _, e := range out {
			_, leaked := e.(game.NightActionRecorded)
			require.False(t, leaked, "dead mafia must not see faction-only events anymore")
		}
	})
}

func TestProjection_UnknownViewerSeesOnlyPublic(t *testing.T) {
	f := newProjectionFixture(t)
	out := game.Project("stranger", f.events, f.g.State())
	for _, e := range out {
		// Anything visible to "stranger" must be public.
		require.Equal(t, "public", e.Visibility().Audience,
			"unknown viewer leaked a non-public event of type %T", e)
	}
}

// Note: the default-deny branch for unknown Visibility.Audience tags
// cannot be exercised from the external game_test package because the
// Event interface is closed. It is tested in projection_internal_test.go.
