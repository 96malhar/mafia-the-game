package game_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/96malhar/mafia-the-game/internal/game"
)

// Tracker-specific behaviour: each night the Tracker picks one living
// player and immediately, privately learns WHO that player visited that
// night (the target of their night action) — never WHAT the action was. It
// wakes LAST, after the doctor. Tracking a mafia-faction member reveals the
// faction's collective target (kill, or recruit target on a recruit night).
// A target that took no action reads as "stayed home" (empty visit). The
// Tracker cannot track itself or a dead player, and a Consort block leaves
// its turn phantom so it learns nothing.

// --- tracker fixtures -----------------------------------------------------

// fixedRosterWithTracker builds a deterministic 6-player game with the
// optional Tracker enabled, mapping each player ID to a fixed role:
//
//	mafia1 -> RoleMafia
//	det    -> RoleDetective
//	doc    -> RoleDoctor
//	trk    -> RoleTracker
//	town1  -> RoleVillager
//	town2  -> RoleVillager
//
// The Tracker consumes one villager slot. On return the game sits on the
// MAFIA's act window. The night turn order with a tracker present is
// Mafia -> Detective -> Doctor -> Tracker (the tracker wakes last).
func fixedRosterWithTracker(t *testing.T) *game.Game {
	t.Helper()
	return fixedRosterMatching(t, rosterDeal{
		ids: []game.PlayerID{"mafia1", "det", "doc", "trk", "town1", "town2"},
		wanted: map[game.PlayerID]game.Role{
			"mafia1": game.RoleMafia,
			"det":    game.RoleDetective,
			"doc":    game.RoleDoctor,
			"trk":    game.RoleTracker,
			"town1":  game.RoleVillager,
			"town2":  game.RoleVillager,
		},
		mafiaCount: 1,
		tracker:    true,
		maxSeeds:   8000,
	})
}

// fixedRoster2MafiaWithTracker builds a deterministic 6-player, 2-mafia game
// with the Tracker enabled:
//
//	mafia1 -> RoleMafia
//	mafia2 -> RoleMafia
//	det    -> RoleDetective
//	doc    -> RoleDoctor
//	trk    -> RoleTracker
//	town1  -> RoleVillager
//
// Used to assert the faction-collective read: tracking the mafioso who did
// NOT press the kill still reveals the faction target. On return the game
// sits on the MAFIA's act window.
func fixedRoster2MafiaWithTracker(t *testing.T) *game.Game {
	t.Helper()
	return fixedRosterMatching(t, rosterDeal{
		ids: []game.PlayerID{"mafia1", "mafia2", "det", "doc", "trk", "town1"},
		wanted: map[game.PlayerID]game.Role{
			"mafia1": game.RoleMafia,
			"mafia2": game.RoleMafia,
			"det":    game.RoleDetective,
			"doc":    game.RoleDoctor,
			"trk":    game.RoleTracker,
			"town1":  game.RoleVillager,
		},
		mafiaCount: 2,
		tracker:    true,
		maxSeeds:   8000,
	})
}

// fixedRosterWithYakuzaAndTracker builds a deterministic 7-player game with
// BOTH the Yakuza and the Tracker enabled:
//
//	mafia1 -> RoleMafia
//	yak    -> RoleYakuza
//	det    -> RoleDetective
//	doc    -> RoleDoctor
//	trk    -> RoleTracker
//	town1  -> RoleVillager
//	town2  -> RoleVillager
//
// Night order: Mafia -> Detective -> Doctor -> Tracker (the Yakuza acts
// within the Mafia turn). On return the game sits on the MAFIA's act window.
func fixedRosterWithYakuzaAndTracker(t *testing.T) *game.Game {
	t.Helper()
	return fixedRosterMatching(t, rosterDeal{
		ids: []game.PlayerID{"mafia1", "yak", "det", "doc", "trk", "town1", "town2"},
		wanted: map[game.PlayerID]game.Role{
			"mafia1": game.RoleMafia,
			"yak":    game.RoleYakuza,
			"det":    game.RoleDetective,
			"doc":    game.RoleDoctor,
			"trk":    game.RoleTracker,
			"town1":  game.RoleVillager,
			"town2":  game.RoleVillager,
		},
		mafiaCount: 1,
		yakuza:     true,
		tracker:    true,
		maxSeeds:   60000,
	})
}

