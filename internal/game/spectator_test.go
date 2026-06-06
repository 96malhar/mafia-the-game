package game_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/96malhar/mafia-the-game/internal/game"
)

// TestSpectator_NightActionMirrorsToGraveyard asserts that every submitted
// night action emits a Graveyard-scoped SpectatorNightAction carrying both
// participants' roles, so dead spectators can render the night feed as
// "Actor (role) targeted Target (role)".
func TestSpectator_NightActionMirrorsToGraveyard(t *testing.T) {
	// fixedRoster leaves the game on the mafia act window; town1/town2 are
	// villagers (see fixedRoster).
	g := fixedRoster(t)
	evts := nightAction(t, g, "mafia1", "town1")

	sa, ok := findEvent[game.SpectatorNightAction](evts)
	require.True(t, ok, "a submitted night action mirrors to the graveyard")
	require.Equal(t, game.PlayerID("mafia1"), sa.Actor)
	require.Equal(t, game.RoleMafia, sa.ActorRole)
	require.Equal(t, game.PlayerID("town1"), sa.Target)
	require.Equal(t, game.RoleVillager, sa.TargetRole)
	require.Equal(t, game.Graveyard().Audience, sa.Visibility().Audience,
		"the spectator feed is graveyard-only — never seen by the living")
}

// TestSpectator_NightActionReachesOnlyTheDead asserts the spectator feed is
// projected to dead players and to no living (or unknown) viewer — a living
// leak would hand the table cross-role night targeting.
func TestSpectator_NightActionReachesOnlyTheDead(t *testing.T) {
	g := fixedRoster(t)
	playNight(t, g, map[game.Role]game.PlayerID{game.RoleMafia: "town2"})
	require.False(t, livingByID(g, "town2"), "town2 should be dead after the night")

	events := []game.Event{
		game.SpectatorNightAction{
			Actor: "mafia1", ActorRole: game.RoleMafia,
			Target: "town1", TargetRole: game.RoleVillager,
		},
	}

	t.Run("a dead player sees the night action", func(t *testing.T) {
		out := game.Project("town2", events, g.State())
		require.Len(t, out, 1, "town2 (dead) must receive the spectator feed")
	})

	t.Run("the living and unknown viewers see nothing", func(t *testing.T) {
		assertNobodySees(t, g.State(), events,
			[]game.PlayerID{"mafia1", "det", "doc", "town1", "stranger"},
			"a spectator night action")
	})
}

// TestSpectator_RecruitShownAsRecruitedToTheDead asserts a Yakuza recruit
// reaches the graveyard as a recruit-flavored SpectatorNightAction
// (Recruit=true), so the dead feed renders "Yakuza recruited X" rather than a
// kill. Real flow: a prior night kill leaves a dead spectator, then the Yakuza
// recruits during the next Mafia turn. The living must NOT see the feed.
func TestSpectator_RecruitShownAsRecruitedToTheDead(t *testing.T) {
	g := fixedRosterWithYakuza(t)
	// Night 1: the mafia kills town2, creating a dead spectator.
	playNight(t, g, map[game.Role]game.PlayerID{game.RoleMafia: "town2"})
	require.False(t, livingByID(g, "town2"), "town2 is dead and now spectating")
	noLynchDay(t, g)

	// Night 2: the Yakuza recruits town1 during the Mafia turn.
	_, err := g.Apply(game.BeginNight{})
	require.NoError(t, err)
	advanceToMafiaAct(t, g)
	evts := recruit(t, g, "yak", "town1")

	// The recruit mirrors to the graveyard, flagged as a recruit (not a kill),
	// carrying both roles so the dead feed can render "Yakuza recruited X".
	sa, ok := findEvent[game.SpectatorNightAction](evts)
	require.True(t, ok, "the recruit mirrors to the graveyard")
	require.True(t, sa.Recruit, "flagged Recruit so the feed renders the 'recruited' verb")
	require.Equal(t, game.RoleYakuza, sa.ActorRole)
	require.Equal(t, game.PlayerID("town1"), sa.Target)
	require.Equal(t, game.Graveyard().Audience, sa.Visibility().Audience,
		"the recruit feed is graveyard-only — never seen by the living")

	t.Run("the dead spectator sees the recruit, flagged 'recruited'", func(t *testing.T) {
		out := game.Project("town2", evts, g.State())
		feed := findAllEvents[game.SpectatorNightAction](out)
		require.Len(t, feed, 1, "town2 (dead) receives the recruit in their feed")
		require.True(t, feed[0].Recruit, "and it's flagged so the verb reads 'recruited'")
	})

	t.Run("no living viewer sees the spectator feed", func(t *testing.T) {
		// mafia1 (living mafia) sees the faction-only RecruitRecorded, but never
		// the graveyard feed; town1/det/stranger see neither.
		for _, viewer := range []game.PlayerID{"mafia1", "det", "town1", "stranger"} {
			out := game.Project(viewer, evts, g.State())
			require.Empty(t, findAllEvents[game.SpectatorNightAction](out),
				"living viewer %q must not see the spectator feed", viewer)
		}
	})
}

