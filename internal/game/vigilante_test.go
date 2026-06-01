package game_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/96malhar/mafia-the-game/internal/game"
)

// --- vigilante fixtures ---------------------------------------------------

// fixedRosterWithVigilante builds a deterministic 6-player game with the
// optional Vigilante enabled (Consort off), mapping each player ID to a
// fixed role:
//
//	mafia1 -> RoleMafia
//	det    -> RoleDetective
//	doc    -> RoleDoctor
//	vig    -> RoleVigilante
//	town1  -> RoleVillager
//	town2  -> RoleVillager
//
// The vigilante consumes one villager slot. On return the game is in
// PhaseNight sitting on the MAFIA's act window. The night turn order with
// a vigilante present is Mafia -> Detective -> Doctor -> Vigilante (the
// vigilante wakes last).
func fixedRosterWithVigilante(t *testing.T) *game.Game {
	t.Helper()
	return fixedRosterMatching(t, rosterDeal{
		ids: []game.PlayerID{"mafia1", "det", "doc", "vig", "town1", "town2"},
		wanted: map[game.PlayerID]game.Role{
			"mafia1": game.RoleMafia,
			"det":    game.RoleDetective,
			"doc":    game.RoleDoctor,
			"vig":    game.RoleVigilante,
			"town1":  game.RoleVillager,
			"town2":  game.RoleVillager,
		},
		mafiaCount: 1,
		vigilante:  true,
		maxSeeds:   5000,
	})
}

// runVigilanteNightToDay walks the night with a vigilante in the queue
// (Mafia -> Detective -> Doctor -> Vigilante). See runNightToDay.
func runVigilanteNightToDay(t *testing.T, g *game.Game, actions map[game.Role]game.PlayerID) []game.Event {
	t.Helper()
	return runNightToDay(t, g,
		[]game.Role{game.RoleMafia, game.RoleDetective, game.RoleDoctor, game.RoleVigilante},
		actions)
}

// --- roster + toggle ------------------------------------------------------

func TestVigilante_SetVigilanteToggle(t *testing.T) {
	g := game.New()
	_, err := g.Apply(game.CreateGame{
		GameID: "g1", MinPlayers: 6, MaxPlayers: 20, MafiaCount: 1, Seed: 0,
	})
	require.NoError(t, err)

	// Default off: setting off again is a no-op.
	_, err = g.Apply(game.SetVigilante{Enabled: false})
	require.ErrorIs(t, err, game.ErrNoChange)
	require.False(t, g.State().VigilanteEnabled())

	// Enable: emits VigilanteChanged{true}, flips state.
	evts, err := g.Apply(game.SetVigilante{Enabled: true})
	require.NoError(t, err)
	vc, ok := findEvent[game.VigilanteChanged](evts)
	require.True(t, ok, "enabling emits VigilanteChanged")
	require.True(t, vc.Enabled)
	require.Equal(t, "public", vc.Visibility().Audience,
		"the composition change is public (it does NOT reveal who)")
	require.True(t, g.State().VigilanteEnabled())

	// Re-enable is a no-op.
	_, err = g.Apply(game.SetVigilante{Enabled: true})
	require.ErrorIs(t, err, game.ErrNoChange)

	// Disable works.
	evts, err = g.Apply(game.SetVigilante{Enabled: false})
	require.NoError(t, err)
	vc, ok = findEvent[game.VigilanteChanged](evts)
	require.True(t, ok)
	require.False(t, vc.Enabled)
	require.False(t, g.State().VigilanteEnabled())
}

func TestVigilante_SetVigilanteLockedAfterDeal(t *testing.T) {
	g := game.New()
	_, err := g.Apply(game.CreateGame{
		GameID: "g1", MinPlayers: 6, MaxPlayers: 20, MafiaCount: 1, Seed: 0,
	})
	require.NoError(t, err)
	addPlayers(t, g, "a", "b", "c", "d", "e", "f")
	_, err = g.Apply(game.StartGame{})
	require.NoError(t, err)

	_, err = g.Apply(game.SetVigilante{Enabled: true})
	require.ErrorIs(t, err, game.ErrWrongPhase,
		"the vigilante toggle is locked once roles are dealt")
}