// fixedRosterWithConsortAndTracker builds a deterministic 7-player game with
// BOTH the Consort and the Tracker enabled:
//
//	mafia1 -> RoleMafia
//	cons   -> RoleConsort
//	det    -> RoleDetective
//	doc    -> RoleDoctor
//	trk    -> RoleTracker
//	town1  -> RoleVillager
//	town2  -> RoleVillager
//
// Night order: Mafia -> Consort -> Detective -> Doctor -> Tracker. Used to
// assert the Consort can roleblock the Tracker. On return the game sits on
// the MAFIA's act window.
func fixedRosterWithConsortAndTracker(t *testing.T) *game.Game {
	t.Helper()
	return fixedRosterMatching(t, rosterDeal{
		ids: []game.PlayerID{"mafia1", "cons", "det", "doc", "trk", "town1", "town2"},
		wanted: map[game.PlayerID]game.Role{
			"mafia1": game.RoleMafia,
			"cons":   game.RoleConsort,
			"det":    game.RoleDetective,
			"doc":    game.RoleDoctor,
			"trk":    game.RoleTracker,
			"town1":  game.RoleVillager,
			"town2":  game.RoleVillager,
		},
		mafiaCount: 1,
		consort:    true,
		tracker:    true,
		maxSeeds:   60000,
	})
}

// runTrackerNightToDay walks the night with a tracker in the queue
// (Mafia -> Detective -> Doctor -> Tracker). See runNightToDay.
func runTrackerNightToDay(t *testing.T, g *game.Game, actions map[game.Role]game.PlayerID) []game.Event {
	t.Helper()
	return runNightToDay(t, g,
		[]game.Role{game.RoleMafia, game.RoleDetective, game.RoleDoctor, game.RoleTracker},
		actions)
}

// --- roster + toggle ------------------------------------------------------

func TestTracker_FactionIsTown(t *testing.T) {
	require.Equal(t, game.FactionTown, game.RoleTracker.Faction(),
		"the Tracker is town-aligned")
}

func TestTracker_SetTrackerToggle(t *testing.T) {
	g := game.New()
	_, err := g.Apply(game.CreateGame{
		GameID: "g1", MinPlayers: 6, MaxPlayers: 20, MafiaCount: 1, Seed: 0,
	})
	require.NoError(t, err)

	require.False(t, g.State().TrackerEnabled(), "tracker defaults off")

	evts, err := g.Apply(game.SetTracker{Enabled: true})
	require.NoError(t, err)
	tc, ok := findEvent[game.TrackerChanged](evts)
	require.True(t, ok, "toggling on emits TrackerChanged")
	require.True(t, tc.Enabled)
	require.True(t, g.State().TrackerEnabled())

	// Re-enabling is a no-op.
	_, err = g.Apply(game.SetTracker{Enabled: true})
	require.ErrorIs(t, err, game.ErrNoChange)

	_, err = g.Apply(game.SetTracker{Enabled: false})
	require.NoError(t, err)
	require.False(t, g.State().TrackerEnabled())
}

// TestTracker_DealtOne: enabling the tracker deals exactly one RoleTracker,
// taking a villager slot.
func TestTracker_DealtOne(t *testing.T) {
	g := fixedRosterWithTracker(t)
	counts := map[game.Role]int{}
	for _, p := range g.State().Players() {
		counts[p.Role()]++
	}
	require.Equal(t, 1, counts[game.RoleTracker], "exactly one tracker dealt")
	require.Equal(t, game.RoleTracker, roleByID(g, "trk"))
}

// --- turn order -----------------------------------------------------------

// TestTracker_WakesLastAfterDoctor: the tracker's turn comes after the
// doctor's, which is the whole reason it can read every other visit.
func TestTracker_WakesLastAfterDoctor(t *testing.T) {
	g := fixedRosterWithTracker(t)

	// Mafia (idle) -> Detective.
	walkRestOfTurn(t, g)
	require.Equal(t, game.RoleDetective, g.State().CurrentNightRole())
	// Detective (idle) -> Doctor.
	walkRestOfTurn(t, g)
	require.Equal(t, game.RoleDoctor, g.State().CurrentNightRole())
	// Doctor (idle) -> Tracker: the tracker wakes immediately after the doctor.
	walkRestOfTurn(t, g)
	require.Equal(t, game.RoleTracker, g.State().CurrentNightRole(),
		"the tracker wakes last, right after the doctor")
	require.Equal(t, game.NightSubAct, g.State().CurrentNightSubPhase())
}