// TestSpectator_PrivateRoleResultsNotLeakedToTheDead guards the boundary the
// spectator feed must NOT cross: a dead spectator may watch WHO acted on whom
// (SpectatorNightAction), but must never receive the private OUTCOMES those
// roles learn — the detective's investigation result above all, plus the
// mafia's private/faction kill ack.
//
// Unlike a projection-only check, this drives a REAL night where the
// detective actually investigates, then projects the genuinely
// engine-emitted batch — so it also exercises that applyNightAction emits
// the DetectiveResult with the right (PrivateTo) visibility, not just that
// the filter would hide a hand-built one.
func TestSpectator_PrivateRoleResultsNotLeakedToTheDead(t *testing.T) {
	g := fixedRoster(t)
	// Real night: the mafia kills town2 and the detective investigates the
	// mafioso. The emitted batch carries the genuine DetectiveResult
	// (PrivateTo det), the faction kill ack (NightActionRecorded), and a
	// SpectatorNightAction per actor (graveyard).
	evts := playNight(t, g, map[game.Role]game.PlayerID{
		game.RoleMafia:     "town2",
		game.RoleDetective: "mafia1",
	})
	require.False(t, livingByID(g, "town2"), "town2 should be dead after the night")

	// Sanity: the detective genuinely investigated, and that produced a real
	// result + spectator action in the batch (otherwise the test below would
	// vacuously pass).
	detResult, ok := findEvent[game.DetectiveResult](evts)
	require.True(t, ok, "the detective actually investigated someone this night")
	require.Equal(t, game.PlayerID("mafia1"), detResult.Target)
	require.True(t, detResult.IsMafia, "the investigated mafioso reads as mafia")
	require.NotEmpty(t, findAllEvents[game.SpectatorNightAction](evts),
		"the night produced spectator actions to feed the graveyard")

	t.Run("a dead spectator sees the action feed but not the private result", func(t *testing.T) {
		out := game.Project("town2", evts, g.State())
		require.NotEmpty(t, findAllEvents[game.SpectatorNightAction](out),
			"the dead spectate who acted on whom")
		require.Empty(t, findAllEvents[game.DetectiveResult](out),
			"the dead must NOT learn the detective's investigation result")
		require.Empty(t, findAllEvents[game.NightActionRecorded](out),
			"the dead must NOT receive the mafia's private faction kill ack")
	})

	t.Run("the detective still receives their own result", func(t *testing.T) {
		// PrivateTo is aliveness-agnostic: the owner sees it whether alive or
		// dead — what's withheld is only NON-owner dead spectators.
		out := game.Project("det", evts, g.State())
		require.NotEmpty(t, findAllEvents[game.DetectiveResult](out),
			"the detective sees their own investigation result")
	})
}

