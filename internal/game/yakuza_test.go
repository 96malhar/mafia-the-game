package game_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/96malhar/mafia-the-game/internal/game"
)

// --- yakuza fixtures ------------------------------------------------------

// fixedRosterWithYakuza builds a deterministic 6-player game with the
// optional Yakuza enabled, mapping each player ID to a fixed role:
//
//	mafia1 -> RoleMafia
//	yak    -> RoleYakuza
//	det    -> RoleDetective
//	doc    -> RoleDoctor
//	town1  -> RoleVillager
//	town2  -> RoleVillager
//
// The Yakuza is a full mafia member (FactionMafia) and has no separate
// night turn — it acts during the MAFIA turn. So the night turn order is
// the plain Mafia -> Detective -> Doctor, and on return the game sits on
// the MAFIA's act window (where both mafia1 and yak may submit).
func fixedRosterWithYakuza(t *testing.T) *game.Game {
	t.Helper()
	return fixedRosterMatching(t, yakuzaDeal())
}

// yakuzaDeal is the rosterDeal backing the standard 6-player Yakuza game,
// shared by fixedRosterWithYakuza (which walks it into the night) and by the
// StartGame-batch reveal test (which inspects the deal via fixedRosterDealt),
// so the roster lives in exactly one place.
func yakuzaDeal() rosterDeal {
	return rosterDeal{
		ids: []game.PlayerID{"mafia1", "yak", "det", "doc", "town1", "town2"},
		wanted: map[game.PlayerID]game.Role{
			"mafia1": game.RoleMafia,
			"yak":    game.RoleYakuza,
			"det":    game.RoleDetective,
			"doc":    game.RoleDoctor,
			"town1":  game.RoleVillager,
			"town2":  game.RoleVillager,
		},
		mafiaCount: 1,
		yakuza:     true,
		maxSeeds:   8000,
	}
}

// fixedRosterWithYakuzaAndVigilante builds a deterministic 6-player game
// with BOTH the Yakuza and the Vigilante enabled:
//
//	mafia1 -> RoleMafia
//	yak    -> RoleYakuza
//	det    -> RoleDetective
//	doc    -> RoleDoctor
//	vig    -> RoleVigilante
//	town1  -> RoleVillager
//
// Night order: Mafia -> Detective -> Vigilante -> Doctor (the Yakuza acts
// within the Mafia turn). On return the game sits on the MAFIA's act window.
func fixedRosterWithYakuzaAndVigilante(t *testing.T) *game.Game {
	t.Helper()
	return fixedRosterMatching(t, rosterDeal{
		ids: []game.PlayerID{"mafia1", "yak", "det", "doc", "vig", "town1"},
		wanted: map[game.PlayerID]game.Role{
			"mafia1": game.RoleMafia,
			"yak":    game.RoleYakuza,
			"det":    game.RoleDetective,
			"doc":    game.RoleDoctor,
			"vig":    game.RoleVigilante,
			"town1":  game.RoleVillager,
		},
		mafiaCount: 1,
		yakuza:     true,
		vigilante:  true,
		maxSeeds:   8000,
	})
}

// fixedRosterWithYakuzaAndConsort builds a deterministic 6-player game with
// BOTH the Yakuza and the Consort enabled:
//
//	mafia1 -> RoleMafia
//	yak    -> RoleYakuza
//	cons   -> RoleConsort
//	det    -> RoleDetective
//	doc    -> RoleDoctor
//	town1  -> RoleVillager
//
// Night order: Mafia -> Consort -> Detective -> Doctor. On return the game
// sits on the MAFIA's act window.
func fixedRosterWithYakuzaAndConsort(t *testing.T) *game.Game {
	t.Helper()
	return fixedRosterMatching(t, rosterDeal{
		ids: []game.PlayerID{"mafia1", "yak", "cons", "det", "doc", "town1"},
		wanted: map[game.PlayerID]game.Role{
			"mafia1": game.RoleMafia,
			"yak":    game.RoleYakuza,
			"cons":   game.RoleConsort,
			"det":    game.RoleDetective,
			"doc":    game.RoleDoctor,
			"town1":  game.RoleVillager,
		},
		mafiaCount: 1,
		yakuza:     true,
		consort:    true,
		maxSeeds:   8000,
	})
}

// fixedRosterWithYakuzaConsortVigilante builds a deterministic 7-player game
// with the Yakuza, Consort, AND Vigilante all enabled:
//
//	mafia1 -> RoleMafia
//	yak    -> RoleYakuza
//	cons   -> RoleConsort
//	vig    -> RoleVigilante
//	det    -> RoleDetective
//	doc    -> RoleDoctor
//	town1  -> RoleVillager
//
// Night order: Mafia -> Consort -> Detective -> Vigilante -> Doctor (the
// Yakuza acts within the Mafia turn). On return the game sits on the MAFIA's
// act window.
func fixedRosterWithYakuzaConsortVigilante(t *testing.T) *game.Game {
	t.Helper()
	return fixedRosterMatching(t, rosterDeal{
		ids: []game.PlayerID{"mafia1", "yak", "cons", "vig", "det", "doc", "town1"},
		wanted: map[game.PlayerID]game.Role{
			"mafia1": game.RoleMafia,
			"yak":    game.RoleYakuza,
			"cons":   game.RoleConsort,
			"vig":    game.RoleVigilante,
			"det":    game.RoleDetective,
			"doc":    game.RoleDoctor,
			"town1":  game.RoleVillager,
		},
		mafiaCount: 1,
		consort:    true,
		vigilante:  true,
		yakuza:     true,
		maxSeeds:   60000,
	})
}

// recruit submits a Yakuza recruit and fails the test on rejection.
func recruit(t *testing.T, g *game.Game, yakuza, target game.PlayerID) []game.Event {
	t.Helper()
	evts, err := g.Apply(game.Recruit{Actor: yakuza, Target: target})
	require.NoError(t, err)
	return evts
}

// --- roster + toggle ------------------------------------------------------

