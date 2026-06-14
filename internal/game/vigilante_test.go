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
// a vigilante present is Mafia -> Detective -> Vigilante -> Doctor (the
// doctor wakes last, after both night-killers).
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
// (Mafia -> Detective -> Vigilante -> Doctor). See runNightToDay.
func runVigilanteNightToDay(t *testing.T, g *game.Game, actions map[game.Role]game.PlayerID) []game.Event {
	t.Helper()
	return runNightToDay(t, g,
		[]game.Role{game.RoleMafia, game.RoleDetective, game.RoleVigilante, game.RoleDoctor},
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

// Enabling the vigilante deals exactly one RoleVigilante, taking the
// slot of a villager. The vigilante is town-aligned and so does NOT
// appear in the mafia roster.
func TestVigilante_RosterComposition(t *testing.T) {
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

// With both optional roles on and the mafia count at its cap, the
// requested composition leaves no villager slots and would produce a
// roster shorter than the player count. StartGame must reject it.
func TestVigilante_StackedOptionalRolesRejectedWhenNoVillagerSlot(t *testing.T) {
	g := game.New()
	_, err := g.Apply(game.CreateGame{
		GameID: "g1", MinPlayers: 5, MaxPlayers: 6, MafiaCount: 1, Seed: 0,
	})
	require.NoError(t, err)
	_, err = g.Apply(game.SetConsort{Enabled: true})
	require.NoError(t, err)
	_, err = g.Apply(game.SetVigilante{Enabled: true})
	require.NoError(t, err)
	_, err = g.Apply(game.SetTracker{Enabled: true})
	require.NoError(t, err)
	addPlayers(t, g, "a", "b", "c", "d", "e")
	// 5 - 1 mafia - 2 reserved - 3 optional = -1 villager slots: the optional
	// roles over-subscribe the seats. Town still out-numbers the mafia-aligned
	// (3 vs 2), so this isolates the composition-fit failure: ErrRosterMismatch.
	_, err = g.Apply(game.StartGame{})
	require.ErrorIs(t, err, game.ErrRosterMismatch,
		"stacked optional roles can't overflow the roster")
}

// --- night turn order -----------------------------------------------------

func TestVigilante_WakesBeforeDoctor(t *testing.T) {
	g := fixedRosterWithVigilante(t)
	require.Equal(t, game.RoleMafia, g.State().CurrentNightRole())

	walkRestOfTurn(t, g) // mafia -> detective
	require.Equal(t, game.RoleDetective, g.State().CurrentNightRole())
	walkRestOfTurn(t, g) // detective -> vigilante
	require.Equal(t, game.RoleVigilante, g.State().CurrentNightRole(),
		"the vigilante wakes after the detective")
	require.Equal(t, game.NightSubAct, g.State().CurrentNightSubPhase())
	walkRestOfTurn(t, g) // vigilante -> doctor
	require.Equal(t, game.RoleDoctor, g.State().CurrentNightRole(),
		"the doctor wakes last, after both night-killers")
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

// Night 1: the vigilante shoots town1 (bullet spent). Night 2: with
// the one bullet gone the vigilante has nothing to do, so his turn
// runs as a phantom — he wakes for cadence/secrecy but the act window
// is skipped (narrate → ponder), exactly like a dead role. He never
// gets an act window to shoot from, and a bypassing submission is
// rejected as "not your turn".
func TestVigilante_OneShotSecondNightRejected(t *testing.T) {
	g := fixedRosterWithVigilante(t)
	runVigilanteNightToDay(t, g, map[game.Role]game.PlayerID{
		game.RoleVigilante: "town1",
	})
	require.Equal(t, game.PhaseDayDiscussion, g.State().Phase())

	// Quiet day, then night 2.
	noLynchDay(t, g)
	beginNightToMafiaAct(t, g)   // -> mafia act window
	walkRestOfTurn(t, g)         // mafia -> detective
	evts := walkRestOfTurn(t, g) // detective -> vigilante (now phantom)

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
	walkRestOfTurn(t, g) // detective -> vigilante
	require.Equal(t, game.RoleVigilante, g.State().CurrentNightRole())

	_, err := g.Apply(game.NightAction{Actor: "vig", Target: "vig"})
	require.ErrorIs(t, err, game.ErrSelfTarget, "the vigilante cannot shoot himself")
}

// --- rule 2: mafia precedence ---------------------------------------------

// Rule 2: mafia targets the vigilante, the vigilante targets the
// mafia. The mafia kill resolves first, so the vigilante dies and his
// shot never lands — the mafia member survives.
func TestVigilante_MafiaKillTakesPrecedenceOverVigilante(t *testing.T) {
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

// Rule 2, town-target variant: mafia targets the vigilante, the
// vigilante targets a TOWNSPERSON (not the mafia). resolvePhase gates
// the shot on shooter.alive BEFORE it looks at the target, so the
// target's faction is irrelevant — the dead vigilante's shot still
// never lands and the townsperson survives. Same branch as the test
// above; this pins that the precedence rule is target-agnostic.
func TestVigilante_MafiaKillTakesPrecedenceTownTarget(t *testing.T) {
	g := fixedRosterWithVigilante(t)
	evts := runVigilanteNightToDay(t, g, map[game.Role]game.PlayerID{
		game.RoleMafia:     "vig",
		game.RoleVigilante: "town1",
	})

	require.False(t, livingByID(g, "vig"), "the vigilante dies to the mafia kill")
	require.True(t, livingByID(g, "town1"),
		"the townsperson survives — the dead vigilante's shot never lands")

	// Exactly one death, and it's the vigilante.
	kills := findAllEvents[game.PlayerKilled](evts)
	require.Len(t, kills, 1, "only the mafia kill lands")
	require.Equal(t, game.PlayerID("vig"), kills[0].PlayerID)
}

// --- rule 3: doctor saves the vigilante -----------------------------------

// Rule 3: mafia targets the vigilante, the doctor saves the
// vigilante, the vigilante targets the mafia. The save cancels the
// mafia kill, so the vigilante lives and his shot lands — the mafia
// dies (which here ends the game as a town win).
func TestVigilante_DoctorSavesVigilanteSoShotLands(t *testing.T) {
	g := fixedRosterWithVigilante(t)
	evts := runVigilanteNightToDay(t, g, map[game.Role]game.PlayerID{
		game.RoleMafia:     "vig",
		game.RoleDoctor:    "vig",
		game.RoleVigilante: "mafia1",
	})

	killed, ok := findEvent[game.PlayerKilled](evts)
	require.True(t, ok, "the saved vigilante's shot lands on the mafia")
	require.Equal(t, game.PlayerID("mafia1"), killed.PlayerID)

	require.True(t, livingByID(g, "vig"), "the saved vigilante survives")
	require.False(t, livingByID(g, "mafia1"), "the mafia dies to the vigilante shot")

	// The shot landed, so the one bullet is spent. (This night ends the
	// game as a town win, so there is no later night to walk to — assert
	// the bullet state directly.)
	require.True(t, g.State().VigilanteShotUsed(), "a landed shot spends the bullet")
}

// Rule 3, town-target variant: mafia targets the vigilante, the doctor
// saves the vigilante, and the vigilante shoots a TOWNSPERSON. The save
// cancels the mafia kill, so the vigilante survives — and a live
// shooter's shot DOES land, so the townsperson dies. The pair with
// TestVigilante_MafiaKillTakesPrecedenceTownTarget shows the doctor save
// is exactly what flips the same cast from "T survives" to "T dies":
// shooter survival, not the target, decides whether the shot lands.
func TestVigilante_DoctorSavesVigilanteSoTownShotLands(t *testing.T) {
	g := fixedRosterWithVigilante(t)
	evts := runVigilanteNightToDay(t, g, map[game.Role]game.PlayerID{
		game.RoleMafia:     "vig",
		game.RoleDoctor:    "vig",
		game.RoleVigilante: "town1",
	})

	require.True(t, livingByID(g, "vig"), "the saved vigilante survives")
	require.False(t, livingByID(g, "town1"),
		"the townsperson dies — the surviving vigilante's shot lands")

	// Exactly one death, and it's the townsperson (the mafia kill on the
	// vigilante was cancelled by the save).
	kills := findAllEvents[game.PlayerKilled](evts)
	require.Len(t, kills, 1, "only the vigilante's shot lands")
	require.Equal(t, game.PlayerID("town1"), kills[0].PlayerID)

	// The shot landed, so the one bullet is spent. Killing a lone
	// villager doesn't end the game (mafia still don't outnumber town),
	// so the bullet state is asserted directly here too.
	require.True(t, g.State().VigilanteShotUsed(), "a landed shot spends the bullet")
}

// --- rule 4: vigilante's target saved -> wasted bullet --------------------

// Rule 4: the doctor saves the vigilante's target, so the shot does
// not land — but the bullet is still spent. We verify both: no kill
// this night, and a second shot the next night is rejected.
func TestVigilante_TargetSavedWastesBulletButSpendsIt(t *testing.T) {
	g := fixedRosterWithVigilante(t)
	evts := runVigilanteNightToDay(t, g, map[game.Role]game.PlayerID{
		game.RoleDoctor:    "town1",
		game.RoleVigilante: "town1",
	})

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
	evts2 := walkRestOfTurn(t, g) // detective -> vigilante (now phantom)
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

// Both the mafia and the vigilante shoot town1. Only one death lands
// (a single PlayerKilled), not a duplicate.
func TestVigilante_SameTargetAsMafiaYieldsOneDeath(t *testing.T) {
	g := fixedRosterWithVigilante(t)
	evts := runVigilanteNightToDay(t, g, map[game.Role]game.PlayerID{
		game.RoleMafia:     "town1",
		game.RoleVigilante: "town1",
	})

	kills := findAllEvents[game.PlayerKilled](evts)
	require.Len(t, kills, 1, "two shots at the same target produce exactly one death")
	require.Equal(t, game.PlayerID("town1"), kills[0].PlayerID)
	require.False(t, livingByID(g, "town1"))

	// The mafia kill resolves first, so the vigilante's shot lands on an
	// already-dead target (resolveHit no-ops on the corpse) — but the bullet
	// is still SPENT. Next night the vigilante's turn is phantom (no act
	// window) and a bypassing submit is rejected.
	require.True(t, g.State().VigilanteShotUsed(),
		"a shot duplicating the mafia's kill still spends the bullet")
	noLynchDay(t, g)
	beginNightToMafiaAct(t, g)
	walkRestOfTurn(t, g) // mafia -> detective
	walkRestOfTurn(t, g) // detective -> vigilante (now phantom)
	require.Equal(t, game.RoleVigilante, g.State().CurrentNightRole())
	require.Equal(t, game.NightSubPonder, g.State().CurrentNightSubPhase(),
		"the spent bullet leaves no act window on a later night")
	_, err := g.Apply(game.NightAction{Actor: "vig", Target: "town2"})
	require.ErrorIs(t, err, game.ErrNotYourTurn)
}

// The mafia and the vigilante both shoot town1 and the doctor saves
// town1. The save cancels both shots silently, so nobody dies and no
// event is emitted — resolveHit runs twice on the saved target (once
// per killer) but each is a silent no-op. The vigilante's bullet is
// still spent.
func TestVigilante_SameTargetAsMafiaSavedByDoctorSpendsBullet(t *testing.T) {
	g := fixedRosterWithVigilante(t)
	evts := runVigilanteNightToDay(t, g, map[game.Role]game.PlayerID{
		game.RoleMafia:     "town1",
		game.RoleDoctor:    "town1",
		game.RoleVigilante: "town1",
	})

	_, killed := findEvent[game.PlayerKilled](evts)
	require.False(t, killed, "the doctor's save cancels both shots on town1")
	require.True(t, livingByID(g, "town1"), "the saved target survives")

	// The bullet is still spent even though the shot landed on the same
	// target as the mafia (and was saved): next night the vigilante's turn
	// is phantom (no act window) and a bypassing submit is rejected.
	noLynchDay(t, g)
	beginNightToMafiaAct(t, g)
	walkRestOfTurn(t, g) // mafia -> detective
	walkRestOfTurn(t, g) // detective -> vigilante (now phantom)
	require.Equal(t, game.RoleVigilante, g.State().CurrentNightRole())
	require.Equal(t, game.NightSubPonder, g.State().CurrentNightSubPhase(),
		"a shot duplicating the mafia's saved target still spends the bullet")
	_, err := g.Apply(game.NightAction{Actor: "vig", Target: "town2"})
	require.ErrorIs(t, err, game.ErrNotYourTurn,
		"the spent bullet leaves no act window on a later night")
}

// --- mafia + vigilante hit two different players --------------------------

// The mafia shoots town1 and the vigilante shoots town2 — two
// distinct, living, un-saved players. resolveNight runs the mafia
// kill first and the (still-alive) vigilante's shot second, each on
// its own target, so exactly TWO deaths are recorded.
func TestVigilante_DifferentTargetsYieldTwoDeaths(t *testing.T) {
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
// Night turn order is Mafia -> Consort -> Detective -> Vigilante -> Doctor.
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

// Rule 1: the consort blocks the vigilante. His turn is phantom (no
// act window), so nothing is recorded — no kill lands AND the bullet
// is preserved, letting him fire on a later night.
func TestVigilante_BlockedByConsortDoesNotSpendBullet(t *testing.T) {
	g := fixedRosterWithConsortAndVigilante(t)

	// Mafia idles; consort blocks the vigilante.
	walkRestOfTurn(t, g) // mafia -> consort
	require.Equal(t, game.RoleConsort, g.State().CurrentNightRole())
	nightAction(t, g, "consort", "vig")
	walkRestOfTurn(t, g) // consort -> detective
	walkRestOfTurn(t, g) // detective -> vigilante (blocked => phantom)
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
	walkRestOfTurn(t, g) // detective -> vigilante
	require.Equal(t, game.RoleVigilante, g.State().CurrentNightRole())
	require.Equal(t, game.NightSubAct, g.State().CurrentNightSubPhase())
	evts2 := nightAction(t, g, "vig", "town1")
	rec, ok := findEvent[game.NightActionRecorded](evts2)
	require.True(t, ok, "a vigilante whose earlier shot was blocked keeps his bullet")
	require.Equal(t, game.PlayerID("town1"), rec.Target)
}

// Regression: the vigilante can kill a mafioso at night, so the cabal
// can reach zero during NIGHT resolution — not only via a lynch. When
// that happens with a living consort she must be promoted on the night
// path, exactly like the lynch path, or the takeover silently fails
// (the RoleMafia turn would go phantom forever and no one inherits the
// kill). The town must NOT be handed a premature win either.
func TestVigilante_KillingLastMafiaAtNightPromotesConsort(t *testing.T) {
	g := fixedRosterWithConsortAndVigilante(t)

	// Quiet night except the vigilante shoots the lone mafioso.
	evts := runNightToDay(t, g,
		[]game.Role{game.RoleMafia, game.RoleConsort, game.RoleDetective, game.RoleVigilante, game.RoleDoctor},
		map[game.Role]game.PlayerID{game.RoleVigilante: "mafia1"})

	killed, ok := findEvent[game.PlayerKilled](evts)
	require.True(t, ok, "the vigilante's shot kills the mafioso")
	require.Equal(t, game.PlayerID("mafia1"), killed.PlayerID)
	require.False(t, livingByID(g, "mafia1"), "the mafioso is dead")

	// The night-time cabal wipe promotes the living consort (private).
	promo, ok := findEvent[game.ConsortPromoted](evts)
	require.True(t, ok, "killing the last mafia at night promotes the living consort")
	require.Equal(t, game.PlayerID("consort"), promo.PlayerID)
	require.Equal(t, game.PlayerID("consort"), promo.Visibility().Player,
		"promotion stays private — the town must not learn a sleeper took over")

	roster, ok := findEvent[game.MafiaRosterRevealed](evts)
	require.True(t, ok, "promotion re-issues the mafia roster (the full cabal)")
	require.ElementsMatch(t, []game.PlayerID{"mafia1", "consort"}, roster.Members)
	require.Equal(t, game.PlayerID(""), roster.Yakuza)

	// No premature town win, and the consort now holds RoleMafia.
	_, ended := findEvent[game.GameEnded](evts)
	require.False(t, ended, "a surviving consort keeps the mafia side alive")
	require.Equal(t, game.PhaseDayDiscussion, g.State().Phase())
	require.Equal(t, game.RoleMafia, roleByID(g, "consort"), "the consort inherits RoleMafia")

	// Pin the intra-batch ORDERING of the night-resolution pipeline
	// (resolveDeathsAndMaybeEnd): the kill resolves, THEN the consort is
	// promoted (and her fresh mafia roster issued), THEN the graveyard
	// roster is refreshed so the dead see her new faction, and only THEN
	// does the phase flip to DayDiscussion — all before any voting can
	// open. A reorder would silently break the promote-before-reveal /
	// promote-before-transition invariant that presence-only checks miss.
	// (The lone PhaseChanged in this batch is the Night→DayDiscussion
	// transition; g.State().Phase() above pins its destination.)
	requireEventOrder(t,
		orderedEvent{"PlayerKilled", indexOfEvent[game.PlayerKilled](evts)},
		orderedEvent{"ConsortPromoted", indexOfEvent[game.ConsortPromoted](evts)},
		orderedEvent{"MafiaRosterRevealed", indexOfEvent[game.MafiaRosterRevealed](evts)},
		orderedEvent{"RosterRevealed (graveyard)", indexOfEvent[game.RosterRevealed](evts)},
		orderedEvent{"PhaseChanged→DayDiscussion", indexOfEvent[game.PhaseChanged](evts)},
	)

	// And the takeover is real: next night the promoted consort carries
	// the faction kill from the RoleMafia act window.
	noLynchDay(t, g)
	beginNightToMafiaAct(t, g)
	require.Equal(t, game.RoleMafia, g.State().CurrentNightRole())
	require.Equal(t, game.NightSubAct, g.State().CurrentNightSubPhase(),
		"the promoted consort gets a live RoleMafia act window")
	evts2 := nightAction(t, g, "consort", "town1")
	rec, ok := findEvent[game.NightActionRecorded](evts2)
	require.True(t, ok, "the promoted consort can now kill")
	require.Equal(t, game.PlayerID("town1"), rec.Target)
}

// --- hold fire (NightPass) ------------------------------------------------

// The vigilante may decline to act ("hold fire"). NightPass ends his
// act window early — advancing straight to ponder, exactly like a fast
// timeout — WITHOUT recording a shot or spending the bullet, so he can
// still fire on a later night.
func TestVigilante_HoldFireEndsTurnWithoutSpendingBullet(t *testing.T) {
	g := fixedRosterWithVigilante(t)
	walkRestOfTurn(t, g) // mafia -> detective
	walkRestOfTurn(t, g) // detective -> vigilante
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
	walkRestOfTurn(t, g) // detective -> vigilante
	require.Equal(t, game.RoleVigilante, g.State().CurrentNightRole())
	require.Equal(t, game.NightSubAct, g.State().CurrentNightSubPhase(),
		"a vigilante who held fire keeps a live act window on later nights")
	evts2 := nightAction(t, g, "vig", "town1")
	rec, ok := findEvent[game.NightActionRecorded](evts2)
	require.True(t, ok, "the preserved bullet fires on a later night")
	require.Equal(t, game.PlayerID("town1"), rec.Target)
}

// Holding fire is only valid during the vigilante's OWN act window.
// On another role's turn it's rejected as wrong-time.
func TestVigilante_HoldFireRejectedWhenNotHisTurn(t *testing.T) {
	g := fixedRosterWithVigilante(t)
	require.Equal(t, game.RoleMafia, g.State().CurrentNightRole())

	_, err := g.Apply(game.NightPass{Actor: "vig"})
	require.ErrorIs(t, err, game.ErrNotYourTurn,
		"the vigilante cannot hold fire during another role's turn")
}

// NightPass is opt-in per role (AllowPass). Only the vigilante exposes
// it today; every other role — including those WITH a night action —
// rejects it with ErrNotYourAction, even during their own act window.
func TestNightPass_RejectedForRolesWithoutAllowPass(t *testing.T) {
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

// --- two-mafia parity plays on; the town can still convert it -------------

// fixedRoster2MafiaWithVigilante deals a 6-player game with TWO mafia and
// the vigilante enabled:
//
//	mafia1, mafia2 -> RoleMafia
//	det            -> RoleDetective
//	doc            -> RoleDoctor
//	vig            -> RoleVigilante
//	town1          -> RoleVillager
//
// Town faction is det+doc+vig+town1 (4) vs 2 strict mafia, so it opens
// well below parity. Used to drive the board down to an exact 2-vs-2
// parity where the town still holds a doctor and a loaded vigilante.
func fixedRoster2MafiaWithVigilante(t *testing.T) *game.Game {
	t.Helper()
	return fixedRosterMatching(t, rosterDeal{
		ids: []game.PlayerID{"mafia1", "mafia2", "det", "doc", "vig", "town1"},
		wanted: map[game.PlayerID]game.Role{
			"mafia1": game.RoleMafia,
			"mafia2": game.RoleMafia,
			"det":    game.RoleDetective,
			"doc":    game.RoleDoctor,
			"vig":    game.RoleVigilante,
			"town1":  game.RoleVillager,
		},
		mafiaCount: 2,
		vigilante:  true,
		maxSeeds:   5000,
	})
}

// driveToTwoMafiaParity walks fixedRoster2MafiaWithVigilante down to an
// exact 2-vs-2 parity with the town still holding a doctor and a LOADED
// vigilante. The mafia kill the villager (N1) and the detective (N2);
// nobody is saved and the vigilante holds fire, so the bullet stays
// loaded. Returns the game on PhaseDayDiscussion at {mafia1, mafia2,
// doc, vig}. The N2 batch never ends the game — parity with two mafia is
// not an instant win.
func driveToTwoMafiaParity(t *testing.T) *game.Game {
	t.Helper()
	g := fixedRoster2MafiaWithVigilante(t)

	// Night 1: the mafia kill the lone villager (unsaved). 2 mafia vs 3 town.
	runVigilanteNightToDay(t, g, map[game.Role]game.PlayerID{
		game.RoleMafia: "town1",
	})
	require.Equal(t, game.PhaseDayDiscussion, g.State().Phase(),
		"2 mafia vs 3 town — no win yet")
	require.False(t, livingByID(g, "town1"))

	noLynchDay(t, g)
	beginNightToMafiaAct(t, g)

	// Night 2: the mafia kill the detective -> exact parity, 2 mafia vs
	// {doc, vig(loaded)}.
	evts := runVigilanteNightToDay(t, g, map[game.Role]game.PlayerID{
		game.RoleMafia: "det",
	})
	_, ended := findEvent[game.GameEnded](evts)
	require.False(t, ended,
		"exact parity with two mafia is not an instant win — the game plays on")
	require.Equal(t, game.PhaseDayDiscussion, g.State().Phase())
	return g
}

// Exact parity with two (or more) mafia is NOT an instant mafia win:
// the town may still hold a winning line, so the game plays on rather
// than short-circuiting. Here the board is 2 mafia vs {doc, loaded vig}.
func TestWin_TwoMafiaParityDoesNotEnd(t *testing.T) {
	g := driveToTwoMafiaParity(t)

	require.Equal(t, game.PhaseDayDiscussion, g.State().Phase(),
		"the game continues at 2-vs-2 parity")
	require.False(t, g.State().VigilanteShotUsed(), "the bullet is still loaded")
	require.True(t, livingByID(g, "mafia1") && livingByID(g, "mafia2"))
	require.True(t, livingByID(g, "doc") && livingByID(g, "vig"))
}

// The payoff that justifies playing a two-mafia parity on: from 2 mafia
// vs {doctor, loaded vigilante} the doctor shields the vigilante from the
// mafia kill and the vigilante spends his bullet on a mafioso, dropping
// the mafia below parity. The town then out-votes the lone survivor.
func TestWin_LoadedVigilanteAndDoctorConvertParityToTownWin(t *testing.T) {
	g := driveToTwoMafiaParity(t)

	noLynchDay(t, g)
	beginNightToMafiaAct(t, g)

	// Night 3: mafia shoot the vigilante to stop his shot; the doctor saves
	// him; the vigilante kills mafia1. -> 1 mafia vs {doc, vig(spent)}.
	evts := runVigilanteNightToDay(t, g, map[game.Role]game.PlayerID{
		game.RoleMafia:     "vig",
		game.RoleVigilante: "mafia1",
		game.RoleDoctor:    "vig",
	})
	_, ended := findEvent[game.GameEnded](evts)
	require.False(t, ended, "1 mafia vs 2 town — the game continues to the day")
	require.True(t, livingByID(g, "vig"), "the doctor's save kept the vigilante alive")
	require.False(t, livingByID(g, "mafia1"), "the vigilante's bullet dropped a mafioso")
	require.True(t, g.State().VigilanteShotUsed(), "the bullet was spent")
	require.Equal(t, game.PhaseDayDiscussion, g.State().Phase())

	// The town now out-votes the lone remaining mafioso for the win.
	lynch := finalizeLynch(t, g, "mafia2")
	ge, ok := findEvent[game.GameEnded](lynch)
	require.True(t, ok, "lynching the last mafioso wins it for the town")
	require.Equal(t, game.FactionTown, ge.Winner)
	require.Equal(t, game.PhaseEnded, g.State().Phase())
}