func TestVigilante_RosterComposition(t *testing.T) {
	// Enabling the vigilante deals exactly one RoleVigilante, taking the
	// slot of a villager. The vigilante is town-aligned and so does NOT
	// appear in the mafia roster.
	g := game.New()
	_, err := g.Apply(game.CreateGame{
		GameID: "g1", MinPlayers: 6, MaxPlayers: 20, MafiaCount: 1, Seed: 0,
	})
	require.NoError(t, err)
	_, err = g.Apply(game.SetVigilante{Enabled: true})
	require.NoError(t, err)
	addPlayers(t, g, "a", "b", "c", "d", "e", "f")
	start, err := g.Apply(game.StartGame{})
	require.NoError(t, err)

	counts := map[game.Role]int{}
	roleByID := map[game.PlayerID]game.Role{}
	for _, p := range g.State().Players() {
		counts[p.Role()]++
		roleByID[p.ID()] = p.Role()
	}
	require.Equal(t, 1, counts[game.RoleVigilante], "exactly one vigilante dealt")
	require.Equal(t, 1, counts[game.RoleMafia])
	require.Equal(t, 1, counts[game.RoleDetective])
	require.Equal(t, 1, counts[game.RoleDoctor])
	require.Equal(t, 2, counts[game.RoleVillager],
		"vigilante took a villager slot (6 - mafia - det - doc - vigilante = 2)")
	require.Equal(t, game.FactionTown, game.RoleVigilante.Faction(),
		"the vigilante is town-aligned")

	roster, ok := findEvent[game.MafiaRosterRevealed](start)
	require.True(t, ok)
	for _, m := range roster.Members {
		require.NotEqual(t, game.RoleVigilante, roleByID[m],
			"the vigilante must NEVER appear in the mafia roster")
	}
}

func TestVigilante_StackedOptionalRolesRejectedWhenNoVillagerSlot(t *testing.T) {
	// With both optional roles on and the mafia count at its cap, the
	// requested composition leaves no villager slots and would produce a
	// roster shorter than the player count. StartGame must reject it.
	g := game.New()
	_, err := g.Apply(game.CreateGame{
		GameID: "g1", MinPlayers: 6, MaxPlayers: 6, MafiaCount: 3, Seed: 0,
	})
	require.NoError(t, err)
	_, err = g.Apply(game.SetConsort{Enabled: true})
	require.NoError(t, err)
	_, err = g.Apply(game.SetVigilante{Enabled: true})
	require.NoError(t, err)
	addPlayers(t, g, "a", "b", "c", "d", "e", "f")
	// 6 - 3 mafia - 2 reserved - 2 optional = -1 villager slots.
	_, err = g.Apply(game.StartGame{})
	require.ErrorIs(t, err, game.ErrRosterMismatch,
		"stacked optional roles can't overflow the roster")
}

// --- night turn order -----------------------------------------------------

func TestVigilante_WakesLastAfterDoctor(t *testing.T) {
	g := fixedRosterWithVigilante(t)
	require.Equal(t, game.RoleMafia, g.State().CurrentNightRole())

	walkRestOfTurn(t, g) // mafia -> detective
	require.Equal(t, game.RoleDetective, g.State().CurrentNightRole())
	walkRestOfTurn(t, g) // detective -> doctor
	require.Equal(t, game.RoleDoctor, g.State().CurrentNightRole())
	walkRestOfTurn(t, g) // doctor -> vigilante
	require.Equal(t, game.RoleVigilante, g.State().CurrentNightRole(),
		"the vigilante wakes last, after the doctor")
	require.Equal(t, game.NightSubAct, g.State().CurrentNightSubPhase())
}

// --- basic kill -----------------------------------------------------------