// --- the visit read -------------------------------------------------------

// TestTracker_SeesTargetVisit: tracking a town role reveals exactly who that
// role visited (the target of its own night action), and nothing about what
// the action was.
func TestTracker_SeesTargetVisit(t *testing.T) {
	tests := []struct {
		name        string
		actions     map[game.Role]game.PlayerID
		trackTarget game.PlayerID
		wantVisited game.PlayerID
	}{
		{
			name:        "track the doctor reveals the save target",
			actions:     map[game.Role]game.PlayerID{game.RoleDoctor: "town1", game.RoleTracker: "doc"},
			trackTarget: "doc",
			wantVisited: "town1",
		},
		{
			name:        "track the detective reveals the investigation target",
			actions:     map[game.Role]game.PlayerID{game.RoleDetective: "mafia1", game.RoleTracker: "det"},
			trackTarget: "det",
			wantVisited: "mafia1",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			g := fixedRosterWithTracker(t)
			evts := runTrackerNightToDay(t, g, tc.actions)
			res, ok := findEvent[game.TrackerResult](evts)
			require.True(t, ok, "the tracker gets a result")
			require.Equal(t, tc.trackTarget, res.Target)
			require.Equal(t, tc.wantVisited, res.Visited,
				"the tracker learns exactly who the target visited")
		})
	}
}

// TestTracker_StayedHome: tracking a player who took no action that night
// yields an empty visit ("stayed home").
func TestTracker_StayedHome(t *testing.T) {
	g := fixedRosterWithTracker(t)
	// Nobody but the tracker acts; the tracker tracks an idle villager.
	evts := runTrackerNightToDay(t, g, map[game.Role]game.PlayerID{
		game.RoleTracker: "town1",
	})
	res, ok := findEvent[game.TrackerResult](evts)
	require.True(t, ok)
	require.Equal(t, game.PlayerID("town1"), res.Target)
	require.Equal(t, game.PlayerID(""), res.Visited,
		"a target that took no action reads as stayed home")
}

// TestTracker_DoctorSelfSaveReadsStayedHome: a doctor who saves THEMSELVES
// never left home, so tracking them reads "stayed home" (empty visit) rather
// than the nonsensical "<doctor> visited <doctor>". The self-target collapses
// to the no-visit reading in trackedVisit.
func TestTracker_DoctorSelfSaveReadsStayedHome(t *testing.T) {
	g := fixedRosterWithTracker(t)
	evts := runTrackerNightToDay(t, g, map[game.Role]game.PlayerID{
		game.RoleDoctor:  "doc", // the doctor saves themselves
		game.RoleTracker: "doc",
	})
	res, ok := findEvent[game.TrackerResult](evts)
	require.True(t, ok)
	require.Equal(t, game.PlayerID("doc"), res.Target)
	require.Equal(t, game.PlayerID(""), res.Visited,
		"a doctor who self-saves stayed home, not visited themselves")
}

// --- the mafia-faction read -----------------------------------------------

// TestTracker_TracksMafiaSeesFactionKill: tracking a mafioso reveals the
// faction's kill target, not the mafioso's personal entry — and reveals only
// the visit, never that it was a kill.
func TestTracker_TracksMafiaSeesFactionKill(t *testing.T) {
	g := fixedRosterWithTracker(t)
	evts := runTrackerNightToDay(t, g, map[game.Role]game.PlayerID{
		game.RoleMafia:   "town1",
		game.RoleTracker: "mafia1",
	})
	res, ok := findEvent[game.TrackerResult](evts)
	require.True(t, ok)
	require.Equal(t, game.PlayerID("mafia1"), res.Target)
	require.Equal(t, game.PlayerID("town1"), res.Visited,
		"tracking a mafioso reveals who the mafia targeted")
}