func TestYakuza_SetYakuzaToggle(t *testing.T) {
	g := game.New()
	_, err := g.Apply(game.CreateGame{
		GameID: "g1", MinPlayers: 6, MaxPlayers: 20, MafiaCount: 1, Seed: 0,
	})
	require.NoError(t, err)

	require.False(t, g.State().YakuzaEnabled(), "yakuza defaults off")

	evts, err := g.Apply(game.SetYakuza{Enabled: true})
	require.NoError(t, err)
	yc, ok := findEvent[game.YakuzaChanged](evts)
	require.True(t, ok, "toggling on emits YakuzaChanged")
	require.True(t, yc.Enabled)
	require.True(t, g.State().YakuzaEnabled())

	// Re-enabling is a no-op.
	_, err = g.Apply(game.SetYakuza{Enabled: true})
	require.ErrorIs(t, err, game.ErrNoChange)

	_, err = g.Apply(game.SetYakuza{Enabled: false})
	require.NoError(t, err)
	require.False(t, g.State().YakuzaEnabled())
}

// TestYakuza_DealtIntoMafiaRoster asserts the Yakuza is dealt FactionMafia
// and appears in the MafiaRosterRevealed the cabal sees at StartGame.
func TestYakuza_DealtIntoMafiaRoster(t *testing.T) {
	g := fixedRosterWithYakuza(t)
	require.Equal(t, game.RoleYakuza, roleByID(g, "yak"))
	require.Equal(t, game.FactionMafia, game.RoleYakuza.Faction(),
		"the Yakuza must be a full mafia member")
}

// TestYakuza_RosterRevealIncludesYakuza captures the StartGame batch and
// asserts the mafia roster lists BOTH the mafioso and the Yakuza. It deals the
// same fixed roster as fixedRosterWithYakuza (via the shared yakuzaDeal), but
// through fixedRosterDealt so it can inspect the StartGame batch directly
// rather than the post-night game the fixture returns.
func TestYakuza_RosterRevealIncludesYakuza(t *testing.T) {
	_, evts := fixedRosterDealt(t, yakuzaDeal())
	roster, ok := findEvent[game.MafiaRosterRevealed](evts)
	require.True(t, ok, "StartGame reveals the mafia roster")
	require.ElementsMatch(t, []game.PlayerID{"mafia1", "yak"}, roster.Members,
		"the roster names both the mafioso and the Yakuza")
	require.Equal(t, game.PlayerID("yak"), roster.Yakuza,
		"the roster identifies which member is the Yakuza so the faction can badge it")
}

// TestYakuza_RosterYakuzaFieldEmptyWithoutYakuza: a game with no Yakuza
// dealt leaves the roster's Yakuza field empty.
func TestYakuza_RosterYakuzaFieldEmptyWithoutYakuza(t *testing.T) {
	g := game.New()
	_, err := g.Apply(game.CreateGame{
		GameID: "g1", MinPlayers: 5, MaxPlayers: 20, MafiaCount: 1, Seed: 1,
	})
	require.NoError(t, err)
	addPlayers(t, g, "a", "b", "c", "d", "e")
	start, err := g.Apply(game.StartGame{})
	require.NoError(t, err)
	roster, ok := findEvent[game.MafiaRosterRevealed](start)
	require.True(t, ok)
	require.Equal(t, game.PlayerID(""), roster.Yakuza,
		"no Yakuza dealt -> empty Yakuza field")
}

// --- the Yakuza as a plain mafioso (faction kill) -------------------------

// TestYakuza_KillsAsMafia: the Yakuza may submit the ordinary faction kill
// during the Mafia turn, and it lands like any mafioso's.
func TestYakuza_KillsAsMafia(t *testing.T) {
	g := fixedRosterWithYakuza(t)

	// The Yakuza (not the mafioso) submits the kill during the Mafia turn.
	evts := nightAction(t, g, "yak", "town1")
	rec, ok := findEvent[game.NightActionRecorded](evts)
	require.True(t, ok)
	require.Equal(t, game.FactionMafia, rec.Faction, "the kill is a faction action")

	evts = append(evts, finishNight(t, g)...)
	killed, ok := findEvent[game.PlayerKilled](evts)
	require.True(t, ok, "the Yakuza's faction kill lands")
	require.Equal(t, game.PlayerID("town1"), killed.PlayerID)
	require.False(t, livingByID(g, "town1"))
}

// --- recruit happy path ---------------------------------------------------

// TestYakuza_RecruitConvertsAndSacrifices is the core flow: a recruit
// converts the target to full mafia, kills the Yakuza, forgoes the faction
// kill, and re-issues the roster.
func TestYakuza_RecruitConvertsAndSacrifices(t *testing.T) {
	g := fixedRosterWithYakuza(t)

	evts := recruit(t, g, "yak", "town1")
	// Co-mafia see the locked recruit; the graveyard sees the mirror.
	_, ok := findEvent[game.RecruitRecorded](evts)
	require.True(t, ok, "co-mafia get a RecruitRecorded ack")
	spec, ok := findEvent[game.SpectatorNightAction](evts)
	require.True(t, ok)
	require.True(t, spec.Recruit, "the graveyard mirror is flagged as a recruit")

	evts = append(evts, finishNight(t, g)...)

	// The convert is now a full mafioso and was told privately.
	require.Equal(t, game.RoleMafia, roleByID(g, "town1"))
	ra, ok := findEvent[game.RoleAssigned](evts)
	require.True(t, ok, "the convert gets a private RoleAssigned")
	require.Equal(t, game.PlayerID("town1"), ra.PlayerID)
	require.Equal(t, game.RoleMafia, ra.Role)

	// The Yakuza sacrificed itself; nobody else died (no faction kill).
	require.False(t, livingByID(g, "yak"), "the Yakuza dies")
	killed := findAllEvents[game.PlayerKilled](evts)
	require.Len(t, killed, 1, "exactly one death — the Yakuza's sacrifice")
	require.Equal(t, game.PlayerID("yak"), killed[0].PlayerID)

	// The roster is re-issued to the faction with the FULL cabal: the living
	// mafioso, the new convert, AND the now-dead Yakuza (so the convert sees
	// every predecessor). The Yakuza field still names the Yakuza so it's
	// badged distinctly even though it's dead.
	roster, ok := findEvent[game.MafiaRosterRevealed](evts)
	require.True(t, ok, "the faction roster is re-issued")
	require.ElementsMatch(t, []game.PlayerID{"mafia1", "town1", "yak"}, roster.Members)
	require.Equal(t, game.PlayerID("yak"), roster.Yakuza)
}