func TestVigilante_ShotKillsTarget(t *testing.T) {
	g := fixedRosterWithVigilante(t)
	evts := runVigilanteNightToDay(t, g, map[game.Role]game.PlayerID{
		game.RoleVigilante: "town1",
	})
	killed, ok := findEvent[game.PlayerKilled](evts)
	require.True(t, ok, "the vigilante's shot kills its target")
	require.Equal(t, game.PlayerID("town1"), killed.PlayerID)
	require.False(t, livingByID(g, "town1"), "town1 should be dead")
}

func TestVigilante_DetectiveReadsVigilanteAsNotMafia(t *testing.T) {
	g := fixedRosterWithVigilante(t)
	walkRestOfTurn(t, g) // mafia -> detective
	require.Equal(t, game.RoleDetective, g.State().CurrentNightRole())

	evts := nightAction(t, g, "det", "vig")
	res, ok := findEvent[game.DetectiveResult](evts)
	require.True(t, ok)
	require.Equal(t, game.PlayerID("vig"), res.Target)
	require.False(t, res.IsMafia, "the vigilante is town-aligned — reads as NOT mafia")
}

// --- one-shot enforcement -------------------------------------------------

func TestVigilante_OneShotSecondNightRejected(t *testing.T) {
	// Night 1: the vigilante shoots town1 (bullet spent). Night 2: with
	// the one bullet gone the vigilante has nothing to do, so his turn
	// runs as a phantom — he wakes for cadence/secrecy but the act window
	// is skipped (narrate → ponder), exactly like a dead role. He never
	// gets an act window to shoot from, and a bypassing submission is
	// rejected as "not your turn".
	g := fixedRosterWithVigilante(t)
	runVigilanteNightToDay(t, g, map[game.Role]game.PlayerID{
		game.RoleVigilante: "town1",
	})
	require.Equal(t, game.PhaseDayDiscussion, g.State().Phase())

	// Quiet day, then night 2.
	noLynchDay(t, g)
	beginNightToMafiaAct(t, g)   // -> mafia act window
	walkRestOfTurn(t, g)         // mafia -> detective
	walkRestOfTurn(t, g)         // detective -> doctor
	evts := walkRestOfTurn(t, g) // doctor -> vigilante (now phantom)

	require.Equal(t, game.RoleVigilante, g.State().CurrentNightRole())
	require.Equal(t, game.NightSubPonder, g.State().CurrentNightSubPhase(),
		"a spent vigilante's turn skips the act window (phantom ponder)")

	narrate, ok := findNightSub(evts, game.NightSubNarrate)
	require.True(t, ok, "the vigilante still narrates for cadence")
	require.Equal(t, game.RoleVigilante, narrate.Role)
	require.True(t, narrate.Phantom,
		"a spent-but-alive vigilante narrates as phantom (no act window)")

	// No act window ever opens, so a bypassing client submission is
	// rejected as wrong-time, not accepted.
	_, err := g.Apply(game.NightAction{Actor: "vig", Target: "town2"})
	require.ErrorIs(t, err, game.ErrNotYourTurn,
		"the vigilante's single bullet is spent — no act window to shoot from")
}

func TestVigilante_CannotSelfTarget(t *testing.T) {
	g := fixedRosterWithVigilante(t)
	walkRestOfTurn(t, g) // mafia -> detective
	walkRestOfTurn(t, g) // detective -> doctor
	walkRestOfTurn(t, g) // doctor -> vigilante
	require.Equal(t, game.RoleVigilante, g.State().CurrentNightRole())

	_, err := g.Apply(game.NightAction{Actor: "vig", Target: "vig"})
	require.ErrorIs(t, err, game.ErrSelfTarget, "the vigilante cannot shoot himself")
}

// --- rule 2: mafia precedence ---------------------------------------------