// TestTracker_NonSubmittingMafiaStillRevealsFactionTarget: in a 2-mafia game
// only one mafioso submits the kill. Tracking the OTHER mafioso (who has no
// personal entry) still reveals the faction target — and crucially does not
// leak which mafioso pressed the button.
func TestTracker_NonSubmittingMafiaStillRevealsFactionTarget(t *testing.T) {
	g := fixedRoster2MafiaWithTracker(t)
	// runNightToDay submits the kill via the first living mafioso (mafia1);
	// we then track mafia2, who never touched pendingNight.
	evts := runTrackerNightToDay(t, g, map[game.Role]game.PlayerID{
		game.RoleMafia:   "town1",
		game.RoleTracker: "mafia2",
	})
	res, ok := findEvent[game.TrackerResult](evts)
	require.True(t, ok)
	require.Equal(t, game.PlayerID("mafia2"), res.Target)
	require.Equal(t, game.PlayerID("town1"), res.Visited,
		"every mafia member reports the same faction target, regardless of who submitted")
}

// TestTracker_TracksMafiaOnRecruitNightSeesRecruitTarget: on a recruit night
// the faction "visits" the recruit target rather than killing. Tracking any
// mafia member that night reveals the recruit target. The just-recruited
// player, still town at the tracker's turn, reads as having stayed home.
func TestTracker_TracksMafiaOnRecruitNightSeesRecruitTarget(t *testing.T) {
	g := fixedRosterWithYakuzaAndTracker(t)

	// Mafia turn: the Yakuza recruits town1 instead of killing.
	recruit(t, g, "yak", "town1")

	// Walk Mafia(recruit ponder) -> Detective act -> Doctor act -> Tracker act.
	walkRestOfTurn(t, g)
	require.Equal(t, game.RoleDetective, g.State().CurrentNightRole())
	walkRestOfTurn(t, g)
	require.Equal(t, game.RoleDoctor, g.State().CurrentNightRole())
	walkRestOfTurn(t, g)
	require.Equal(t, game.RoleTracker, g.State().CurrentNightRole())
	require.Equal(t, game.NightSubAct, g.State().CurrentNightSubPhase())

	// Track the Yakuza: a mafia-faction member, so we see the faction's
	// target this night — the recruit target.
	yakBatch := nightAction(t, g, "trk", "yak")
	res, ok := findEvent[game.TrackerResult](yakBatch)
	require.True(t, ok)
	require.Equal(t, game.PlayerID("town1"), res.Visited,
		"on a recruit night the faction's visit is the recruit target")
}

// TestTracker_TracksNonRecruitingMafiaOnRecruitNight: the Yakuza recruits
// town1, but the tracker watches the OTHER mafioso (mafia1, who did nothing
// this night). The result must still reveal the faction's target — the
// recruit target — exactly as if it had tracked the Yakuza, so the tracker
// can't tell which mafia member actually performed the recruit.
func TestTracker_TracksNonRecruitingMafiaOnRecruitNight(t *testing.T) {
	g := fixedRosterWithYakuzaAndTracker(t)

	// Mafia turn: the Yakuza recruits town1 instead of killing. mafia1 takes
	// no action (the recruit closes the faction's act window).
	recruit(t, g, "yak", "town1")

	walkRestOfTurn(t, g) // -> Detective act
	require.Equal(t, game.RoleDetective, g.State().CurrentNightRole())
	walkRestOfTurn(t, g) // -> Doctor act
	require.Equal(t, game.RoleDoctor, g.State().CurrentNightRole())
	walkRestOfTurn(t, g) // -> Tracker act
	require.Equal(t, game.RoleTracker, g.State().CurrentNightRole())
	require.Equal(t, game.NightSubAct, g.State().CurrentNightSubPhase())

	// Track mafia1 — the mafioso who did NOT recruit.
	batch := nightAction(t, g, "trk", "mafia1")
	res, ok := findEvent[game.TrackerResult](batch)
	require.True(t, ok)
	require.Equal(t, game.PlayerID("mafia1"), res.Target)
	require.Equal(t, game.PlayerID("town1"), res.Visited,
		"any mafia member reports the faction's recruit target, not just the recruiter")
}