// TestYakuza_RecruitRosterReachesConvertNotTown projects the recruit
// resolution batch: the convert (now mafia) receives the full-cabal roster —
// living mafioso, itself, and the dead Yakuza — while a living town player
// receives nothing. This is the visibility contract a rejoin replays, so it
// pins both "the convert sees its predecessors" and "living town never sees
// this, ever".
func TestYakuza_RecruitRosterReachesConvertNotTown(t *testing.T) {
	g := fixedRosterWithYakuza(t)
	evts := recruit(t, g, "yak", "town1")
	evts = append(evts, finishNight(t, g)...)

	// The convert (town1, now RoleMafia) sees the re-issued roster naming the
	// full cabal, including the now-dead Yakuza, with the Yakuza field set.
	convertView := game.Project("town1", evts, g.State())
	rosters := findAllEvents[game.MafiaRosterRevealed](convertView)
	require.NotEmpty(t, rosters, "the convert (now mafia) receives the re-issued roster")
	last := rosters[len(rosters)-1]
	require.ElementsMatch(t, []game.PlayerID{"mafia1", "town1", "yak"}, last.Members)
	require.Equal(t, game.PlayerID("yak"), last.Yakuza)

	// A still-town player (alive) must never receive the roster.
	require.True(t, livingByID(g, "town2"))
	townView := game.Project("town2", evts, g.State())
	require.Empty(t, findAllEvents[game.MafiaRosterRevealed](townView),
		"living town must never see the mafia roster")
}

// --- mutual exclusion of kill and recruit ---------------------------------

// TestYakuza_KillThenRecruitRejected: once the faction kill is submitted the
// act window closes, so a follow-up recruit is rejected.
func TestYakuza_KillThenRecruitRejected(t *testing.T) {
	g := fixedRosterWithYakuza(t)
	nightAction(t, g, "mafia1", "town1") // mafioso kills first, closing the window
	_, err := g.Apply(game.Recruit{Actor: "yak", Target: "town2"})
	require.ErrorIs(t, err, game.ErrNotYourTurn,
		"a recruit after the kill locked the turn is rejected")
}

// TestYakuza_RecruitThenKillRejected: once the recruit is submitted the act
// window closes, so a follow-up faction kill is rejected.
func TestYakuza_RecruitThenKillRejected(t *testing.T) {
	g := fixedRosterWithYakuza(t)
	recruit(t, g, "yak", "town1")
	_, err := g.Apply(game.NightAction{Actor: "mafia1", Target: "town2"})
	require.ErrorIs(t, err, game.ErrNotYourTurn,
		"a kill after the recruit locked the turn is rejected")
}

// --- recruit validation ---------------------------------------------------

func TestYakuza_CannotRecruitMafia(t *testing.T) {
	g := fixedRosterWithYakuza(t)
	_, err := g.Apply(game.Recruit{Actor: "yak", Target: "mafia1"})
	require.ErrorIs(t, err, game.ErrNotYourAction,
		"recruiting an existing mafioso is rejected")
}

func TestYakuza_CannotRecruitSelf(t *testing.T) {
	g := fixedRosterWithYakuza(t)
	_, err := g.Apply(game.Recruit{Actor: "yak", Target: "yak"})
	require.ErrorIs(t, err, game.ErrSelfTarget)
}

func TestYakuza_NonYakuzaCannotRecruit(t *testing.T) {
	g := fixedRosterWithYakuza(t)
	_, err := g.Apply(game.Recruit{Actor: "mafia1", Target: "town1"})
	require.ErrorIs(t, err, game.ErrNotYourAction,
		"only the Yakuza may recruit")
}

// TestYakuza_CanRecruitConsort: the Consort (mafia-aligned but not a strict
// mafioso) is a legal recruit target.
func TestYakuza_CanRecruitConsort(t *testing.T) {
	g := fixedRosterWithYakuzaAndConsort(t)
	_, err := g.Apply(game.Recruit{Actor: "yak", Target: "cons"})
	require.NoError(t, err, "recruiting the Consort is allowed")
	finishNight(t, g)
	require.Equal(t, game.RoleMafia, roleByID(g, "cons"),
		"the recruited Consort becomes a full mafioso")
	require.False(t, livingByID(g, "yak"))
}

// --- power suppression ----------------------------------------------------

// TestYakuza_RecruitSuppressesDetective: recruiting the Detective makes its
// turn phantom (it produces no result) and privately notifies it via
// Recruited.
func TestYakuza_RecruitSuppressesDetective(t *testing.T) {
	g := fixedRosterWithYakuza(t)
	recruit(t, g, "yak", "det")
	evts := finishNight(t, g)

	require.Empty(t, findAllEvents[game.DetectiveResult](evts),
		"a recruited detective produces no investigation result")
	notice, ok := findEvent[game.Recruited](evts)
	require.True(t, ok, "the recruited detective gets a private Recruited notice")
	require.Equal(t, game.PlayerID("det"), notice.PlayerID)
	require.Equal(t, game.RoleMafia, roleByID(g, "det"), "and is converted")
}