func TestVigilante_MafiaKillTakesPrecedenceOverVigilante(t *testing.T) {
	// Rule 2: mafia targets the vigilante, the vigilante targets the
	// mafia. The mafia kill resolves first, so the vigilante dies and his
	// shot never lands — the mafia member survives.
	g := fixedRosterWithVigilante(t)
	evts := runVigilanteNightToDay(t, g, map[game.Role]game.PlayerID{
		game.RoleMafia:     "vig",
		game.RoleVigilante: "mafia1",
	})

	require.False(t, livingByID(g, "vig"), "the vigilante dies to the mafia kill")
	require.True(t, livingByID(g, "mafia1"),
		"the mafia survives — the dead vigilante's shot never lands")

	// Exactly one death, and it's the vigilante.
	kills := findAllEvents[game.PlayerKilled](evts)
	require.Len(t, kills, 1, "only the mafia kill lands")
	require.Equal(t, game.PlayerID("vig"), kills[0].PlayerID)
}

// --- rule 3: doctor saves the vigilante -----------------------------------

func TestVigilante_DoctorSavesVigilanteSoShotLands(t *testing.T) {
	// Rule 3: mafia targets the vigilante, the doctor saves the
	// vigilante, the vigilante targets the mafia. The save cancels the
	// mafia kill, so the vigilante lives and his shot lands — the mafia
	// dies (which here ends the game as a town win).
	g := fixedRosterWithVigilante(t)
	evts := runVigilanteNightToDay(t, g, map[game.Role]game.PlayerID{
		game.RoleMafia:     "vig",
		game.RoleDoctor:    "vig",
		game.RoleVigilante: "mafia1",
	})

	saved, ok := findEvent[game.PlayerSaved](evts)
	require.True(t, ok, "the doctor's save of the vigilante is recorded")
	require.Equal(t, game.PlayerID("vig"), saved.PlayerID)

	killed, ok := findEvent[game.PlayerKilled](evts)
	require.True(t, ok, "the saved vigilante's shot lands on the mafia")
	require.Equal(t, game.PlayerID("mafia1"), killed.PlayerID)

	require.True(t, livingByID(g, "vig"), "the saved vigilante survives")
	require.False(t, livingByID(g, "mafia1"), "the mafia dies to the vigilante shot")
}

// --- rule 4: vigilante's target saved -> wasted bullet --------------------

func TestVigilante_TargetSavedWastesBulletButSpendsIt(t *testing.T) {
	// Rule 4: the doctor saves the vigilante's target, so the shot does
	// not land — but the bullet is still spent. We verify both: no kill
	// this night, and a second shot the next night is rejected.
	g := fixedRosterWithVigilante(t)
	evts := runVigilanteNightToDay(t, g, map[game.Role]game.PlayerID{
		game.RoleDoctor:    "town1",
		game.RoleVigilante: "town1",
	})

	saved, ok := findEvent[game.PlayerSaved](evts)
	require.True(t, ok, "the doctor's save of the vigilante's target is recorded")
	require.Equal(t, game.PlayerID("town1"), saved.PlayerID)
	_, killed := findEvent[game.PlayerKilled](evts)
	require.False(t, killed, "the saved target does not die")
	require.True(t, livingByID(g, "town1"), "town1 survives the wasted shot")
	require.Equal(t, game.PhaseDayDiscussion, g.State().Phase())

	// Bullet is spent even though the shot was wasted: next night the
	// vigilante's turn is phantom (no act window), so he can't shoot
	// again.
	noLynchDay(t, g)
	beginNightToMafiaAct(t, g)
	walkRestOfTurn(t, g)          // mafia -> detective
	walkRestOfTurn(t, g)          // detective -> doctor
	evts2 := walkRestOfTurn(t, g) // doctor -> vigilante (now phantom)
	require.Equal(t, game.RoleVigilante, g.State().CurrentNightRole())
	require.Equal(t, game.NightSubPonder, g.State().CurrentNightSubPhase(),
		"a wasted shot still spends the bullet — the next turn is phantom")
	narrate, ok := findNightSub(evts2, game.NightSubNarrate)
	require.True(t, ok)
	require.True(t, narrate.Phantom, "spent vigilante narrates as phantom")
	// The ponder substituted for the (skipped) act window must ALSO carry
	// Phantom=true: that's the flag the room reads to size this beat with
	// the randomized phantom timer instead of stalling on a 60s act window
	// the spent vigilante can't use.
	// The ponder substituted for the (skipped) act window must ALSO carry
	// Phantom=true: that's the flag the room reads to size this beat with
	// the randomized phantom timer instead of stalling on a 60s act window
	// the spent vigilante can't use. (Filter by role: a real role that
	// timed out earlier in evts2 also passes through a non-phantom ponder
	// for uniform cadence, so the first ponder isn't the vigilante's.)
	var vigPonder game.NightSubPhaseStarted
	var found bool
	for _, e := range evts2 {
		if ns, ok := e.(game.NightSubPhaseStarted); ok &&
			ns.Sub == game.NightSubPonder && ns.Role == game.RoleVigilante {
			vigPonder, found = ns, true
		}
	}
	require.True(t, found, "the spent vigilante reaches a ponder sub-phase")
	require.True(t, vigPonder.Phantom, "spent vigilante's ponder is phantom-substituted")

	_, err := g.Apply(game.NightAction{Actor: "vig", Target: "town2"})
	require.ErrorIs(t, err, game.ErrNotYourTurn,
		"a wasted shot still spends the one bullet — no act window")
}