// TestTracker_TracksFreshRecruitReadsStayedHome: tracking the player being
// recruited THIS night reads "stayed home" — at the tracker's turn the
// recruit's role has not yet flipped (resolveRecruit runs later), and their
// own power is suppressed, so they visited no one.
func TestTracker_TracksFreshRecruitReadsStayedHome(t *testing.T) {
	g := fixedRosterWithYakuzaAndTracker(t)

	recruit(t, g, "yak", "town1")

	walkRestOfTurn(t, g) // -> Detective act
	walkRestOfTurn(t, g) // -> Doctor act
	walkRestOfTurn(t, g) // -> Tracker act
	require.Equal(t, game.RoleTracker, g.State().CurrentNightRole())

	batch := nightAction(t, g, "trk", "town1")
	res, ok := findEvent[game.TrackerResult](batch)
	require.True(t, ok)
	require.Equal(t, game.PlayerID(""), res.Visited,
		"the player being recruited this night visited no one")
}

// TestTracker_MafiaTargetsTrackerButDoctorSaves: the tracker is the mafia's
// kill target, but the doctor saves them. The tracker wakes AFTER the doctor
// and BEFORE resolution, so it still acts and learns its result this night;
// the save then cancels the kill at resolution and the tracker survives.
// Tracking the mafioso shows the faction came for the tracker themselves.
func TestTracker_MafiaTargetsTrackerButDoctorSaves(t *testing.T) {
	g := fixedRosterWithTracker(t)
	evts := runTrackerNightToDay(t, g, map[game.Role]game.PlayerID{
		game.RoleMafia:   "trk", // the mafia targets the tracker
		game.RoleDoctor:  "trk", // the doctor protects the tracker
		game.RoleTracker: "mafia1",
	})

	// The tracker acts even though it is the mafia's target — its turn comes
	// before resolution, while it is still alive.
	res, ok := findEvent[game.TrackerResult](evts)
	require.True(t, ok, "the tracker still acts the night it is targeted")
	require.Equal(t, game.PlayerID("trk"), res.Visited,
		"tracking the mafioso reveals the faction targeted the tracker themselves")

	// The save cancels the kill silently: nobody dies and the tracker lives.
	_, killed := findEvent[game.PlayerKilled](evts)
	require.False(t, killed, "the doctor's save cancels the kill — no death")
	require.True(t, livingByID(g, "trk"), "the saved tracker survives the night")
}

// --- privacy + immediacy --------------------------------------------------

// TestTracker_ResultIsImmediateAndPrivate: the result rides the very batch
// that records the track, and is visible ONLY to the tracker.
func TestTracker_ResultIsImmediateAndPrivate(t *testing.T) {
	g := fixedRosterWithTracker(t)
	// Walk to the tracker's act window with nobody else acting.
	walkRestOfTurn(t, g) // mafia -> detective
	walkRestOfTurn(t, g) // detective -> doctor
	walkRestOfTurn(t, g) // doctor -> tracker
	require.Equal(t, game.RoleTracker, g.State().CurrentNightRole())

	evts := nightAction(t, g, "trk", "mafia1")
	res, ok := findEvent[game.TrackerResult](evts)
	require.True(t, ok, "the result is delivered in the submit batch, not at resolve")
	require.Equal(t, game.PlayerID("trk"), res.Tracker)
	require.Equal(t, "player", res.Visibility().Audience)
	require.Equal(t, game.PlayerID("trk"), res.Visibility().Player,
		"the tracking result is private to the tracker")
}

// --- targeting rules ------------------------------------------------------

func TestTracker_CannotTrackSelf(t *testing.T) {
	g := fixedRosterWithTracker(t)
	walkRestOfTurn(t, g) // mafia -> detective
	walkRestOfTurn(t, g) // detective -> doctor
	walkRestOfTurn(t, g) // doctor -> tracker
	require.Equal(t, game.RoleTracker, g.State().CurrentNightRole())

	_, err := g.Apply(game.NightAction{Actor: "trk", Target: "trk"})
	require.ErrorIs(t, err, game.ErrSelfTarget,
		"the tracker cannot track themselves")
}

// TestTracker_DeadTargetRejected: Night 1 the mafia kills town1. Night 2,
// tracking the now-dead town1 is rejected.
func TestTracker_DeadTargetRejected(t *testing.T) {
	g := fixedRosterWithTracker(t)
	runTrackerNightToDay(t, g, map[game.Role]game.PlayerID{
		game.RoleMafia: "town1",
	})
	require.False(t, livingByID(g, "town1"))

	noLynchDay(t, g)
	beginNightToMafiaAct(t, g)
	walkRestOfTurn(t, g) // mafia -> detective
	walkRestOfTurn(t, g) // detective -> doctor
	walkRestOfTurn(t, g) // doctor -> tracker
	require.Equal(t, game.RoleTracker, g.State().CurrentNightRole())

	_, err := g.Apply(game.NightAction{Actor: "trk", Target: "town1"})
	require.ErrorIs(t, err, game.ErrPlayerDead,
		"the tracker cannot track a dead player")
}

