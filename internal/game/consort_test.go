package game_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/96malhar/mafia-the-game/internal/game"
)

// --- consort fixtures -----------------------------------------------------

// fixedRosterWithConsort builds a deterministic 6-player game with the
// optional Consort enabled, mapping each player ID to a fixed role:
//
//	mafia1  -> RoleMafia
//	consort -> RoleConsort
//	det     -> RoleDetective
//	doc     -> RoleDoctor
//	town1   -> RoleVillager
//	town2   -> RoleVillager
//
// composeRoster(6, 1, consort=true) deals exactly that set (the consort
// consumes one villager slot). We brute-force seeds until the random
// deal matches; 6! = 720 permutations make this trivial.
//
// On return the game is in PhaseNight sitting on the MAFIA's act window
// (same postcondition as fixedRoster). The night turn order with a
// consort present is Mafia -> Consort -> Detective -> Doctor.
func fixedRosterWithConsort(t *testing.T) *game.Game {
	t.Helper()
	ids := []game.PlayerID{"mafia1", "consort", "det", "doc", "town1", "town2"}
	wanted := map[game.PlayerID]game.Role{
		"mafia1":  game.RoleMafia,
		"consort": game.RoleConsort,
		"det":     game.RoleDetective,
		"doc":     game.RoleDoctor,
		"town1":   game.RoleVillager,
		"town2":   game.RoleVillager,
	}

	for seed := range int64(5000) {
		g := game.New()
		_, err := g.Apply(game.CreateGame{
			GameID: "g1", MinPlayers: 6, MaxPlayers: 20, MafiaCount: 1, Seed: seed,
		})
		require.NoError(t, err)
		_, err = g.Apply(game.SetConsort{Enabled: true})
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
		if !match {
			continue
		}
		consortBeginNight(t, g)
		return g
	}
	t.Fatalf("could not find a seed yielding the fixed consort roster in 5000 attempts")
	return nil
}

// consortBeginNight issues BeginNight and walks opening -> mafia narrate
// -> mafia act, leaving the game on the mafia's act window.
func consortBeginNight(t *testing.T, g *game.Game) {
	t.Helper()
	_, err := g.Apply(game.BeginNight{})
	require.NoError(t, err)
	require.Equal(t, game.NightSubOpening, g.State().CurrentNightSubPhase())
	advancePhase(t, g) // opening -> mafia narrate
	require.Equal(t, game.RoleMafia, g.State().CurrentNightRole())
	advancePhase(t, g) // mafia narrate -> mafia act
	require.Equal(t, game.NightSubAct, g.State().CurrentNightSubPhase(),
		"consort fixture must leave the game on the mafia act window")
}