// TestYakuza_RecruitedAndBlockedShowsOnlyRecruited: when the Yakuza recruits
// the same active role the Consort distracts, the recruit notice takes
// precedence — the player sees ONLY the Recruited notice (at their turn) and
// NEVER a Blocked notice. The conversion subsumes the distract.
func TestYakuza_RecruitedAndBlockedShowsOnlyRecruited(t *testing.T) {
	g := fixedRosterWithYakuzaAndConsort(t)
	var evts []game.Event
	evts = append(evts, recruit(t, g, "yak", "doc")...) // recruit the doctor
	// Walk to the consort's act window and block the SAME target.
	evts = append(evts, walkRestOfTurn(t, g)...) // mafia ponder -> consort act
	require.Equal(t, game.RoleConsort, g.State().CurrentNightRole())
	evts = append(evts, nightAction(t, g, "cons", "doc")...)
	evts = append(evts, finishNight(t, g)...)

	require.Empty(t, findAllEvents[game.Blocked](evts),
		"a recruited+blocked player must NEVER get a Blocked notice")
	recruited := findAllEvents[game.Recruited](evts)
	require.Len(t, recruited, 1, "exactly one Recruited notice")
	require.Equal(t, game.PlayerID("doc"), recruited[0].PlayerID)
	require.Equal(t, game.PlayerID("doc"), recruited[0].Visibility().Player,
		"the recruit notice stays private to the convert")
	// The notice lands at the doctor's TURN, i.e. before the night's
	// resolution batch (the Yakuza's self-sacrifice PlayerKilled).
	requireEventOrder(t,
		orderedEvent{"Recruited (at the turn)", indexOfEvent[game.Recruited](evts)},
		orderedEvent{"PlayerKilled (at resolution)", indexOfEvent[game.PlayerKilled](evts)},
	)
	require.Equal(t, game.RoleMafia, roleByID(g, "doc"), "the doctor is converted")
}

// TestYakuza_RecruitVillagerNotifiesAtResolution: a recruited villager has
// no night turn, so the Recruited notice is delivered at night resolution.
func TestYakuza_RecruitVillagerNotifiesAtResolution(t *testing.T) {
	g := fixedRosterWithYakuza(t)
	recruit(t, g, "yak", "town1")
	evts := finishNight(t, g)

	notice, ok := findEvent[game.Recruited](evts)
	require.True(t, ok, "the recruited villager is notified")
	require.Equal(t, game.PlayerID("town1"), notice.PlayerID)
	// The Recruited notice rides in the same resolve batch as the Yakuza's
	// death (a villager has no earlier turn to be told at), before the kill.
	requireEventOrder(t,
		orderedEvent{"recruited", indexOfEvent[game.Recruited](evts)},
		orderedEvent{"playerKilled", indexOfEvent[game.PlayerKilled](evts)},
	)
}

// TestYakuza_RecruitKilledBeforeConversionGetsNoNotice: if the recruit target
// dies before the conversion resolves (here the Vigilante shoots them the same
// night), the conversion is wasted and the player gets NO recruit notices —
// neither Recruited nor RoleAssigned. They die a plain villager, with no
// private toast, exactly like any other night-kill victim. (Kills resolve in
// resolvePhase, before resolveRecruit, whose conversion is gated on tp.alive.)
func TestYakuza_RecruitKilledBeforeConversionGetsNoNotice(t *testing.T) {
	g := fixedRosterWithYakuzaAndVigilante(t)
	recruit(t, g, "yak", "town1")
	walkRestOfTurn(t, g) // mafia ponder -> detective act
	walkRestOfTurn(t, g) // detective idle -> vigilante act
	require.Equal(t, game.RoleVigilante, g.State().CurrentNightRole())
	evts := nightAction(t, g, "vig", "town1") // kill the recruit
	evts = append(evts, finishNight(t, g)...)

	require.False(t, livingByID(g, "town1"))
	require.Equal(t, game.RoleVillager, roleByID(g, "town1"),
		"the recruit died before converting — still a villager, never mafia")
	require.Empty(t, findAllEvents[game.Recruited](evts),
		"a recruit that died before converting gets no Recruited notice")
	require.Empty(t, findAllEvents[game.RoleAssigned](evts),
		"...and no RoleAssigned — the conversion was skipped on the dead target")
}

// --- detective reads ------------------------------------------------------

// TestYakuza_DetectiveReadsYakuzaAsMafiaWhenNotRecruited: before any recruit
// the Yakuza reads as mafia.
func TestYakuza_DetectiveReadsYakuzaAsMafiaWhenNotRecruited(t *testing.T) {
	g := fixedRosterWithYakuza(t)
	// Walk past the (no-action) Mafia turn to the detective's act window.
	walkRestOfTurn(t, g)
	require.Equal(t, game.RoleDetective, g.State().CurrentNightRole())
	evts := nightAction(t, g, "det", "yak")
	res, ok := findEvent[game.DetectiveResult](evts)
	require.True(t, ok)
	require.True(t, res.IsMafia, "an un-recruited Yakuza reads as mafia")
}

// TestYakuza_DetectiveReadsYakuzaAsCleanAfterRecruit: once the Yakuza has
// recruited (committed its sacrifice) it reads as NOT mafia.
func TestYakuza_DetectiveReadsYakuzaAsCleanAfterRecruit(t *testing.T) {
	g := fixedRosterWithYakuza(t)
	recruit(t, g, "yak", "town1") // recruit during the Mafia turn
	walkRestOfTurn(t, g)          // -> detective act window
	require.Equal(t, game.RoleDetective, g.State().CurrentNightRole())
	evts := nightAction(t, g, "det", "yak")
	res, ok := findEvent[game.DetectiveResult](evts)
	require.True(t, ok)
	require.False(t, res.IsMafia, "a Yakuza that has recruited reads as NOT mafia")
}

// TestYakuza_DetectiveReadsRecruitTargetAsMafia: the recruit target reads as
// mafia immediately, even the same night (before the role flip resolves).
func TestYakuza_DetectiveReadsRecruitTargetAsMafia(t *testing.T) {
	g := fixedRosterWithYakuza(t)
	recruit(t, g, "yak", "town1")
	walkRestOfTurn(t, g) // -> detective act window
	require.Equal(t, game.RoleDetective, g.State().CurrentNightRole())
	// town1 is still a villager in state (flip resolves at night end), but
	// the in-flight recruit makes the detective read it as mafia.
	require.Equal(t, game.RoleVillager, roleByID(g, "town1"))
	evts := nightAction(t, g, "det", "town1")
	res, ok := findEvent[game.DetectiveResult](evts)
	require.True(t, ok)
	require.True(t, res.IsMafia, "the recruit target reads as mafia immediately")
}

// --- doctor save vs the sacrifice -----------------------------------------