// --- the Consort roleblock ------------------------------------------------

// TestTracker_BlockedByConsort: a Consort block leaves the tracker's turn
// phantom (no act window): it's notified via a private Blocked notice after
// its narrate, a bypassed submit is rejected, and it learns nothing.
func TestTracker_BlockedByConsort(t *testing.T) {
	g := fixedRosterWithConsortAndTracker(t)

	// Mafia idle -> Consort act. The consort distracts the tracker.
	walkRestOfTurn(t, g)
	require.Equal(t, game.RoleConsort, g.State().CurrentNightRole())
	nightAction(t, g, "cons", "trk")

	// Walk Consort -> Detective -> Doctor -> Tracker (blocked => phantom).
	walkRestOfTurn(t, g) // consort -> detective act
	require.Equal(t, game.RoleDetective, g.State().CurrentNightRole())
	walkRestOfTurn(t, g) // detective idle -> doctor act
	require.Equal(t, game.RoleDoctor, g.State().CurrentNightRole())
	trkBatch := walkRestOfTurn(t, g) // doctor idle -> tracker (phantom)
	require.Equal(t, game.RoleTracker, g.State().CurrentNightRole())
	require.Equal(t, game.NightSubPonder, g.State().CurrentNightSubPhase(),
		"a blocked tracker's turn is phantom — no act window")

	blk, ok := findEvent[game.Blocked](trkBatch)
	require.True(t, ok, "blocked tracker notified after narrate")
	require.Equal(t, game.PlayerID("trk"), blk.PlayerID)

	// Submit anyway: rejected (no act window), and no result is produced.
	evts, err := g.Apply(game.NightAction{Actor: "trk", Target: "mafia1"})
	require.ErrorIs(t, err, game.ErrNotYourTurn, "a blocked tracker has no act window")
	_, hasResult := findEvent[game.TrackerResult](evts)
	require.False(t, hasResult, "a blocked tracker learns nothing")
}

// --- recruit suppression (the last-waking active role) --------------------

// TestTracker_RecruitedByYakuzaSuppressesTracking: the Yakuza recruits the
// Tracker itself. The Tracker wakes last, but the recruit (locked during the
// Mafia turn) suppresses its power: its turn is phantom, it learns nothing,
// and it gets a private Recruited notice (never a result). This is the
// tracker analogue of TestYakuza_RecruitSuppressesDetective — the Tracker is
// the lone active role that lacked this coverage.
func TestTracker_RecruitedByYakuzaSuppressesTracking(t *testing.T) {
	g := fixedRosterWithYakuzaAndTracker(t)
	recruit(t, g, "yak", "trk")

	walkRestOfTurn(t, g)             // mafia (recruit ponder) -> detective act
	walkRestOfTurn(t, g)             // detective idle -> doctor act
	trkBatch := walkRestOfTurn(t, g) // doctor idle -> tracker (phantom)
	require.Equal(t, game.RoleTracker, g.State().CurrentNightRole())
	require.Equal(t, game.NightSubPonder, g.State().CurrentNightSubPhase(),
		"a recruited tracker's turn is phantom — no act window")

	notice, ok := findEvent[game.Recruited](trkBatch)
	require.True(t, ok, "the recruited tracker gets a private Recruited notice")
	require.Equal(t, game.PlayerID("trk"), notice.PlayerID)
	require.Empty(t, findAllEvents[game.TrackerResult](trkBatch),
		"a recruited tracker learns nothing")

	// A bypassing submit is rejected (no act window), and produces no result.
	evts, err := g.Apply(game.NightAction{Actor: "trk", Target: "mafia1"})
	require.ErrorIs(t, err, game.ErrNotYourTurn, "a recruited tracker has no act window")
	require.Empty(t, findAllEvents[game.TrackerResult](evts))

	finishNight(t, g)
	require.Equal(t, game.RoleMafia, roleByID(g, "trk"), "and the tracker is converted")
}

