package game_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/96malhar/mafia-the-game/internal/game"
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
		game.MafiaRosterRevealed{Members: []game.PlayerID{"mafia1"}},
		game.DetectiveResult{
			Detective: "det", Target: "mafia1", IsMafia: true,
		},
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

	t.Run("alive mafia sees the mafia roster; town never does", func(t *testing.T) {
		// The roster reveal is the whole point of the feature: mafia learn
		// their teammates, town must not. A leak here would hand the town
		// a guaranteed mafia ID, so this is a security-critical assertion.
		mafiaView := game.Project("mafia1", f.events, f.g.State())
		var mafiaSaw bool
		for _, e := range mafiaView {
			if _, ok := e.(game.MafiaRosterRevealed); ok {
				mafiaSaw = true
			}
		}
		require.True(t, mafiaSaw, "alive mafia must see the mafia roster")

		for _, viewer := range []game.PlayerID{"det", "doc", "town1", "town2", "stranger"} {
			out := game.Project(viewer, f.events, f.g.State())
			for _, e := range out {
				_, leaked := e.(game.MafiaRosterRevealed)
				require.False(t, leaked, "viewer %q must not see the mafia roster", viewer)
			}
		}
	})

	t.Run("dead mafia loses faction visibility", func(t *testing.T) {
		// Construct a state where the mafia is dead. Build a fresh game
		// where mafia gets lynched, then project the old fixture events
		// against the new (dead-mafia) state.
		//
		// Night order is now Mafia → Det → Doctor. We run a complete
		// night (saved kill) so we reach DayDiscussion, then transition
		// to DayVote, then unanimously vote out the mafia.
		g := fixedRoster(t)
		// Use playNight to walk through the full Night state machine
		// (mafia → det timeout → doc save → resolve) without having
		// to spell out each sub-phase transition explicitly.
		playNight(t, g, map[game.Role]game.PlayerID{
			game.RoleMafia:  "town1",
			game.RoleDoctor: "town1", // save -> resolves night
		})
		_, _ = g.Apply(game.OpenVoting{}) // host: open voting
		_, _ = g.Apply(game.DayVote{Voter: "town1", Target: "mafia1"})
		_, _ = g.Apply(game.DayVote{Voter: "town2", Target: "mafia1"})
		_, _ = g.Apply(game.DayVote{Voter: "det", Target: "mafia1"})
		_, _ = g.Apply(game.DayVote{Voter: "doc", Target: "mafia1"})
		_, _ = g.Apply(game.FinalizeVotes{}) // -> game ends (town wins)

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

func TestProjection_SoloTownActionStaysPrivate(t *testing.T) {
	g := fixedRoster(t)

	// A solo town role (doctor) records a night action. Town is a single
	// shared faction, so a faction-scoped ack would leak the target to
	// every townsperson. It must be private to the actor.
	events := []game.Event{
		game.NightActionRecorded{
			Actor: "doc", Target: "town1", Faction: game.FactionTown,
		},
	}

	t.Run("actor sees own ack", func(t *testing.T) {
		out := game.Project("doc", events, g.State())
		require.Len(t, out, 1, "doctor must see their own NightActionRecorded")
	})

	t.Run("no other town member sees it", func(t *testing.T) {
		for _, viewer := range []game.PlayerID{"det", "town1", "town2", "mafia1"} {
			out := game.Project(viewer, events, g.State())
			require.Empty(t, out, "viewer %q must not see another town role's NightActionRecorded", viewer)
		}
	})
}

func TestProjection_VoteSecrecyAndReveal(t *testing.T) {
	g := fixedRoster(t)

	events := []game.Event{
		game.VoteCast{Voter: "town1", Target: "mafia1"},
		game.VoteCast{Voter: "det", Target: "mafia1"},
		game.VotesRevealed{Day: 1, Tally: map[game.PlayerID]game.PlayerID{
			"town1": "mafia1", "det": "mafia1",
		}},
	}

	t.Run("a voter sees only their own pre-reveal vote", func(t *testing.T) {
		out := game.Project("town1", events, g.State())
		var ownCast, othersCast int
		for _, e := range out {
			if vc, ok := e.(game.VoteCast); ok {
				if vc.Voter == "town1" {
					ownCast++
				} else {
					othersCast++
				}
			}
		}
		require.Equal(t, 1, ownCast, "town1 sees their own VoteCast")
		require.Zero(t, othersCast, "town1 must NOT see det's VoteCast before reveal")
	})

	t.Run("a non-voter sees nobody's pre-reveal vote", func(t *testing.T) {
		out := game.Project("town2", events, g.State())
		for _, e := range out {
			_, leaked := e.(game.VoteCast)
			require.False(t, leaked, "town2 (hasn't voted) must see no VoteCast pre-reveal")
		}
	})

	t.Run("everyone — including the dead — sees the revealed tally", func(t *testing.T) {
		// Build a state where town2 is dead, to prove the reveal reaches
		// spectating/dead players too (requirement: reveal is public).
		dg := fixedRoster(t)
		playNight(t, dg, map[game.Role]game.PlayerID{game.RoleMafia: "town2"})
		require.Equal(t, game.PhaseDayDiscussion, dg.State().Phase())

		for _, viewer := range []game.PlayerID{"town1", "town2", "det", "doc", "mafia1", "stranger"} {
			out := game.Project(viewer, events, dg.State())
			var found bool
			for _, e := range out {
				if rv, ok := e.(game.VotesRevealed); ok {
					found = true
					require.Len(t, rv.Tally, 2, "the full tally rides on the reveal event")
				}
			}
			require.True(t, found, "viewer %q must see the revealed tally", viewer)
		}
	})
}

func TestProjection_ConsortMafiaMutualIgnorance(t *testing.T) {
	// The whole point of the consort's separate faction: she is mafia-
	// aligned for winning but must NOT see mafia coordination, and the
	// mafia must not learn she exists. The mafia roster reveal is the
	// canonical mafia-only event — it must reach the mafia and never the
	// consort.
	g := fixedRosterWithConsort(t)
	events := []game.Event{
		game.MafiaRosterRevealed{Members: []game.PlayerID{"mafia1"}},
		game.NightActionRecorded{
			Actor: "mafia1", Target: "town1", Faction: game.FactionMafia,
		},
	}

	t.Run("alive mafia sees the roster and the kill ack", func(t *testing.T) {
		out := game.Project("mafia1", events, g.State())
		var sawRoster, sawAck bool
		for _, e := range out {
			switch e.(type) {
			case game.MafiaRosterRevealed:
				sawRoster = true
			case game.NightActionRecorded:
				sawAck = true
			}
		}
		require.True(t, sawRoster, "mafia must see the roster")
		require.True(t, sawAck, "mafia must see the faction kill ack")
	})

	t.Run("consort sees NEITHER the roster nor the kill ack", func(t *testing.T) {
		out := game.Project("consort", events, g.State())
		for _, e := range out {
			switch e.(type) {
			case game.MafiaRosterRevealed:
				t.Fatal("consort must NOT see the mafia roster")
			case game.NightActionRecorded:
				t.Fatal("consort must NOT see mafia-only coordination")
			}
		}
	})
}

func TestProjection_BlockedNoticeIsPrivateToTarget(t *testing.T) {
	// The Blocked notice is private to the blocked player; the room must
	// never learn who the consort targeted.
	g := fixedRosterWithConsort(t)
	events := []game.Event{game.Blocked{PlayerID: "doc"}}

	t.Run("the blocked player sees their own notice", func(t *testing.T) {
		out := game.Project("doc", events, g.State())
		require.Len(t, out, 1, "the doctor must see their own Blocked notice")
	})

	t.Run("nobody else sees it — not even the consort", func(t *testing.T) {
		for _, viewer := range []game.PlayerID{"mafia1", "consort", "det", "town1", "town2", "stranger"} {
			out := game.Project(viewer, events, g.State())
			require.Empty(t, out, "viewer %q must not see the doctor's Blocked notice", viewer)
		}
	})
}

func TestProjection_ConsortPromotedIsPrivateToPromotee(t *testing.T) {
	// Promotion is a secret takeover: only the promoted consort learns
	// it. The accompanying roster reveal is mafia-only and now lists her.
	g := fixedRosterWithConsort(t)
	events := []game.Event{game.ConsortPromoted{PlayerID: "consort"}}

	t.Run("the promoted player sees the promotion", func(t *testing.T) {
		out := game.Project("consort", events, g.State())
		require.Len(t, out, 1, "the consort must see her own promotion")
	})

	t.Run("nobody else sees the promotion", func(t *testing.T) {
		for _, viewer := range []game.PlayerID{"mafia1", "det", "doc", "town1", "town2", "stranger"} {
			out := game.Project(viewer, events, g.State())
			require.Empty(t, out, "viewer %q must not learn a sleeper took over", viewer)
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