// TestYakuza_DoctorCannotSaveSacrifice: a doctor save on the Yakuza does not
// prevent the self-sacrifice — it still dies and the recruit still succeeds.
func TestYakuza_DoctorCannotSaveSacrifice(t *testing.T) {
	g := fixedRosterWithYakuza(t)
	recruit(t, g, "yak", "town1")
	// Walk to the doctor's act window and save the Yakuza.
	walkRestOfTurn(t, g) // mafia ponder -> detective act
	walkRestOfTurn(t, g) // detective -> doctor act
	require.Equal(t, game.RoleDoctor, g.State().CurrentNightRole())
	nightAction(t, g, "doc", "yak")
	finishNight(t, g)

	require.False(t, livingByID(g, "yak"),
		"the sacrifice is unpreventable — the doctor can't save the Yakuza")
	require.Equal(t, game.RoleMafia, roleByID(g, "town1"), "the recruit still succeeds")
}

// --- vigilante vs the sacrifice -------------------------------------------

// TestYakuza_VigilanteBulletSpentAgainstSacrifice: shooting a Yakuza that has
// already recruited this night does NOT land (the sacrifice kills it), but
// the bullet is SPENT — consistent with firing at a target the mafia already
// killed.
func TestYakuza_VigilanteBulletSpentAgainstSacrifice(t *testing.T) {
	g := fixedRosterWithYakuzaAndVigilante(t)
	recruit(t, g, "yak", "town1") // recruit during the Mafia turn
	// Walk to the vigilante's act window (Mafia -> Detective -> Vigilante).
	walkRestOfTurn(t, g) // mafia ponder -> detective act
	walkRestOfTurn(t, g) // detective -> vigilante act
	require.Equal(t, game.RoleVigilante, g.State().CurrentNightRole())
	evts := nightAction(t, g, "vig", "yak") // shoot the sacrificing Yakuza
	evts = append(evts, finishNight(t, g)...)

	require.True(t, g.State().VigilanteShotUsed(), "the bullet is spent on the wasted shot")

	// The Yakuza dies (from its own sacrifice) exactly once — the wasted
	// shot adds no second death.
	require.False(t, livingByID(g, "yak"))
	require.Len(t, findAllEvents[game.PlayerKilled](evts), 1)
	require.Equal(t, game.RoleMafia, roleByID(g, "town1"), "the recruit still succeeds")
}

// TestYakuza_VigilanteKillsYakuzaWhenNoRecruit: contrast — shooting the
// Yakuza on a night it did NOT recruit kills it normally and spends the bullet.
func TestYakuza_VigilanteKillsYakuzaWhenNoRecruit(t *testing.T) {
	g := fixedRosterWithYakuzaAndVigilante(t)
	// Mafia turn passes with no kill/recruit; walk to the vigilante.
	walkRestOfTurn(t, g) // mafia (timeout) -> detective act
	walkRestOfTurn(t, g) // detective -> vigilante act
	require.Equal(t, game.RoleVigilante, g.State().CurrentNightRole())
	nightAction(t, g, "vig", "yak")
	finishNight(t, g)

	require.True(t, g.State().VigilanteShotUsed(), "the bullet is spent")
	require.False(t, livingByID(g, "yak"), "the vigilante's shot lands")
}

// TestYakuza_RecruitKeepsFactionAliveSoConsortNotPromoted: if the Yakuza
// recruits someone AND the Vigilante kills the last original mafioso the same
// night, the Consort must NOT be promoted — the fresh convert is now a living
// mafia, so the faction never reached zero. This works because the conversion
// runs inside resolveNight (resolveRecruit), BEFORE the post-night
// promoteConsortIfNeeded check, so the convert already counts as RoleMafia.
func TestYakuza_RecruitKeepsFactionAliveSoConsortNotPromoted(t *testing.T) {
	g := fixedRosterWithYakuzaConsortVigilante(t)

	// Mafia turn: the Yakuza recruits the villager (forgoing the kill).
	recruit(t, g, "yak", "town1")
	// Walk to the vigilante's act window (Consort, Detective idle).
	walkRestOfTurn(t, g) // mafia ponder -> consort act
	require.Equal(t, game.RoleConsort, g.State().CurrentNightRole())
	walkRestOfTurn(t, g) // consort idle -> detective act
	require.Equal(t, game.RoleDetective, g.State().CurrentNightRole())
	walkRestOfTurn(t, g) // detective idle -> vigilante act
	require.Equal(t, game.RoleVigilante, g.State().CurrentNightRole())
	// The vigilante shoots the LAST original mafioso (not the new recruit).
	evts := nightAction(t, g, "vig", "mafia1")
	evts = append(evts, finishNight(t, g)...)

	// Both members of the original cabal are dead...
	require.False(t, livingByID(g, "mafia1"), "the vigilante killed the original mafioso")
	require.False(t, livingByID(g, "yak"), "the Yakuza sacrificed itself")
	// ...but the convert is a LIVING RoleMafia, so the faction survived.
	require.True(t, livingByID(g, "town1"))
	require.Equal(t, game.RoleMafia, roleByID(g, "town1"), "the recruit is the remaining mafia")

	// Therefore the Consort is NOT promoted and stays a Consort.
	require.Empty(t, findAllEvents[game.ConsortPromoted](evts),
		"the convert keeps the faction alive, so no consort promotion")
	require.Equal(t, game.RoleConsort, roleByID(g, "cons"),
		"the consort must remain a consort")

	// And the game continues (the mafia side is alive via the convert).
	require.Empty(t, findAllEvents[game.GameEnded](evts), "neither side has won")
	require.Equal(t, game.PhaseDayDiscussion, g.State().Phase())
}