// --- death / resolution timing --------------------------------------------

// TestTracker_KilledTheNightItTracks: the mafia targets the tracker (unsaved)
// the same night it tracks. The tracker wakes AFTER the kill is locked but
// BEFORE resolution, so it still gets its result; the kill then lands and the
// tracker dies. And because a PrivateTo result is aliveness-agnostic for its
// owner, the now-dead tracker still re-projects its own result on
// reconnect/replay. (Covers both the deliver-before-death and the
// dead-owner-re-projection gaps.)
func TestTracker_KilledTheNightItTracks(t *testing.T) {
	g := fixedRosterWithTracker(t)
	evts := runTrackerNightToDay(t, g, map[game.Role]game.PlayerID{
		game.RoleMafia:   "trk",    // the mafia comes for the tracker, unsaved
		game.RoleTracker: "mafia1", // who tracks the mafioso back
	})

	res, ok := findEvent[game.TrackerResult](evts)
	require.True(t, ok, "the tracker still gets its result the night it is killed")
	require.Equal(t, game.PlayerID("trk"), res.Visited,
		"tracking the mafioso reveals the faction targeted the tracker itself")

	killed, ok := findEvent[game.PlayerKilled](evts)
	require.True(t, ok, "the unsaved kill lands")
	require.Equal(t, game.PlayerID("trk"), killed.PlayerID)
	require.False(t, livingByID(g, "trk"), "the tracker is dead after resolution")

	// Reconnect/replay: a dead owner still sees the private result it earned
	// while alive (the "player" audience is aliveness-agnostic for the owner).
	out := game.Project("trk", evts, g.State())
	require.NotEmpty(t, findAllEvents[game.TrackerResult](out),
		"a dead tracker re-projecting the log still sees its own earned result")
}

// --- faction read: settled convert + idle faction -------------------------

// TestTracker_TracksPriorNightConvertSeesFactionKill: a player recruited on a
// prior night now stands RoleMafia (the role flip settled at resolveRecruit).
// On a LATER kill night, tracking that convert reports the faction's
// collective kill target — exactly like a born mafioso — never the convert's
// own (empty) personal entry. Exercises the runtime role-flip path into
// trackedVisit's faction branch, distinct from the dealt-born-mafia path.
func TestTracker_TracksPriorNightConvertSeesFactionKill(t *testing.T) {
	g := fixedRosterWithYakuzaAndTracker(t)

	// Night 1: the Yakuza recruits town1 (converts it; sacrifices itself).
	recruit(t, g, "yak", "town1")
	finishNight(t, g)
	require.Equal(t, game.RoleMafia, roleByID(g, "town1"), "town1 is converted")
	require.True(t, livingByID(g, "town1"))
	require.False(t, livingByID(g, "yak"), "the Yakuza self-sacrificed")

	noLynchDay(t, g)
	beginNightToMafiaAct(t, g)

	// Night 2: the mafia kills town2; the tracker tracks the prior-night convert.
	evts := runTrackerNightToDay(t, g, map[game.Role]game.PlayerID{
		game.RoleMafia:   "town2",
		game.RoleTracker: "town1",
	})
	res, ok := findEvent[game.TrackerResult](evts)
	require.True(t, ok)
	require.Equal(t, game.PlayerID("town1"), res.Target)
	require.Equal(t, game.PlayerID("town2"), res.Visited,
		"a settled convert reports the faction's collective kill target, not its own entry")
}

// TestTracker_TracksIdleMafiaReadsStayedHome: when the mafia lets its act
// window lapse (no kill, no recruit), tracking a mafia member reads "stayed
// home". This is the only mafia-tracking case that exercises
// factionNightTarget's empty fall-through (vs the villager-idle pendingNight
// path covered by TestTracker_StayedHome).
func TestTracker_TracksIdleMafiaReadsStayedHome(t *testing.T) {
	g := fixedRosterWithTracker(t)
	// RoleMafia deliberately omitted -> the faction's window times out.
	evts := runTrackerNightToDay(t, g, map[game.Role]game.PlayerID{
		game.RoleTracker: "mafia1",
	})
	res, ok := findEvent[game.TrackerResult](evts)
	require.True(t, ok)
	require.Equal(t, game.PlayerID("mafia1"), res.Target)
	require.Equal(t, game.PlayerID(""), res.Visited,
		"tracking a mafia member on a night the faction did nothing reads stayed home")
}