// --- mafia + vigilante hit the same third player --------------------------

func TestVigilante_SameTargetAsMafiaYieldsOneDeath(t *testing.T) {
	// Both the mafia and the vigilante shoot town1. Only one death lands
	// (a single PlayerKilled), not a duplicate.
	g := fixedRosterWithVigilante(t)
	evts := runVigilanteNightToDay(t, g, map[game.Role]game.PlayerID{
		game.RoleMafia:     "town1",
		game.RoleVigilante: "town1",
	})

	kills := findAllEvents[game.PlayerKilled](evts)
	require.Len(t, kills, 1, "two shots at the same target produce exactly one death")
	require.Equal(t, game.PlayerID("town1"), kills[0].PlayerID)
	require.False(t, livingByID(g, "town1"))
}

// --- mafia + vigilante hit two different players --------------------------

func TestVigilante_DifferentTargetsYieldTwoDeaths(t *testing.T) {
	// The mafia shoots town1 and the vigilante shoots town2 — two
	// distinct, living, un-saved players. resolveNight runs the mafia
	// kill first and the (still-alive) vigilante's shot second, each on
	// its own target, so exactly TWO deaths are recorded.
	g := fixedRosterWithVigilante(t)
	evts := runVigilanteNightToDay(t, g, map[game.Role]game.PlayerID{
		game.RoleMafia:     "town1",
		game.RoleVigilante: "town2",
	})

	kills := findAllEvents[game.PlayerKilled](evts)
	require.Len(t, kills, 2, "two distinct targets produce two deaths")
	require.ElementsMatch(t,
		[]game.PlayerID{"town1", "town2"},
		[]game.PlayerID{kills[0].PlayerID, kills[1].PlayerID},
		"the mafia's and the vigilante's separate targets both die")
	require.False(t, livingByID(g, "town1"), "the mafia's target dies")
	require.False(t, livingByID(g, "town2"), "the vigilante's target dies")
}

// --- rule 1: consort blocks the vigilante ---------------------------------