// TestYakuza_VigilanteKillsRecruitSoConsortPromoted: the converse of the test
// above. The Yakuza is the LAST mafia and recruits someone, but the Vigilante
// kills that recruit the SAME night. Kills resolve before the conversion
// (resolvePhase before resolveRecruit), so the recruit dies a villager and the
// conversion is wasted — leaving NO living mafia once the Yakuza sacrifices
// itself, so the Consort IS promoted.
func TestYakuza_VigilanteKillsRecruitSoConsortPromoted(t *testing.T) {
	g := fixedRosterWithYakuzaConsortVigilante(t)

	// Reach a state where the Yakuza is the last mafia-faction killer: a quiet
	// night, then lynch the only original mafioso (keeping the Vigilante's
	// bullet loaded).
	finishNight(t, g) // quiet night 1
	require.Equal(t, game.PhaseDayDiscussion, g.State().Phase())
	finalizeLynch(t, g, "mafia1")
	require.Equal(t, game.RoleConsort, roleByID(g, "cons"),
		"not promoted yet — the Yakuza is still the remaining mafia")
	require.True(t, livingByID(g, "yak"))

	// Night 2: the Yakuza (now the last mafia) recruits town1; the Vigilante
	// shoots that SAME recruit.
	_, err := g.Apply(game.BeginNight{})
	require.NoError(t, err)
	advanceToMafiaAct(t, g)
	recruit(t, g, "yak", "town1")
	walkRestOfTurn(t, g) // mafia ponder -> consort act
	walkRestOfTurn(t, g) // consort idle -> detective act
	walkRestOfTurn(t, g) // detective idle -> vigilante act
	require.Equal(t, game.RoleVigilante, g.State().CurrentNightRole())
	evts := nightAction(t, g, "vig", "town1") // kill the recruit
	evts = append(evts, finishNight(t, g)...)

	// The recruit died before the conversion resolved, so it's wasted: town1
	// is a dead villager, never a mafioso.
	require.False(t, livingByID(g, "town1"))
	require.Equal(t, game.RoleVillager, roleByID(g, "town1"),
		"the recruit died before converting — conversion wasted")
	require.False(t, livingByID(g, "yak"), "the Yakuza sacrificed itself")

	// No living mafia remain, so the Consort takes over.
	promo, ok := findEvent[game.ConsortPromoted](evts)
	require.True(t, ok, "with no mafia left, the consort is promoted")
	require.Equal(t, game.PlayerID("cons"), promo.PlayerID)
	require.Equal(t, game.RoleMafia, roleByID(g, "cons"), "the consort inherits RoleMafia")
}

// --- mafia turn stays real with only the Yakuza alive ---------------------

// TestYakuza_MafiaTurnOpensWhenOnlyYakuzaAlive: with every strict mafioso
// dead but the Yakuza alive, the Mafia turn is NOT phantom — the Yakuza can
// still act.
func TestYakuza_MafiaTurnOpensWhenOnlyYakuzaAlive(t *testing.T) {
	g := fixedRosterWithYakuza(t)
	// Night 1: nobody acts; resolve to day.
	finishNight(t, g)
	require.Equal(t, game.PhaseDayDiscussion, g.State().Phase())
	// Lynch the only strict mafioso. The Yakuza (FactionMafia) keeps the
	// faction alive, so the game continues.
	finalizeLynch(t, g, "mafia1")
	require.Equal(t, game.PhaseDayDiscussion, g.State().Phase(),
		"the game continues — the Yakuza is still mafia")

	// Night 2: the Mafia turn must still open as a real (non-phantom) turn.
	_, err := g.Apply(game.BeginNight{})
	require.NoError(t, err)
	advanceToMafiaAct(t, g) // asserts we reach the mafia ACT window
	require.Equal(t, game.NightSubAct, g.State().CurrentNightSubPhase())

	// And the Yakuza can take the faction kill.
	nightAction(t, g, "yak", "town1")
	finishNight(t, g)
	require.False(t, livingByID(g, "town1"))
}

// --- win counting + consort promotion gating ------------------------------

// TestYakuza_CountsTowardMafiaParityWin: the Yakuza is counted as mafia for
// the parity win.
func TestYakuza_CountsTowardMafiaParityWin(t *testing.T) {
	g := fixedRosterWithYakuza(t)
	// 6 players: {mafia1, yak} vs {det, doc, town1, town2}. Two unsaved
	// faction kills drop town to 2, then the third reaches strict-mafia
	// parity-plus (2 mafia vs 2 town -> continue; next kill 2 vs 1 -> win).
	// We let the Yakuza/mafioso kill across nights.
	killNight := func(target game.PlayerID) {
		advanceToMafiaActIfNeeded(t, g)
		nightAction(t, g, "mafia1", target)
		finishNight(t, g)
	}
	killNight("town1") // 2 vs 3
	require.Equal(t, game.PhaseDayDiscussion, g.State().Phase())
	noLynchDay(t, g)
	_, err := g.Apply(game.BeginNight{})
	require.NoError(t, err)
	advanceToMafiaAct(t, g)
	nightAction(t, g, "yak", "town2") // 2 vs 2 -> parity, continue
	finishNight(t, g)
	require.Equal(t, game.PhaseDayDiscussion, g.State().Phase())
	noLynchDay(t, g)
	_, err = g.Apply(game.BeginNight{})
	require.NoError(t, err)
	advanceToMafiaAct(t, g)
	nightAction(t, g, "mafia1", "det") // 2 vs 1 -> mafia win
	finishNight(t, g)
	require.Equal(t, game.PhaseEnded, g.State().Phase())
}

// TestYakuza_BlocksConsortPromotion: with a Consort + Yakuza, the Consort is
// promoted ONLY once every FactionMafia member (the mafioso AND the Yakuza)
// is dead — a living Yakuza blocks promotion.
func TestYakuza_BlocksConsortPromotion(t *testing.T) {
	g := fixedRosterWithYakuzaAndConsort(t)
	// Night 1: quiet night to reach day.
	finishNight(t, g)
	require.Equal(t, game.PhaseDayDiscussion, g.State().Phase())

	// Lynch the strict mafioso. The Yakuza still lives, so the Consort must
	// NOT be promoted.
	evts := finalizeLynch(t, g, "mafia1")
	require.Empty(t, findAllEvents[game.ConsortPromoted](evts),
		"a living Yakuza blocks Consort promotion")
	require.Equal(t, game.RoleConsort, roleByID(g, "cons"),
		"the Consort is not promoted while the Yakuza lives")
	require.Equal(t, game.PhaseDayDiscussion, g.State().Phase())

	// Another quiet night, then lynch the Yakuza — now every FactionMafia
	// member is dead, so the Consort is promoted.
	_, err := g.Apply(game.BeginNight{})
	require.NoError(t, err)
	advanceToMafiaAct(t, g)
	finishNight(t, g)
	require.Equal(t, game.PhaseDayDiscussion, g.State().Phase())
	evts = finalizeLynch(t, g, "yak")
	promoted, ok := findEvent[game.ConsortPromoted](evts)
	require.True(t, ok, "with the Yakuza dead too, the Consort is promoted")
	require.Equal(t, game.PlayerID("cons"), promoted.PlayerID)
	require.Equal(t, game.RoleMafia, roleByID(g, "cons"))
}