// --- the Consort as a tracked target (non-faction branch) -----------------

// TestTracker_TracksConsortSeesBlockTarget: the Consort is FactionConsort,
// NOT FactionMafia, so tracking her routes through trackedVisit's non-faction
// branch and reveals her personal action target — whom she distracted — not
// the mafia's collective target. The Tracker learns the visit, never that it
// was a block.
func TestTracker_TracksConsortSeesBlockTarget(t *testing.T) {
	g := fixedRosterWithConsortAndTracker(t)
	order := []game.Role{game.RoleMafia, game.RoleConsort, game.RoleDetective, game.RoleDoctor, game.RoleTracker}
	evts := runNightToDay(t, g, order, map[game.Role]game.PlayerID{
		game.RoleConsort: "town2", // distract a villager (legal no-op)
		game.RoleTracker: "cons",  // track the consort
	})
	res, ok := findEvent[game.TrackerResult](evts)
	require.True(t, ok)
	require.Equal(t, game.PlayerID("cons"), res.Target)
	require.Equal(t, game.PlayerID("town2"), res.Visited,
		"the consort reports her block target via the personal-action branch, not the faction target")
}

// TestTracker_TracksConsortBlockedTargetReadsStayedHome: tracking a town role
// whose action was nullified by a Consort block reads "stayed home" — a
// blocked actor never writes a pendingNight entry, so it is indistinguishable
// from a genuinely idle target. The block does not leak through the tracker.
// (Contrast: TestTracker_SeesTargetVisit, where an UNblocked doctor's save
// target is revealed — so this test fails if a block ever leaked.)
func TestTracker_TracksConsortBlockedTargetReadsStayedHome(t *testing.T) {
	g := fixedRosterWithConsortAndTracker(t)
	order := []game.Role{game.RoleMafia, game.RoleConsort, game.RoleDetective, game.RoleDoctor, game.RoleTracker}
	evts := runNightToDay(t, g, order, map[game.Role]game.PlayerID{
		game.RoleConsort: "doc", // block the doctor (its turn goes phantom)
		game.RoleTracker: "doc", // track the blocked doctor
	})
	// Sanity: the doctor really was blocked (a private Blocked notice fired).
	blk, ok := findEvent[game.Blocked](evts)
	require.True(t, ok, "the doctor was blocked")
	require.Equal(t, game.PlayerID("doc"), blk.PlayerID)

	res, ok := findEvent[game.TrackerResult](evts)
	require.True(t, ok)
	require.Equal(t, game.PlayerID("doc"), res.Target)
	require.Equal(t, game.PlayerID(""), res.Visited,
		"a Consort-blocked target reads stayed home — the block does not leak")
}

// --- multi-night (no one-shot limit) --------------------------------------

// TestTracker_ReTracksAcrossNights: the Tracker has no one-shot limit — it
// tracks again on a later night and gets a fresh, correct result. The
// tracker analogue of TestDetective_ReinvestigatesAcrossNights.
func TestTracker_ReTracksAcrossNights(t *testing.T) {
	g := fixedRosterWithTracker(t)

	// Night 1: track the doctor, who saved town1.
	evts1 := runTrackerNightToDay(t, g, map[game.Role]game.PlayerID{
		game.RoleDoctor:  "town1",
		game.RoleTracker: "doc",
	})
	res1, ok := findEvent[game.TrackerResult](evts1)
	require.True(t, ok)
	require.Equal(t, game.PlayerID("doc"), res1.Target)
	require.Equal(t, game.PlayerID("town1"), res1.Visited)

	noLynchDay(t, g)
	beginNightToMafiaAct(t, g)

	// Night 2: track the detective, who investigated mafia1 — a fresh result.
	evts2 := runTrackerNightToDay(t, g, map[game.Role]game.PlayerID{
		game.RoleDetective: "mafia1",
		game.RoleTracker:   "det",
	})
	res2, ok := findEvent[game.TrackerResult](evts2)
	require.True(t, ok, "the tracker tracks again on night 2 — the power is not spent")
	require.Equal(t, game.PlayerID("det"), res2.Target)
	require.Equal(t, game.PlayerID("mafia1"), res2.Visited)
}