// fixedRosterWithConsortAndVigilante builds a deterministic 6-player game
// with BOTH optional roles enabled:
//
//	mafia1 -> RoleMafia
//	consort-> RoleConsort
//	det    -> RoleDetective
//	doc    -> RoleDoctor
//	vig    -> RoleVigilante
//	town1  -> RoleVillager
//
// Night turn order is Mafia -> Consort -> Detective -> Doctor -> Vigilante.
func fixedRosterWithConsortAndVigilante(t *testing.T) *game.Game {
	t.Helper()
	return fixedRosterMatching(t, rosterDeal{
		ids: []game.PlayerID{"mafia1", "consort", "det", "doc", "vig", "town1"},
		wanted: map[game.PlayerID]game.Role{
			"mafia1":  game.RoleMafia,
			"consort": game.RoleConsort,
			"det":     game.RoleDetective,
			"doc":     game.RoleDoctor,
			"vig":     game.RoleVigilante,
			"town1":   game.RoleVillager,
		},
		mafiaCount: 1,
		consort:    true,
		vigilante:  true,
		maxSeeds:   20000,
	})
}

func TestVigilante_BlockedByConsortDoesNotSpendBullet(t *testing.T) {
	// Rule 1: the consort blocks the vigilante. His turn is phantom (no
	// act window), so nothing is recorded — no kill lands AND the bullet
	// is preserved, letting him fire on a later night.
	g := fixedRosterWithConsortAndVigilante(t)

	// Mafia idles; consort blocks the vigilante.
	walkRestOfTurn(t, g) // mafia -> consort
	require.Equal(t, game.RoleConsort, g.State().CurrentNightRole())
	nightAction(t, g, "consort", "vig")
	walkRestOfTurn(t, g) // consort -> detective
	walkRestOfTurn(t, g) // detective -> doctor
	walkRestOfTurn(t, g) // doctor -> vigilante (blocked => phantom)
	require.Equal(t, game.RoleVigilante, g.State().CurrentNightRole())
	require.Equal(t, game.NightSubPonder, g.State().CurrentNightSubPhase(),
		"a blocked vigilante's turn is phantom — no act window")

	// The blocked vigilante has no act window; a bypassing submit is rejected.
	_, err := g.Apply(game.NightAction{Actor: "vig", Target: "town1"})
	require.ErrorIs(t, err, game.ErrNotYourTurn, "a blocked vigilante has no act window")

	// Finish the night: nobody dies.
	evts := finishNight(t, g)
	_, killed := findEvent[game.PlayerKilled](evts)
	require.False(t, killed, "a blocked vigilante kills no one")
	require.True(t, livingByID(g, "town1"))

	// The bullet was NOT spent: next night the vigilante can fire.
	noLynchDay(t, g)
	beginNightToMafiaAct(t, g)
	walkRestOfTurn(t, g) // mafia -> consort
	walkRestOfTurn(t, g) // consort -> detective
	walkRestOfTurn(t, g) // detective -> doctor
	walkRestOfTurn(t, g) // doctor -> vigilante
	require.Equal(t, game.RoleVigilante, g.State().CurrentNightRole())
	require.Equal(t, game.NightSubAct, g.State().CurrentNightSubPhase())
	evts2 := nightAction(t, g, "vig", "town1")
	rec, ok := findEvent[game.NightActionRecorded](evts2)
	require.True(t, ok, "a vigilante whose earlier shot was blocked keeps his bullet")
	require.Equal(t, game.PlayerID("town1"), rec.Target)
}

// --- hold fire (NightPass) ------------------------------------------------