// advanceToMafiaActIfNeeded walks from a fresh PhaseNight (opening) to the
// mafia act window only when not already there — a small convenience for the
// multi-night win test.
func advanceToMafiaActIfNeeded(t *testing.T, g *game.Game) {
	t.Helper()
	if g.State().CurrentNightSubPhase() == game.NightSubAct &&
		g.State().CurrentNightRole() == game.RoleMafia {
		return
	}
	advanceToMafiaAct(t, g)
}

// --- recruit interaction edge cases ---------------------------------------

// TestYakuza_RecruitedDoctorCannotSaveVigilanteVictim proves the recruit's
// power suppression has teeth: a recruited Doctor's turn is phantom, so no
// save lands and the Vigilante's victim dies. (The recruited-Detective test
// proves the no-result case; this proves the no-save consequence.)
func TestYakuza_RecruitedDoctorCannotSaveVigilanteVictim(t *testing.T) {
	g := fixedRosterWithYakuzaAndVigilante(t)
	var evts []game.Event
	evts = append(evts, recruit(t, g, "yak", "doc")...) // recruit the doctor
	evts = append(evts, walkRestOfTurn(t, g)...)        // mafia ponder -> detective act
	require.Equal(t, game.RoleDetective, g.State().CurrentNightRole())
	evts = append(evts, walkRestOfTurn(t, g)...) // detective idle -> vigilante act
	require.Equal(t, game.RoleVigilante, g.State().CurrentNightRole())
	evts = append(evts, nightAction(t, g, "vig", "town1")...) // vigilante shoots town1
	evts = append(evts, walkRestOfTurn(t, g)...)              // vig submit -> doctor turn (phantom)
	require.Equal(t, game.RoleDoctor, g.State().CurrentNightRole())
	require.Equal(t, game.NightSubPonder, g.State().CurrentNightSubPhase(),
		"a recruited doctor's turn is phantom — no save window")
	evts = append(evts, finishNight(t, g)...)

	require.False(t, livingByID(g, "town1"),
		"the recruited doctor could not save, so the vigilante's shot lands")
	require.Equal(t, game.RoleMafia, roleByID(g, "doc"), "the doctor was converted")
	rec := findAllEvents[game.Recruited](evts)
	require.Len(t, rec, 1, "the recruited doctor gets exactly one Recruited notice")
	require.Equal(t, game.PlayerID("doc"), rec[0].PlayerID)
}

// TestYakuza_DoctorSavesRecruitTargetWhichStillConverts: a doctor save on the
// recruit target cancels a vigilante shot on them (so they survive) — and the
// recruit still converts the survivor. The cancelled shot still spends the
// bullet. Resolution order: resolvePhase (save cancels the shot) runs before
// resolveRecruit (which converts the still-living target).
func TestYakuza_DoctorSavesRecruitTargetWhichStillConverts(t *testing.T) {
	g := fixedRosterWithYakuzaAndVigilante(t)
	recruit(t, g, "yak", "town1") // recruit town1
	walkRestOfTurn(t, g)          // -> detective act
	walkRestOfTurn(t, g)          // -> vigilante act
	require.Equal(t, game.RoleVigilante, g.State().CurrentNightRole())
	nightAction(t, g, "vig", "town1") // vigilante shoots town1
	walkRestOfTurn(t, g)              // -> doctor act
	require.Equal(t, game.RoleDoctor, g.State().CurrentNightRole())
	nightAction(t, g, "doc", "town1") // doctor saves town1
	finishNight(t, g)

	require.True(t, livingByID(g, "town1"), "the doctor saved town1 from the vigilante")
	require.Equal(t, game.RoleMafia, roleByID(g, "town1"), "the survivor is still converted")
	require.True(t, g.State().VigilanteShotUsed(), "the cancelled shot still spends the bullet")
	require.False(t, livingByID(g, "yak"), "the Yakuza sacrificed itself")
}

// TestYakuza_ConvertActsAsMafiaNextNight: the convert is a functioning
// mafioso, not just a relabeled survivor — the night after conversion it
// submits the faction kill, which lands.
func TestYakuza_ConvertActsAsMafiaNextNight(t *testing.T) {
	g := fixedRosterWithYakuza(t)
	recruit(t, g, "yak", "town1") // night 1: recruit town1
	finishNight(t, g)
	require.Equal(t, game.RoleMafia, roleByID(g, "town1"), "town1 is now a mafioso")
	require.Equal(t, game.PhaseDayDiscussion, g.State().Phase())
	noLynchDay(t, g)

	// Night 2: the convert submits the faction kill.
	_, err := g.Apply(game.BeginNight{})
	require.NoError(t, err)
	advanceToMafiaAct(t, g)
	evts := nightAction(t, g, "town1", "town2") // the convert kills
	evts = append(evts, finishNight(t, g)...)
	killed, ok := findEvent[game.PlayerKilled](evts)
	require.True(t, ok, "the convert's faction kill lands")
	require.Equal(t, game.PlayerID("town2"), killed.PlayerID)
	require.False(t, livingByID(g, "town2"))
}