// runConsortNightToDay walks from the mafia act window through the whole
// night (Mafia -> Consort -> Detective -> Doctor), submitting the given
// per-role actions; any role missing from the map times out. It returns
// every event emitted across all sub-phase transitions and the resolve
// batch. The game ends on PhaseDayDiscussion (or PhaseEnded if the night
// resolved a win).
func runConsortNightToDay(t *testing.T, g *game.Game, actions map[game.Role]game.PlayerID) []game.Event {
	t.Helper()
	order := []game.Role{game.RoleMafia, game.RoleConsort, game.RoleDetective, game.RoleDoctor}
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

// lynch drives the current DayDiscussion to a lynch of target: it opens
// voting, has every living player except the target vote for the target
// (always a strict majority), and finalizes. Returns the FinalizeVotes
// events. The game may end (town win) or return to DayDiscussion.
func lynch(t *testing.T, g *game.Game, target game.PlayerID) []game.Event {
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

// --- roster + toggle ------------------------------------------------------

func TestConsort_SetConsortToggle(t *testing.T) {
	g := game.New()
	_, err := g.Apply(game.CreateGame{
		GameID: "g1", MinPlayers: 6, MaxPlayers: 20, MafiaCount: 1, Seed: 0,
	})
	require.NoError(t, err)

	// Default off: setting off again is a no-op.
	_, err = g.Apply(game.SetConsort{Enabled: false})
	require.ErrorIs(t, err, game.ErrNoChange)
	require.False(t, g.State().ConsortEnabled())

	// Enable: emits ConsortChanged{true}, flips state.
	evts, err := g.Apply(game.SetConsort{Enabled: true})
	require.NoError(t, err)
	cc, ok := findEvent[game.ConsortChanged](evts)
	require.True(t, ok, "enabling emits ConsortChanged")
	require.True(t, cc.Enabled)
	require.Equal(t, "public", cc.Visibility().Audience,
		"the composition change is public (it does NOT reveal who)")
	require.True(t, g.State().ConsortEnabled())

	// Re-enable is a no-op.
	_, err = g.Apply(game.SetConsort{Enabled: true})
	require.ErrorIs(t, err, game.ErrNoChange)

	// Disable works.
	evts, err = g.Apply(game.SetConsort{Enabled: false})
	require.NoError(t, err)
	cc, ok = findEvent[game.ConsortChanged](evts)
	require.True(t, ok)
	require.False(t, cc.Enabled)
	require.False(t, g.State().ConsortEnabled())
}

func TestConsort_SetConsortLockedAfterDeal(t *testing.T) {
	// Once StartGame has dealt roles the toggle is locked.
	g := game.New()
	_, err := g.Apply(game.CreateGame{
		GameID: "g1", MinPlayers: 6, MaxPlayers: 20, MafiaCount: 1, Seed: 0,
	})
	require.NoError(t, err)
	for _, id := range []game.PlayerID{"a", "b", "c", "d", "e", "f"} {
		_, err := g.Apply(game.AddPlayer{PlayerID: id, Name: string(id)})
		require.NoError(t, err)
	}
	_, err = g.Apply(game.StartGame{})
	require.NoError(t, err)

	_, err = g.Apply(game.SetConsort{Enabled: true})
	require.ErrorIs(t, err, game.ErrWrongPhase,
		"the consort toggle is locked once roles are dealt")
}

func TestConsort_RosterComposition(t *testing.T) {
	// Enabling the consort deals exactly one RoleConsort, taking the
	// slot of a villager, and the mafia roster lists only true mafia.
	g := game.New()
	_, err := g.Apply(game.CreateGame{
		GameID: "g1", MinPlayers: 6, MaxPlayers: 20, MafiaCount: 1, Seed: 0,
	})
	require.NoError(t, err)
	_, err = g.Apply(game.SetConsort{Enabled: true})
	require.NoError(t, err)
	for _, id := range []game.PlayerID{"a", "b", "c", "d", "e", "f"} {
		_, err := g.Apply(game.AddPlayer{PlayerID: id, Name: string(id)})
		require.NoError(t, err)
	}
	start, err := g.Apply(game.StartGame{})
	require.NoError(t, err)

	counts := map[game.Role]int{}
	roleByID := map[game.PlayerID]game.Role{}
	for _, p := range g.State().Players() {
		counts[p.Role()]++
		roleByID[p.ID()] = p.Role()
	}
	require.Equal(t, 1, counts[game.RoleConsort], "exactly one consort dealt")
	require.Equal(t, 1, counts[game.RoleMafia])
	require.Equal(t, 1, counts[game.RoleDetective])
	require.Equal(t, 1, counts[game.RoleDoctor])
	require.Equal(t, 2, counts[game.RoleVillager],
		"consort took a villager slot (6 - mafia - det - doc - consort = 2)")

	roster, ok := findEvent[game.MafiaRosterRevealed](start)
	require.True(t, ok, "StartGame still emits the mafia roster")
	require.Len(t, roster.Members, 1, "1-mafia roster lists exactly the mafia")
	for _, m := range roster.Members {
		require.Equal(t, game.RoleMafia, roleByID[m],
			"the consort must NEVER appear in the mafia roster")
	}
}

// --- night turn order -----------------------------------------------------

func TestConsort_NightTurnOrderIsMafiaConsortDetectiveDoctor(t *testing.T) {
	g := fixedRosterWithConsort(t)
	require.Equal(t, game.RoleMafia, g.State().CurrentNightRole(),
		"mafia acts first, even with a consort present")

	nightAction(t, g, "mafia1", "town1")
	walkRestOfTurn(t, g)
	require.Equal(t, game.RoleConsort, g.State().CurrentNightRole(),
		"the consort's turn comes right AFTER the mafia")
	require.Equal(t, game.NightSubAct, g.State().CurrentNightSubPhase())

	nightAction(t, g, "consort", "town2")
	walkRestOfTurn(t, g)
	require.Equal(t, game.RoleDetective, g.State().CurrentNightRole(),
		"detective follows the consort")

	nightAction(t, g, "det", "mafia1")
	walkRestOfTurn(t, g)
	require.Equal(t, game.RoleDoctor, g.State().CurrentNightRole(),
		"doctor is last")

	nightAction(t, g, "doc", "town1")
	walkRestOfTurn(t, g)
	require.Equal(t, game.PhaseDayDiscussion, g.State().Phase())
}

// --- block resolution -----------------------------------------------------

func TestConsort_BlockedDoctorCannotSaveAndVictimDies(t *testing.T) {
	// Mafia kills town1; consort blocks the doctor. The doctor's attempt
	// to save town1 is REJECTED with ErrBlocked at submit time, so the
	// kill lands and town1 dies.
	g := fixedRosterWithConsort(t)

	nightAction(t, g, "mafia1", "town1") // kill town1
	walkRestOfTurn(t, g)                 // -> consort act
	require.Equal(t, game.RoleConsort, g.State().CurrentNightRole())
	nightAction(t, g, "consort", "doc") // block the doctor
	walkRestOfTurn(t, g)                // -> detective act
	require.Equal(t, game.RoleDetective, g.State().CurrentNightRole())
	walkRestOfTurn(t, g) // detective idle -> doctor act
	require.Equal(t, game.RoleDoctor, g.State().CurrentNightRole())

	// The blocked doctor's submission is rejected outright.
	_, err := g.Apply(game.NightAction{Actor: "doc", Target: "town1"})
	require.ErrorIs(t, err, game.ErrBlocked,
		"a blocked doctor cannot submit a save")

	// Walk to resolve: town1 dies (no save was ever recorded).
	var evts []game.Event
	for g.State().Phase() == game.PhaseNight {
		evts = append(evts, walkRestOfTurn(t, g)...)
	}
	killed, ok := findEvent[game.PlayerKilled](evts)
	require.True(t, ok, "with the save blocked, the kill lands")
	require.Equal(t, game.PlayerID("town1"), killed.PlayerID)
	_, saved := findEvent[game.PlayerSaved](evts)
	require.False(t, saved, "a blocked doctor produces no save")

	for _, p := range g.State().Players() {
		if p.ID() == "town1" {
			require.False(t, p.Alive(), "town1 should be dead (save blocked)")
		}
	}
}

func TestConsort_BlockedDoctorCannotSaveTheConsortVictimAndConsortDies(t *testing.T) {
	// The reflexive case: the mafia targets the CONSORT, and the consort
	// — acting earlier in the same night, while still alive (deaths only
	// resolve at night's end) — blocks the very doctor who would save
	// her. Her own block defeats her rescue: the doctor's save of the
	// consort is rejected, so the kill lands and she dies.
	g := fixedRosterWithConsort(t)

	nightAction(t, g, "mafia1", "consort") // mafia targets the consort
	walkRestOfTurn(t, g)                   // -> consort act
	require.Equal(t, game.RoleConsort, g.State().CurrentNightRole())
	nightAction(t, g, "consort", "doc") // the consort blocks her own savior
	walkRestOfTurn(t, g)                // -> detective act
	require.Equal(t, game.RoleDetective, g.State().CurrentNightRole())
	walkRestOfTurn(t, g) // detective idle -> doctor act
	require.Equal(t, game.RoleDoctor, g.State().CurrentNightRole())

	// The blocked doctor's attempt to save the consort is rejected.
	_, err := g.Apply(game.NightAction{Actor: "doc", Target: "consort"})
	require.ErrorIs(t, err, game.ErrBlocked,
		"a blocked doctor cannot save anyone — not even the consort who blocked them")

	// Walk to resolve: the consort dies (no save was ever recorded).
	var evts []game.Event
	for g.State().Phase() == game.PhaseNight {
		evts = append(evts, walkRestOfTurn(t, g)...)
	}
	killed, ok := findEvent[game.PlayerKilled](evts)
	require.True(t, ok, "the consort's kill lands because she blocked her own savior")
	require.Equal(t, game.PlayerID("consort"), killed.PlayerID)
	_, saved := findEvent[game.PlayerSaved](evts)
	require.False(t, saved, "a blocked doctor produces no save")

	for _, p := range g.State().Players() {
		if p.ID() == "consort" {
			require.False(t, p.Alive(), "the consort should be dead (her own block voided the save)")
		}
	}
}

func TestConsort_UnblockedDoctorSaveStillWorks(t *testing.T) {
	// Control for the block test: with the consort blocking someone else
	// (the detective), the doctor's save of the mafia target succeeds.
	g := fixedRosterWithConsort(t)
	evts := runConsortNightToDay(t, g, map[game.Role]game.PlayerID{
		game.RoleMafia:   "town1",
		game.RoleConsort: "det", // blocks the detective, not the doctor
		game.RoleDoctor:  "town1",
	})

	saved, ok := findEvent[game.PlayerSaved](evts)
	require.True(t, ok, "an unblocked doctor save works normally")
	require.Equal(t, game.PlayerID("town1"), saved.PlayerID)
	_, killed := findEvent[game.PlayerKilled](evts)
	require.False(t, killed, "the save cancels the kill")
}

func TestConsort_BlockNoticeArrivesAtTurnStart(t *testing.T) {
	// The blocked town role learns they're blocked at the START of their
	// own act window (not at submit or resolve), via a private Blocked
	// event. Here the consort blocks the doctor: the Blocked event must
	// ride the batch that opens the doctor's act window.
	g := fixedRosterWithConsort(t)

	// Mafia acts; consort blocks the doctor.
	nightAction(t, g, "mafia1", "town1")
	walkRestOfTurn(t, g)
	require.Equal(t, game.RoleConsort, g.State().CurrentNightRole())
	nightAction(t, g, "consort", "doc")

	// Walk consort -> detective. The detective is NOT blocked, so no
	// Blocked event should appear when their window opens.
	detBatch := walkRestOfTurn(t, g)
	require.Equal(t, game.RoleDetective, g.State().CurrentNightRole())
	_, detBlocked := findEvent[game.Blocked](detBatch)
	require.False(t, detBlocked, "the detective wasn't blocked — no notice")

	// Walk detective -> doctor. NOW the Blocked event fires, at the
	// doctor's act-window open, private to the doctor.
	docBatch := walkRestOfTurn(t, g)
	require.Equal(t, game.RoleDoctor, g.State().CurrentNightRole())
	require.Equal(t, game.NightSubAct, g.State().CurrentNightSubPhase())
	blk, ok := findEvent[game.Blocked](docBatch)
	require.True(t, ok, "the blocked doctor is notified at the start of their turn")
	require.Equal(t, game.PlayerID("doc"), blk.PlayerID)
	require.Equal(t, "player", blk.Visibility().Audience)
	require.Equal(t, game.PlayerID("doc"), blk.Visibility().Player,
		"the block notice is private to the blocked player; the room must not learn the target")
}

func TestConsort_BlockedDetectiveCannotAct(t *testing.T) {
	// A blocked detective is notified at turn start and, if a client
	// bypasses the hidden picker and submits, the action is REJECTED with
	// ErrBlocked (so they never receive a DetectiveResult).
	g := fixedRosterWithConsort(t)

	nightAction(t, g, "mafia1", "town1")
	walkRestOfTurn(t, g)
	require.Equal(t, game.RoleConsort, g.State().CurrentNightRole())
	nightAction(t, g, "consort", "det")

	// consort -> detective: the Blocked notice opens the detective's turn.
	detBatch := walkRestOfTurn(t, g)
	require.Equal(t, game.RoleDetective, g.State().CurrentNightRole())
	blk, ok := findEvent[game.Blocked](detBatch)
	require.True(t, ok, "blocked detective notified at turn start")
	require.Equal(t, game.PlayerID("det"), blk.PlayerID)

	// Submit anyway: rejected, and no investigation result is produced.
	evts, err := g.Apply(game.NightAction{Actor: "det", Target: "mafia1"})
	require.ErrorIs(t, err, game.ErrBlocked, "a blocked detective cannot act")
	_, hasResult := findEvent[game.DetectiveResult](evts)
	require.False(t, hasResult, "a blocked detective learns nothing")
}

func TestConsort_BlockOnMafiaHasNoEffect(t *testing.T) {
	// The consort may target a mafioso, but it's wasted: the kill still
	// lands and no Blocked notice is sent to the mafia.
	g := fixedRosterWithConsort(t)
	evts := runConsortNightToDay(t, g, map[game.Role]game.PlayerID{
		game.RoleMafia:   "town1",
		game.RoleConsort: "mafia1", // blocking the mafia is a no-op
	})

	killed, ok := findEvent[game.PlayerKilled](evts)
	require.True(t, ok, "the mafia kill is immune to the block")
	require.Equal(t, game.PlayerID("town1"), killed.PlayerID)

	for _, e := range evts {
		if b, ok := e.(game.Blocked); ok {
			require.NotEqual(t, game.PlayerID("mafia1"), b.PlayerID,
				"a blocked mafioso receives no notice (the block is a no-op)")
		}
	}
}

func TestConsort_CurrentActorBlockedReflectsBlock(t *testing.T) {
	// CurrentActorBlocked drives the room's shortened act window. It must
	// report false for the mafia and the consort herself, false for an
	// un-targeted town role, and true only once the blocked role's turn
	// is in flight.
	g := fixedRosterWithConsort(t)

	require.Equal(t, game.RoleMafia, g.State().CurrentNightRole())
	require.False(t, g.State().CurrentActorBlocked(), "mafia is never reported blocked")

	nightAction(t, g, "mafia1", "town1")
	walkRestOfTurn(t, g) // -> consort act
	require.Equal(t, game.RoleConsort, g.State().CurrentNightRole())
	require.False(t, g.State().CurrentActorBlocked(), "the consort herself is not blocked")

	nightAction(t, g, "consort", "doc") // block the doctor
	walkRestOfTurn(t, g)                // -> detective act
	require.Equal(t, game.RoleDetective, g.State().CurrentNightRole())
	require.False(t, g.State().CurrentActorBlocked(), "detective wasn't the block target")

	walkRestOfTurn(t, g) // -> doctor act
	require.Equal(t, game.RoleDoctor, g.State().CurrentNightRole())
	require.Equal(t, game.NightSubAct, g.State().CurrentNightSubPhase())
	require.True(t, g.State().CurrentActorBlocked(),
		"the blocked doctor's turn reports blocked (drives the short act window)")
}

// --- detective reads consort ----------------------------------------------

func TestConsort_DetectiveReadsUnpromotedConsortAsNotMafia(t *testing.T) {
	g := fixedRosterWithConsort(t)

	// Mafia + consort idle; detective investigates the consort.
	walkRestOfTurn(t, g) // mafia -> consort
	walkRestOfTurn(t, g) // consort -> detective
	require.Equal(t, game.RoleDetective, g.State().CurrentNightRole())

	evts := nightAction(t, g, "det", "consort")
	res, ok := findEvent[game.DetectiveResult](evts)
	require.True(t, ok)
	require.Equal(t, game.PlayerID("consort"), res.Target)
	require.False(t, res.IsMafia,
		"an un-promoted consort reads as NOT mafia (she's FactionConsort)")
}

func TestConsort_DetectiveReadsConsortAsMafiaOnlyAfterPromotion(t *testing.T) {
	// What the detective reads is the consort's FACTION, and promotion
	// rewrites it. Night 1: the detective investigates the un-promoted
	// consort and gets a clean "not mafia". The town then lynches the
	// only original mafia, promoting the living consort to RoleMafia.
	// Night 2: the very same investigation now returns "mafia".
	g := fixedRosterWithConsort(t)

	// Night 1 — detective investigates the consort; everyone else idles.
	evts := runConsortNightToDay(t, g, map[game.Role]game.PlayerID{
		game.RoleDetective: "consort",
	})
	require.Equal(t, game.PhaseDayDiscussion, g.State().Phase())
	res, ok := findEvent[game.DetectiveResult](evts)
	require.True(t, ok, "the night-1 investigation must produce a result")
	require.Equal(t, game.PlayerID("consort"), res.Target)
	require.False(t, res.IsMafia,
		"before promotion the consort is FactionConsort — reads as NOT mafia")

	// Day 1 — lynch the only mafia, which promotes the consort.
	lynchEvts := lynch(t, g, "mafia1")
	promo, ok := findEvent[game.ConsortPromoted](lynchEvts)
	require.True(t, ok, "lynching the last mafia promotes the living consort")
	require.Equal(t, game.PlayerID("consort"), promo.PlayerID)
	require.Equal(t, game.PhaseDayDiscussion, g.State().Phase(),
		"promotion keeps the mafia side alive — the game continues")

	// Night 2 — the consort now holds RoleMafia, but her CONSORT turn
	// still runs as a phantom (queued from the dealt-time consort, not
	// the live role) so the night cadence is unchanged and the takeover
	// stays hidden. Order is Mafia -> Consort(phantom) -> Detective ->
	// Doctor. Walk from the mafia act window through the phantom consort
	// turn to the detective's and re-investigate her.
	consortBeginNight(t, g) // BeginNight -> ... -> mafia act window
	require.Equal(t, game.RoleMafia, g.State().CurrentNightRole())
	walkRestOfTurn(t, g) // mafia idles -> consort phantom ponder
	require.Equal(t, game.RoleConsort, g.State().CurrentNightRole(),
		"a promoted consort still keeps her phantom turn to hide the takeover")
	require.Equal(t, game.NightSubPonder, g.State().CurrentNightSubPhase(),
		"the promoted consort's turn is phantom — ponder substitutes for act")
	walkRestOfTurn(t, g) // consort phantom -> detective act
	require.Equal(t, game.RoleDetective, g.State().CurrentNightRole())

	evts2 := nightAction(t, g, "det", "consort")
	res2, ok := findEvent[game.DetectiveResult](evts2)
	require.True(t, ok, "the night-2 investigation must produce a result")
	require.Equal(t, game.PlayerID("consort"), res2.Target)
	require.True(t, res2.IsMafia,
		"after promotion the consort holds RoleMafia — reads as mafia")
}

// TestConsort_PromotedConsortKeepsPhantomTurn pins the anti-leak
// invariant: once a consort is promoted to RoleMafia, her CONSORT turn
// must STILL run every night as a phantom. Dropping it would shorten
// the moderator's night cadence the instant a promotion happens,
// betraying the secret takeover to anyone counting the beats. The turn
// is queued from the dealt-time consort (consortEnabled), not the live
// role, and substitutes ponder for the act window because no living
// player holds RoleConsort once she's been promoted.
func TestConsort_PromotedConsortKeepsPhantomTurn(t *testing.T) {
	g := fixedRosterWithConsort(t)

	// Night 1: everyone idles; back to day.
	runConsortNightToDay(t, g, nil)
	require.Equal(t, game.PhaseDayDiscussion, g.State().Phase())

	// Lynch the only original mafia -> promotes the living consort.
	lynchEvts := lynch(t, g, "mafia1")
	_, promoted := findEvent[game.ConsortPromoted](lynchEvts)
	require.True(t, promoted, "lynching the last mafia promotes the consort")

	// Night 2: from the mafia act window, walking the rest of the turn
	// must land on the consort's PHANTOM turn (ponder, not act).
	consortBeginNight(t, g)
	require.Equal(t, game.RoleMafia, g.State().CurrentNightRole())

	evts := walkRestOfTurn(t, g) // mafia act -> consort phantom ponder
	require.Equal(t, game.RoleConsort, g.State().CurrentNightRole(),
		"the promoted consort's phantom turn must still run")
	require.Equal(t, game.NightSubPonder, g.State().CurrentNightSubPhase(),
		"a phantom turn substitutes ponder for the act window")

	// The consort narrate emitted while walking into this turn must be
	// flagged Phantom so the room sizes it as the (randomized) phantom
	// window rather than a live narrate.
	narr, ok := findNightSub(evts, game.NightSubNarrate)
	require.True(t, ok, "the consort turn must emit a narrate")
	require.Equal(t, game.RoleConsort, narr.Role)
	require.True(t, narr.Phantom,
		"a promoted consort's narrate must be Phantom — she no longer holds RoleConsort")

	// And no one can act on the phantom consort turn: the promoted
	// consort now holds RoleMafia, so she's not the current night role.
	_, err := g.Apply(game.NightAction{Actor: "consort", Target: "town1"})
	require.ErrorIs(t, err, game.ErrNotYourTurn,
		"a phantom consort turn accepts no action")

	// The night still completes normally through detective and doctor.
	walkRestOfTurn(t, g) // consort phantom -> detective act
	require.Equal(t, game.RoleDetective, g.State().CurrentNightRole())
}

// --- promotion + win conditions -------------------------------------------

func TestConsort_PromotedWhenLastMafiaLynched(t *testing.T) {
	// A quiet night, then the town lynches the only mafia. With the
	// consort still alive she's promoted to RoleMafia (private notice),
	// and the game continues — the town has NOT won.
	g := fixedRosterWithConsort(t)
	runConsortNightToDay(t, g, nil) // everyone idle
	require.Equal(t, game.PhaseDayDiscussion, g.State().Phase())

	evts := lynch(t, g, "mafia1")

	l, ok := findEvent[game.PlayerLynched](evts)
	require.True(t, ok)
	require.Equal(t, game.PlayerID("mafia1"), l.PlayerID)

	promo, ok := findEvent[game.ConsortPromoted](evts)
	require.True(t, ok, "lynching the last mafia promotes the living consort")
	require.Equal(t, game.PlayerID("consort"), promo.PlayerID)
	require.Equal(t, "player", promo.Visibility().Audience)
	require.Equal(t, game.PlayerID("consort"), promo.Visibility().Player,
		"promotion is private: the town must not learn a sleeper took over")

	// A fresh roster reveal rides along so her client learns its new faction.
	roster, ok := findEvent[game.MafiaRosterRevealed](evts)
	require.True(t, ok, "promotion re-issues the mafia roster (now just her)")
	require.Equal(t, []game.PlayerID{"consort"}, roster.Members)

	_, ended := findEvent[game.GameEnded](evts)
	require.False(t, ended, "promotion keeps the mafia side alive — town has not won")
	require.Equal(t, game.PhaseDayDiscussion, g.State().Phase())

	for _, p := range g.State().Players() {
		if p.ID() == "consort" {
			require.Equal(t, game.RoleMafia, p.Role(),
				"the consort now holds RoleMafia")
		}
	}
}

func TestConsort_NoPromotionWhenMafiaSurvives(t *testing.T) {
	// Lynch a villager: the mafia is still alive, so no promotion fires.
	g := fixedRosterWithConsort(t)
	runConsortNightToDay(t, g, nil)
	require.Equal(t, game.PhaseDayDiscussion, g.State().Phase())

	evts := lynch(t, g, "town1")
	_, promoted := findEvent[game.ConsortPromoted](evts)
	require.False(t, promoted, "no promotion while the cabal still lives")
	for _, p := range g.State().Players() {
		if p.ID() == "consort" {
			require.Equal(t, game.RoleConsort, p.Role(),
				"consort stays a consort while a mafia survives")
		}
	}
}

func TestConsort_CountsTowardMafiaParityWin(t *testing.T) {
	// An un-promoted consort counts on the mafia side for win conditions:
	// mafia (1) + consort (1) reach parity with town once town drops to 2.
	g := fixedRosterWithConsort(t)

	// Night 1: mafia kills town1. Living after: mafia1, consort, det,
	// doc, town2 (mafia-aligned 2 < town 3 — no win yet).
	evts := runConsortNightToDay(t, g, map[game.Role]game.PlayerID{
		game.RoleMafia: "town1",
	})
	_, ended := findEvent[game.GameEnded](evts)
	require.False(t, ended, "2 mafia-aligned vs 3 town — game continues")
	require.Equal(t, game.PhaseDayDiscussion, g.State().Phase())

	// Skip the day with no lynch, then start the next night.
	_, err := g.Apply(game.OpenVoting{})
	require.NoError(t, err)
	_, err = g.Apply(game.FinalizeVotes{}) // empty tally -> NoLynch
	require.NoError(t, err)
	require.True(t, g.State().DayLynchResolved())
	consortBeginNight(t, g)

	// Night 2: mafia kills town2. Living after: mafia1, consort, det,
	// doc (mafia-aligned 2 == town 2 -> mafia wins, consort counted in).
	evts = runConsortNightToDay(t, g, map[game.Role]game.PlayerID{
		game.RoleMafia: "town2",
	})
	ge, ok := findEvent[game.GameEnded](evts)
	require.True(t, ok, "mafia reaches parity with the consort counted on their side")
	require.Equal(t, game.FactionMafia, ge.Winner)
	require.Equal(t, game.PhaseEnded, g.State().Phase())
}