func TestVigilante_HoldFireEndsTurnWithoutSpendingBullet(t *testing.T) {
	// The vigilante may decline to act ("hold fire"). NightPass ends his
	// act window early — advancing straight to ponder, exactly like a fast
	// timeout — WITHOUT recording a shot or spending the bullet, so he can
	// still fire on a later night.
	g := fixedRosterWithVigilante(t)
	walkRestOfTurn(t, g) // mafia -> detective
	walkRestOfTurn(t, g) // detective -> doctor
	walkRestOfTurn(t, g) // doctor -> vigilante
	require.Equal(t, game.RoleVigilante, g.State().CurrentNightRole())
	require.Equal(t, game.NightSubAct, g.State().CurrentNightSubPhase())

	evts, err := g.Apply(game.NightPass{Actor: "vig"})
	require.NoError(t, err, "the vigilante may hold fire during his own act window")

	// Advances straight to ponder, recording nothing.
	require.Equal(t, game.NightSubPonder, g.State().CurrentNightSubPhase(),
		"holding fire ends the act window early (act -> ponder)")
	ponder, ok := findNightSub(evts, game.NightSubPonder)
	require.True(t, ok)
	require.Equal(t, game.RoleVigilante, ponder.Role)
	require.False(t, ponder.Phantom,
		"a real turn that held fire is NOT phantom (he was present and able)")
	_, recorded := findEvent[game.NightActionRecorded](evts)
	require.False(t, recorded, "holding fire records no night action")

	// A second pass now is rejected — the act window is already closed.
	_, err = g.Apply(game.NightPass{Actor: "vig"})
	require.ErrorIs(t, err, game.ErrNotYourTurn,
		"once the turn advanced to ponder there is no act window to pass on")

	// Finish the night: nobody dies.
	evts = finishNight(t, g)
	_, killed := findEvent[game.PlayerKilled](evts)
	require.False(t, killed, "holding fire kills no one")
	require.Equal(t, game.PhaseDayDiscussion, g.State().Phase())

	// The bullet is preserved: next night the vigilante gets a live act
	// window and can fire.
	noLynchDay(t, g)
	beginNightToMafiaAct(t, g)
	walkRestOfTurn(t, g) // mafia -> detective
	walkRestOfTurn(t, g) // detective -> doctor
	walkRestOfTurn(t, g) // doctor -> vigilante
	require.Equal(t, game.RoleVigilante, g.State().CurrentNightRole())
	require.Equal(t, game.NightSubAct, g.State().CurrentNightSubPhase(),
		"a vigilante who held fire keeps a live act window on later nights")
	evts2 := nightAction(t, g, "vig", "town1")
	rec, ok := findEvent[game.NightActionRecorded](evts2)
	require.True(t, ok, "the preserved bullet fires on a later night")
	require.Equal(t, game.PlayerID("town1"), rec.Target)
}

func TestVigilante_HoldFireRejectedWhenNotHisTurn(t *testing.T) {
	// Holding fire is only valid during the vigilante's OWN act window.
	// On another role's turn it's rejected as wrong-time.
	g := fixedRosterWithVigilante(t)
	require.Equal(t, game.RoleMafia, g.State().CurrentNightRole())

	_, err := g.Apply(game.NightPass{Actor: "vig"})
	require.ErrorIs(t, err, game.ErrNotYourTurn,
		"the vigilante cannot hold fire during another role's turn")
}

func TestNightPass_RejectedForRolesWithoutAllowPass(t *testing.T) {
	// NightPass is opt-in per role (AllowPass). Only the vigilante exposes
	// it today; every other role — including those WITH a night action —
	// rejects it with ErrNotYourAction, even during their own act window.
	g := fixedRosterWithVigilante(t)

	// Mafia's act window: mafia has a night action but no pass affordance.
	require.Equal(t, game.RoleMafia, g.State().CurrentNightRole())
	require.Equal(t, game.NightSubAct, g.State().CurrentNightSubPhase())
	_, err := g.Apply(game.NightPass{Actor: "mafia1"})
	require.ErrorIs(t, err, game.ErrNotYourAction, "mafia cannot pass via NightPass")

	// A villager has no night action at all — also rejected.
	_, err = g.Apply(game.NightPass{Actor: "town1"})
	require.ErrorIs(t, err, game.ErrNotYourAction, "a villager has no action to pass on")

	// The detective likewise can't pass, even on his own turn.
	walkRestOfTurn(t, g) // mafia -> detective
	require.Equal(t, game.RoleDetective, g.State().CurrentNightRole())
	require.Equal(t, game.NightSubAct, g.State().CurrentNightSubPhase())
	_, err = g.Apply(game.NightPass{Actor: "det"})
	require.ErrorIs(t, err, game.ErrNotYourAction, "detective cannot pass via NightPass")
}