// TestSpectator_TrackerResultNotLeakedToTheDead is the tracker analogue of
// the detective check: a dead spectator may watch the tracker act on whom
// (SpectatorNightAction) but must never receive the private TrackerResult.
// Real-flow, so it also exercises that applyNightAction emits TrackerResult
// with PrivateTo visibility.
func TestSpectator_TrackerResultNotLeakedToTheDead(t *testing.T) {
	g := fixedRosterWithTracker(t)
	// Real night: the mafia kills town1 (creating a dead spectator) and the
	// tracker tracks the mafioso, learning the faction's kill target.
	evts := runTrackerNightToDay(t, g, map[game.Role]game.PlayerID{
		game.RoleMafia:   "town1",
		game.RoleTracker: "mafia1",
	})
	require.False(t, livingByID(g, "town1"), "town1 should be dead after the night")

	// Sanity (non-vacuous): the tracker genuinely tracked, producing a real
	// result + spectator feed in the batch.
	trkResult, ok := findEvent[game.TrackerResult](evts)
	require.True(t, ok, "the tracker actually tracked someone this night")
	require.Equal(t, game.PlayerID("town1"), trkResult.Visited,
		"tracking the mafioso reveals the faction kill target")
	require.NotEmpty(t, findAllEvents[game.SpectatorNightAction](evts),
		"the night produced spectator actions to feed the graveyard")

	t.Run("a dead spectator sees the action feed but not the private result", func(t *testing.T) {
		out := game.Project("town1", evts, g.State())
		require.NotEmpty(t, findAllEvents[game.SpectatorNightAction](out),
			"the dead spectate who the tracker watched")
		require.Empty(t, findAllEvents[game.TrackerResult](out),
			"the dead must NOT learn the tracker's result")
	})

	t.Run("the tracker still receives their own result", func(t *testing.T) {
		out := game.Project("trk", evts, g.State())
		require.NotEmpty(t, findAllEvents[game.TrackerResult](out),
			"the tracker sees their own result")
	})
}

// TestSpectator_ConsortBlockNotLeakedToTheDead is the consort-block analogue:
// it drives a REAL night where the consort distracts the doctor while the
// mafia kills a villager. A dead spectator watches the consort's distract via
// the feed (SpectatorNightAction), but must NOT receive the private Blocked
// notice that only the distracted doctor learns. Real-flow, so it also
// exercises that the engine emits Blocked with PrivateTo visibility.
func TestSpectator_ConsortBlockNotLeakedToTheDead(t *testing.T) {
	g := fixedRosterWithConsort(t)
	// The consort distracts the doctor (a real roleblock → Blocked{doc}); the
	// mafia kills town2. With the doctor blocked, no save lands and town2
	// dies, giving us a dead spectator.
	evts := runConsortNightToDay(t, g, map[game.Role]game.PlayerID{
		game.RoleMafia:   "town2",
		game.RoleConsort: "doc",
	})
	require.False(t, livingByID(g, "town2"), "town2 should be dead after the night")

	// Sanity (non-vacuous): the consort genuinely distracted the doctor,
	// producing both a private Blocked notice and a spectator action.
	blocked, ok := findEvent[game.Blocked](evts)
	require.True(t, ok, "the consort actually distracted the doctor this night")
	require.Equal(t, game.PlayerID("doc"), blocked.PlayerID)
	sawConsortInBatch := false
	for _, sa := range findAllEvents[game.SpectatorNightAction](evts) {
		if sa.ActorRole == game.RoleConsort && sa.Target == "doc" {
			sawConsortInBatch = true
		}
	}
	require.True(t, sawConsortInBatch, "the consort's distract reached the spectator feed")

	t.Run("a dead spectator sees the consort's distract but not the Blocked notice", func(t *testing.T) {
		out := game.Project("town2", evts, g.State())
		sawConsortDistract := false
		for _, sa := range findAllEvents[game.SpectatorNightAction](out) {
			if sa.ActorRole == game.RoleConsort && sa.Target == "doc" {
				sawConsortDistract = true
			}
		}
		require.True(t, sawConsortDistract, "the dead see the consort distract the doctor")
		require.Empty(t, findAllEvents[game.Blocked](out),
			"the dead must NOT receive the doctor's private roleblock notice")
	})

	t.Run("the distracted doctor still receives their own notice", func(t *testing.T) {
		out := game.Project("doc", evts, g.State())
		require.NotEmpty(t, findAllEvents[game.Blocked](out),
			"the doctor sees their own Blocked notice")
	})
}