// TestYakuza_RecruitCanTripMafiaParityWin: a recruit keeps the mafia count
// flat while dropping the town by one, so it can be the move that pushes the
// strict mafia past parity into a win. checkWin runs in resolveDeathsAndMaybeEnd
// right after the recruit resolves.
func TestYakuza_RecruitCanTripMafiaParityWin(t *testing.T) {
	g := fixedRosterWithYakuza(t)
	// Whittle the town down to parity via two faction kills (det, then doc).
	playNight(t, g, map[game.Role]game.PlayerID{game.RoleMafia: "det"}) // 2 mafia vs 3 town
	require.Equal(t, game.PhaseDayDiscussion, g.State().Phase())
	noLynchDay(t, g)
	_, err := g.Apply(game.BeginNight{})
	require.NoError(t, err)
	advanceToMafiaAct(t, g)
	playNight(t, g, map[game.Role]game.PlayerID{game.RoleMafia: "doc"}) // 2 mafia vs 2 town
	require.Equal(t, game.PhaseDayDiscussion, g.State().Phase())
	noLynchDay(t, g)
	_, err = g.Apply(game.BeginNight{})
	require.NoError(t, err)
	advanceToMafiaAct(t, g)

	// The recruit converts town1 and sacrifices the Yakuza: mafia stays at 2
	// while the town drops to 1 (town2), so the mafia strictly outnumber it.
	evts := recruit(t, g, "yak", "town1")
	evts = append(evts, finishNight(t, g)...)
	end, ok := findEvent[game.GameEnded](evts)
	require.True(t, ok, "the recruit drops the town below parity -> mafia win")
	require.Equal(t, game.FactionMafia, end.Winner)
	require.Equal(t, game.PhaseEnded, g.State().Phase())
}

// TestYakuza_RecruitsVigilantePreservesBulletAndConverts: recruiting the
// Vigilante phantoms its turn (no shot), so its one bullet is never spent, and
// it is converted to RoleMafia (its night turn then phantoms for good, since
// no living RoleVigilante remains).
func TestYakuza_RecruitsVigilantePreservesBulletAndConverts(t *testing.T) {
	g := fixedRosterWithYakuzaAndVigilante(t)
	var evts []game.Event
	evts = append(evts, recruit(t, g, "yak", "vig")...) // recruit the vigilante
	evts = append(evts, walkRestOfTurn(t, g)...)        // -> detective act
	evts = append(evts, walkRestOfTurn(t, g)...)        // -> vigilante turn (phantom: recruited)
	require.Equal(t, game.RoleVigilante, g.State().CurrentNightRole())
	require.Equal(t, game.NightSubPonder, g.State().CurrentNightSubPhase(),
		"a recruited vigilante's turn is phantom — no shot window")
	evts = append(evts, finishNight(t, g)...)

	require.Equal(t, game.RoleMafia, roleByID(g, "vig"), "the vigilante was converted")
	require.False(t, g.State().VigilanteShotUsed(),
		"a recruited vigilante never fires — its bullet is preserved")
	rec := findAllEvents[game.Recruited](evts)
	require.Len(t, rec, 1)
	require.Equal(t, game.PlayerID("vig"), rec[0].PlayerID)
	require.False(t, livingByID(g, "yak"))
}

// TestYakuza_RecruitRejectedOutsideMafiaActWindow: the recruit is valid ONLY
// during the Mafia turn's act window against a living player — an unknown/empty
// target, the wrong turn, and a day phase are all rejected.
func TestYakuza_RecruitRejectedOutsideMafiaActWindow(t *testing.T) {
	g := fixedRosterWithYakuza(t)
	// Unknown / empty target during the mafia act window.
	_, err := g.Apply(game.Recruit{Actor: "yak", Target: "nobody"})
	require.ErrorIs(t, err, game.ErrUnknownPlayer)
	_, err = g.Apply(game.Recruit{Actor: "yak", Target: ""})
	require.ErrorIs(t, err, game.ErrUnknownPlayer)

	// Wrong turn: walk past the mafia turn to the detective's act window.
	walkRestOfTurn(t, g)
	require.Equal(t, game.RoleDetective, g.State().CurrentNightRole())
	_, err = g.Apply(game.Recruit{Actor: "yak", Target: "town1"})
	require.ErrorIs(t, err, game.ErrNotYourTurn)

	// Wrong phase: a recruit attempted in the day is rejected.
	finishNight(t, g)
	require.Equal(t, game.PhaseDayDiscussion, g.State().Phase())
	_, err = g.Apply(game.Recruit{Actor: "yak", Target: "town1"})
	require.ErrorIs(t, err, game.ErrWrongPhase)
}

// TestYakuza_CannotRecruitDeadTarget: a dead player is not a legal recruit
// target (requireLivingPlayer rejects it).
func TestYakuza_CannotRecruitDeadTarget(t *testing.T) {
	g := fixedRosterWithYakuza(t)
	playNight(t, g, map[game.Role]game.PlayerID{game.RoleMafia: "town2"}) // kill town2
	require.False(t, livingByID(g, "town2"))
	noLynchDay(t, g)
	_, err := g.Apply(game.BeginNight{})
	require.NoError(t, err)
	advanceToMafiaAct(t, g)
	_, err = g.Apply(game.Recruit{Actor: "yak", Target: "town2"}) // town2 is dead
	require.ErrorIs(t, err, game.ErrPlayerDead)
}

// TestYakuza_ConsortBlockOnYakuzaHasNoEffect: the Yakuza is faction-immune to
// the Consort block (and acts earlier, in the Mafia turn), so distracting it
// neither stops the recruit nor sends it a Blocked notice.
func TestYakuza_ConsortBlockOnYakuzaHasNoEffect(t *testing.T) {
	g := fixedRosterWithYakuzaAndConsort(t)
	var evts []game.Event
	evts = append(evts, recruit(t, g, "yak", "town1")...) // yakuza recruits in the mafia turn
	evts = append(evts, walkRestOfTurn(t, g)...)          // -> consort act
	require.Equal(t, game.RoleConsort, g.State().CurrentNightRole())
	evts = append(evts, nightAction(t, g, "cons", "yak")...) // consort distracts the Yakuza (no-op)
	evts = append(evts, finishNight(t, g)...)

	require.Equal(t, game.RoleMafia, roleByID(g, "town1"), "the recruit still converts")
	require.False(t, livingByID(g, "yak"), "the Yakuza still sacrifices itself")
	for _, b := range findAllEvents[game.Blocked](evts) {
		require.NotEqual(t, game.PlayerID("yak"), b.PlayerID,
			"the Yakuza is faction-immune — no Blocked notice")
	}
}
