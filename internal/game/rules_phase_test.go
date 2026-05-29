package game_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/malhar/mafia-the-game/internal/game"
)

// --- test fixtures ---------------------------------------------------------

// fixedRoster builds a deterministic 5-player game where each player ID
// maps to a specific role regardless of seed:
//
//	mafia1   -> RoleMafia
//	det      -> RoleDetective
//	doc      -> RoleDoctor
//	town1    -> RoleVillager
//	town2    -> RoleVillager
//
// Brute force over seeds finds an assignment that matches the wanted
// mapping. With 5! = 120 permutations and a fast PCG, this is trivial.
//
// On return the game is in PhaseNight, the opening sub-phase has been
// elapsed, and the mafia's NightSubNarrate has been elapsed too — i.e.
// the caller is sitting on the mafia's NightSubAct window, ready to
// submit (or skip via AdvancePhase to simulate a timeout). This
// matches how every test in this file uses the fixture: at the start
// of "real" mafia action.
func fixedRoster(t *testing.T) *game.Game {
	t.Helper()
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
		_, err := g.Apply(game.CreateGame{
			GameID: "g1", MinPlayers: 5, MaxPlayers: 20, MafiaCount: 1, Seed: seed,
		})
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
		_, err = g.Apply(game.BeginNight{})
		require.NoError(t, err)
		// BeginNight emits NightOpeningStarted; advance through opening
		// and the first role's narrate so callers land on mafia's act.
		require.Equal(t, game.NightSubOpening, g.State().CurrentNightSubPhase())
		_, err = g.Apply(game.AdvancePhase{}) // opening → mafia narrate
		require.NoError(t, err)
		require.Equal(t, game.RoleMafia, g.State().CurrentNightRole())
		require.Equal(t, game.NightSubNarrate, g.State().CurrentNightSubPhase())
		_, err = g.Apply(game.AdvancePhase{}) // mafia narrate → act
		require.NoError(t, err)
		require.Equal(t, game.NightSubAct, g.State().CurrentNightSubPhase(),
			"fixedRoster must leave the game in mafia's act window")
		return g
	}
	t.Fatalf("could not find a seed yielding the fixed role assignment in 1000 attempts")
	return nil
}

// nightAction submits one night action for the role that is currently
// holding the act window. Fails the test if the engine rejects it.
// Use this in tests that exercise a single role's action; for
// orchestrating a full night, use playNight.
func nightAction(t *testing.T, g *game.Game, actor, target game.PlayerID) []game.Event {
	t.Helper()
	evts, err := g.Apply(game.NightAction{Actor: actor, Target: target})
	require.NoError(t, err)
	return evts
}

// advancePhase fires one AdvancePhase and returns the resulting events.
// Each call advances exactly one sub-phase boundary; see NightSubPhase
// for the transition graph.
func advancePhase(t *testing.T, g *game.Game) []game.Event {
	t.Helper()
	evts, err := g.Apply(game.AdvancePhase{})
	require.NoError(t, err)
	return evts
}

// walkRestOfTurn advances the engine through every remaining sub-phase
// of the CURRENT role's turn (and through the next role's narrate, if
// any). It ends on one of:
//   - the next role's act window (real role) — caller's next step is
//     usually a NightAction or another walkRestOfTurn to time it out;
//   - the next role's ponder window (phantom role, narrate has fired
//     but no act will) — caller can assert on the phantom and call
//     walkRestOfTurn again to step past it;
//   - PhaseDayDiscussion / PhaseEnded if this was the last role.
//
// The helper handles all three entry states transparently:
//
//	NightSubAct       — caller chose NOT to submit; walks via
//	                    act→sleep (skipping ponder), then through
//	                    settle and into the next role's window.
//	NightSubPonder    — caller already submitted (real) OR caller is
//	                    sitting on a fresh phantom-role ponder; walks
//	                    through sleep + settle + next role's narrate.
//	NightSubNarrate   — caller hand-stepped into narrate; walks
//	                    through act/ponder + sleep + settle + next.
//	                    Less common in tests but supported for
//	                    symmetry.
//
// Implementation: we track whether at least one settle has elapsed.
// Once settle has elapsed we know we're sitting in (or past) the
// next role's territory, and the next "stable" sub-phase (act for
// real roles, ponder for phantoms) is the right place to stop.
func walkRestOfTurn(t *testing.T, g *game.Game) []game.Event {
	t.Helper()
	var out []game.Event
	settleElapsed := false
	for {
		if g.State().Phase() != game.PhaseNight {
			return out
		}
		if settleElapsed {
			sp := g.State().CurrentNightSubPhase()
			// A fresh role-turn is at narrate or (for phantoms) at
			// ponder after narrate elapses. We want to stop at the
			// caller-meaningful boundary — act for real roles, ponder
			// for phantoms — so we let narrate elapse but stop at the
			// next sub-phase.
			if sp == game.NightSubAct {
				return out
			}
			if sp == game.NightSubPonder && !g.State().NightTurnSubmitted() {
				return out
			}
		}
		// Note the sub-phase we're about to leave so we can detect
		// when settle has just elapsed (next iteration's role will be
		// the new one).
		leaving := g.State().CurrentNightSubPhase()
		out = append(out, advancePhase(t, g)...)
		if leaving == game.NightSubSettle {
			settleElapsed = true
		}
	}
}

// playNight runs through a full Night with the given (role -> target)
// actions. Any role missing from the map is skipped (timeout-style):
// from act, AdvancePhase fires, skipping ponder, going through sleep
// and settle, popping the next role and walking it to its act window.
// Phantom turns (no living holder of the role) accept no action;
// they're walked through automatically.
//
// Returns every event emitted across all sub-phase transitions and
// the resolve batch, so callers can findEvent for any event type
// (DetectiveResult fires at act time, PlayerKilled / PlayerSaved /
// GameEnded fire at resolve).
//
// The helper assumes the caller is positioned at the mafia's act
// window (i.e. the postcondition of fixedRoster / toNextNight).
func playNight(t *testing.T, g *game.Game, actions map[game.Role]game.PlayerID) []game.Event {
	t.Helper()
	canonical := []game.Role{game.RoleMafia, game.RoleDetective, game.RoleDoctor}
	var allEvents []game.Event
	for _, r := range canonical {
		require.Equal(t, r, g.State().CurrentNightRole(),
			"playNight: expected role %s to be current but got %s",
			r, g.State().CurrentNightRole())
		sp := g.State().CurrentNightSubPhase()
		target, want := actions[r]

		// Phantom roles arrive at NightSubPonder (no act window).
		// Real roles arrive at NightSubAct.
		switch sp {
		case game.NightSubAct:
			if want {
				var actor game.PlayerID
				for _, p := range g.State().Players() {
					if p.Role() == r && p.Alive() {
						actor = p.ID()
						break
					}
				}
				require.NotEmpty(t, actor,
					"playNight: no living %s to submit action", r)
				allEvents = append(allEvents, nightAction(t, g, actor, target)...)
				// Post-submit: walk ponder → sleep → settle (→ next role).
				allEvents = append(allEvents, walkRestOfTurn(t, g)...)
			} else {
				// Skip via AdvancePhase: act → (timeout) → sleep →
				// settle → next role.
				allEvents = append(allEvents, walkRestOfTurn(t, g)...)
			}
		case game.NightSubPonder:
			// Phantom turn: just walk through.
			allEvents = append(allEvents, walkRestOfTurn(t, g)...)
		default:
			t.Fatalf("playNight: unexpected sub-phase %q at start of %s turn", sp, r)
		}

		if g.State().Phase() != game.PhaseNight {
			// Night resolved during this role's settle pop.
			return allEvents
		}
	}
	t.Fatalf("playNight: walked all three roles but still in PhaseNight (sub-phase %q)",
		g.State().CurrentNightSubPhase())
	return allEvents
}

// toDayVote walks the engine from the mafia-act position through to
// PhaseDayVote, applying the given (role -> target) night actions and
// then issuing OpenVoting (host-driven transition).
func toDayVote(t *testing.T, g *game.Game, actions map[game.Role]game.PlayerID) {
	t.Helper()
	playNight(t, g, actions)
	require.Equal(t, game.PhaseDayDiscussion, g.State().Phase())
	_, err := g.Apply(game.OpenVoting{})
	require.NoError(t, err)
	require.Equal(t, game.PhaseDayVote, g.State().Phase())
}

// toNextNight assumes the game is in PhaseDayDiscussion (post-Night
// resolve) and walks to the NEXT PhaseNight by simulating a lynch on
// the given target. After return, the engine is positioned at the
// mafia's act window (same postcondition as fixedRoster), so the
// caller can keep using nightAction / playNight directly.
func toNextNight(t *testing.T, g *game.Game, lynchTarget game.PlayerID, voters ...game.PlayerID) {
	t.Helper()
	require.Equal(t, game.PhaseDayDiscussion, g.State().Phase())
	_, err := g.Apply(game.OpenVoting{})
	require.NoError(t, err)
	for _, v := range voters {
		_, err := g.Apply(game.DayVote{Voter: v, Target: lynchTarget})
		require.NoError(t, err)
	}
	_, err = g.Apply(game.FinalizeVotes{})
	require.NoError(t, err)
	// FinalizeVotes either ends the game or returns to DayDiscussion
	// with lynchResolved=true.
	if g.State().Phase() == game.PhaseEnded {
		return
	}
	require.Equal(t, game.PhaseDayDiscussion, g.State().Phase())
	require.True(t, g.State().DayLynchResolved())
	_, err = g.Apply(game.BeginNight{})
	require.NoError(t, err)
	require.Equal(t, game.PhaseNight, g.State().Phase())
	// BeginNight → opening; walk through opening + mafia narrate so
	// caller lands on mafia act, matching fixedRoster's postcondition.
	require.Equal(t, game.NightSubOpening, g.State().CurrentNightSubPhase())
	advancePhase(t, g) // opening → mafia narrate
	require.Equal(t, game.NightSubNarrate, g.State().CurrentNightSubPhase())
	advancePhase(t, g) // mafia narrate → mafia act (or ponder if phantom — never)
	require.Equal(t, game.NightSubAct, g.State().CurrentNightSubPhase(),
		"toNextNight: expected mafia's act window (mafia phantom is unreachable by win conditions)")
}

// --- Night opening + turn-order plumbing ---------------------------------

func TestNight_OpeningEmittedAfterPhaseChange(t *testing.T) {
	// BeginNight should emit PhaseChanged{To: Night} then
	// NightOpeningStarted — NOT a NightNarrationStarted for the
	// mafia. The opening is a one-shot "City, go to sleep." beat
	// before any role's narration.
	g := game.New()
	_, err := g.Apply(game.CreateGame{
		GameID: "g1", MinPlayers: 5, MaxPlayers: 20, MafiaCount: 1, Seed: 0,
	})
	require.NoError(t, err)
	for _, id := range []game.PlayerID{"a", "b", "c", "d", "e"} {
		_, err := g.Apply(game.AddPlayer{PlayerID: id, Name: string(id)})
		require.NoError(t, err)
	}
	_, err = g.Apply(game.StartGame{})
	require.NoError(t, err)

	evts, err := g.Apply(game.BeginNight{})
	require.NoError(t, err)

	// PhaseChanged + NightOpeningStarted, nothing else night-related.
	_, opening := findEvent[game.NightOpeningStarted](evts)
	require.True(t, opening, "BeginNight must emit NightOpeningStarted")
	_, narrate := findEvent[game.NightNarrationStarted](evts)
	require.False(t, narrate, "BeginNight must NOT emit NightNarrationStarted yet")

	require.Equal(t, game.NightSubOpening, g.State().CurrentNightSubPhase())
	require.Equal(t, game.Role(""), g.State().CurrentNightRole(),
		"currentNightRole must be empty during opening")
}

func TestNight_OpeningElapsesIntoMafiaNarrate(t *testing.T) {
	// One AdvancePhase during NightSubOpening transitions into the
	// first role's NightSubNarrate.
	g := game.New()
	_, err := g.Apply(game.CreateGame{
		GameID: "g1", MinPlayers: 5, MaxPlayers: 20, MafiaCount: 1, Seed: 0,
	})
	require.NoError(t, err)
	for _, id := range []game.PlayerID{"a", "b", "c", "d", "e"} {
		_, err := g.Apply(game.AddPlayer{PlayerID: id, Name: string(id)})
		require.NoError(t, err)
	}
	_, err = g.Apply(game.StartGame{})
	require.NoError(t, err)
	_, err = g.Apply(game.BeginNight{})
	require.NoError(t, err)

	evts := advancePhase(t, g)
	ns, ok := findEvent[game.NightNarrationStarted](evts)
	require.True(t, ok, "opening → narrate must emit NightNarrationStarted")
	require.Equal(t, game.RoleMafia, ns.Role)
	require.Equal(t, game.RoleMafia, g.State().CurrentNightRole())
	require.Equal(t, game.NightSubNarrate, g.State().CurrentNightSubPhase())
}

func TestNight_FirstActWindowIsMafia(t *testing.T) {
	g := fixedRoster(t)
	require.Equal(t, game.PhaseNight, g.State().Phase())
	require.Equal(t, game.RoleMafia, g.State().CurrentNightRole())
	require.Equal(t, game.NightSubAct, g.State().CurrentNightSubPhase(),
		"the first act window must be the mafia's")
}

func TestNight_RoleTurnSubPhaseSequence(t *testing.T) {
	// Walk one full real turn and verify the exact sub-phase event
	// sequence: narrate → act → ponder → sleep → settle → next role's
	// narrate. We start from fixedRoster (already past mafia's
	// narrate); submit; then step through each remaining sub-phase
	// one AdvancePhase at a time.
	g := fixedRoster(t)
	require.Equal(t, game.NightSubAct, g.State().CurrentNightSubPhase())

	// Submit: act → ponder, with NightPonderStarted in the batch.
	evts := nightAction(t, g, "mafia1", "town1")
	_, pondered := findEvent[game.NightPonderStarted](evts)
	require.True(t, pondered, "submit must emit NightPonderStarted")
	require.Equal(t, game.NightSubPonder, g.State().CurrentNightSubPhase())
	require.True(t, g.State().NightTurnSubmitted(),
		"NightTurnSubmitted must be true after a submit")

	// ponder → sleep.
	evts = advancePhase(t, g)
	sl, ok := findEvent[game.NightSleepStarted](evts)
	require.True(t, ok, "ponder → sleep must emit NightSleepStarted")
	require.Equal(t, game.RoleMafia, sl.Role)
	require.Equal(t, game.NightSubSleep, g.State().CurrentNightSubPhase())

	// sleep → settle.
	evts = advancePhase(t, g)
	st, ok := findEvent[game.NightSettleStarted](evts)
	require.True(t, ok, "sleep → settle must emit NightSettleStarted")
	require.Equal(t, game.RoleMafia, st.Role)
	require.Equal(t, game.NightSubSettle, g.State().CurrentNightSubPhase())

	// settle → next role's narrate. The submitted flag resets.
	evts = advancePhase(t, g)
	ns, ok := findEvent[game.NightNarrationStarted](evts)
	require.True(t, ok, "settle → next role must emit NightNarrationStarted")
	require.Equal(t, game.RoleDetective, ns.Role)
	require.Equal(t, game.RoleDetective, g.State().CurrentNightRole())
	require.Equal(t, game.NightSubNarrate, g.State().CurrentNightSubPhase())
	require.False(t, g.State().NightTurnSubmitted())
}

func TestNight_TimeoutPassesThroughPonderToSleep(t *testing.T) {
	// Reaching AdvancePhase during NightSubAct (the actor never
	// submitted) is the timeout branch. It transitions through
	// NightSubPonder — same as the submit branch — so the audio
	// cadence is uniform across submit/timeout and observers can't
	// distinguish them. nightSubmitted stays false so the room's
	// Ponder function can pick a timeout-appropriate duration.
	g := fixedRoster(t)
	require.Equal(t, game.NightSubAct, g.State().CurrentNightSubPhase())

	evts := advancePhase(t, g) // act → ponder
	ponder, ok := findEvent[game.NightPonderStarted](evts)
	require.True(t, ok, "act timeout must transition into NightPonderStarted")
	require.Equal(t, game.RoleMafia, ponder.Role)
	require.False(t, ponder.Phantom,
		"mafia is alive — Phantom must be false even on timeout")
	require.Equal(t, game.NightSubPonder, g.State().CurrentNightSubPhase())
	require.False(t, g.State().NightTurnSubmitted(),
		"timeout means no submit — NightTurnSubmitted must stay false")

	// One more AdvancePhase: ponder → sleep.
	evts = advancePhase(t, g)
	_, ok = findEvent[game.NightSleepStarted](evts)
	require.True(t, ok, "ponder → sleep must emit NightSleepStarted")
	require.Equal(t, game.NightSubSleep, g.State().CurrentNightSubPhase())
}

func TestNight_TurnOrderMafiaDetectiveDoctor(t *testing.T) {
	g := fixedRoster(t)

	// Mafia submits and turn fully advances through ponder → sleep →
	// settle → detective narrate → detective act.
	nightAction(t, g, "mafia1", "town1")
	walkRestOfTurn(t, g) // ponder → sleep → settle → next role's narrate
	// walkRestOfTurn stops at the next role's act window.
	require.Equal(t, game.RoleDetective, g.State().CurrentNightRole())
	require.Equal(t, game.NightSubAct, g.State().CurrentNightSubPhase())

	// Detective submits.
	evts := nightAction(t, g, "det", "mafia1")
	// DetectiveResult is in the same batch as NightPonderStarted —
	// both come from applyNightAction.
	_, hasResult := findEvent[game.DetectiveResult](evts)
	require.True(t, hasResult, "detective submission emits DetectiveResult immediately")
	_, hasPonder := findEvent[game.NightPonderStarted](evts)
	require.True(t, hasPonder, "detective submission also emits NightPonderStarted")

	// Walk through detective's ponder → sleep → settle → doctor narrate → doctor act.
	walkRestOfTurn(t, g)
	require.Equal(t, game.RoleDoctor, g.State().CurrentNightRole())
	require.Equal(t, game.NightSubAct, g.State().CurrentNightSubPhase())

	// Doctor submits.
	nightAction(t, g, "doc", "town2")
	// Walk through doctor's ponder → sleep → settle → night resolves
	// → PhaseDayDiscussion.
	walkRestOfTurn(t, g)
	require.Equal(t, game.PhaseDayDiscussion, g.State().Phase(),
		"last role's settle resolves the night and transitions to DayDiscussion")
	require.Equal(t, game.Role(""), g.State().CurrentNightRole())
	require.Equal(t, game.NightSubPhase(""), g.State().CurrentNightSubPhase())
}

func TestNight_SkippedRolesGetTimeoutAdvance(t *testing.T) {
	g := fixedRoster(t)
	// All three roles time out.
	for _, want := range []game.Role{game.RoleDetective, game.RoleDoctor} {
		// Currently in <prev>'s act window; skip via AdvancePhase
		// drives act → sleep → settle → next narrate → next act.
		walkRestOfTurn(t, g)
		require.Equal(t, want, g.State().CurrentNightRole())
		require.Equal(t, game.NightSubAct, g.State().CurrentNightSubPhase())
	}
	// Doctor times out: night resolves.
	walkRestOfTurn(t, g)
	require.Equal(t, game.PhaseDayDiscussion, g.State().Phase())
}

func TestNight_DeadRoleEmitsPhantomTurn(t *testing.T) {
	// Phantom turns exist to hide info leakage: the room must not
	// be able to deduce that a role is dead just from missing audio
	// cues. So on subsequent nights we always queue Mafia →
	// Detective → Doctor; turns whose role has no living holder
	// substitute NightSubPonder (with randomized room-side duration)
	// for the act window — narrate and sleep still fire identically.
	g := fixedRoster(t)

	playNight(t, g, map[game.Role]game.PlayerID{
		game.RoleMafia: "det", // detective dies
	})
	require.Equal(t, game.PhaseDayDiscussion, g.State().Phase())

	toNextNight(t, g, "town2", "town1", "doc")
	require.Equal(t, game.PhaseNight, g.State().Phase())
	require.Equal(t, game.RoleMafia, g.State().CurrentNightRole())
	require.Equal(t, game.NightSubAct, g.State().CurrentNightSubPhase())

	// Mafia submits.
	nightAction(t, g, "mafia1", "town1")
	// Walk to the next role; this time, since detective is dead, the
	// detective turn arrives at NightSubPonder (phantom-substituted),
	// NOT NightSubAct.
	walkRestOfTurn(t, g)
	require.Equal(t, game.RoleDetective, g.State().CurrentNightRole())
	require.Equal(t, game.NightSubPonder, g.State().CurrentNightSubPhase(),
		"dead detective: act window is substituted with ponder")
	require.False(t, g.State().NightTurnSubmitted(),
		"phantom ponder is NOT post-submit (no act happened)")

	// A dead detective trying to submit gets ErrPlayerDead (actor
	// alive check fires before sub-phase check).
	_, err := g.Apply(game.NightAction{Actor: "det", Target: "town1"})
	require.ErrorIs(t, err, game.ErrPlayerDead)

	// Walking forward continues to doctor's act window.
	walkRestOfTurn(t, g)
	require.Equal(t, game.RoleDoctor, g.State().CurrentNightRole())
	require.Equal(t, game.NightSubAct, g.State().CurrentNightSubPhase())
}

func TestNight_PhantomTurnEmitsPhantomFlagOnEvents(t *testing.T) {
	// NightNarrationStarted and NightPonderStarted carry Phantom=true
	// for phantom turns so the room can size durations (narrate is
	// still a role's narrate; ponder gets the random phantom window).
	g := fixedRoster(t)

	playNight(t, g, map[game.Role]game.PlayerID{
		game.RoleMafia: "det", // detective dies
	})
	toNextNight(t, g, "town2", "town1", "doc")
	require.Equal(t, game.PhaseNight, g.State().Phase())

	// Submit mafia; in the batch from walkRestOfTurn we should see
	// the detective's narrate AND ponder both flagged Phantom=true.
	nightAction(t, g, "mafia1", "town1")
	evts := walkRestOfTurn(t, g)

	var sawDetNarrate, sawDetPonder bool
	for _, e := range evts {
		switch v := e.(type) {
		case game.NightNarrationStarted:
			if v.Role == game.RoleDetective {
				require.True(t, v.Phantom,
					"detective narrate must be Phantom=true when detective is dead")
				sawDetNarrate = true
			}
		case game.NightPonderStarted:
			if v.Role == game.RoleDetective {
				require.True(t, v.Phantom,
					"detective ponder (phantom-substituted) must be Phantom=true")
				sawDetPonder = true
			}
		}
	}
	require.True(t, sawDetNarrate, "expected NightNarrationStarted for detective")
	require.True(t, sawDetPonder, "expected NightPonderStarted for detective")
}

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

	t.Run("wrong role on wrong turn", func(t *testing.T) {
		g := fixedRoster(t) // currentNightRole = mafia, sub = act
		_, err := g.Apply(game.NightAction{Actor: "det", Target: "mafia1"})
		require.ErrorIs(t, err, game.ErrNotYourTurn,
			"detective cannot act during the mafia's act window")
	})

	t.Run("submission outside act window rejected as ErrNotYourTurn", func(t *testing.T) {
		// During mafia's NARRATE sub-phase the mafia is "current" but
		// the act window is not yet open. Submission must collapse
		// onto the same wrong-time error.
		g := game.New()
		_, err := g.Apply(game.CreateGame{
			GameID: "g1", MinPlayers: 5, MaxPlayers: 20, MafiaCount: 1, Seed: 0,
		})
		require.NoError(t, err)
		for _, id := range []game.PlayerID{"a", "b", "c", "d", "e"} {
			_, err := g.Apply(game.AddPlayer{PlayerID: id, Name: string(id)})
			require.NoError(t, err)
		}
		_, err = g.Apply(game.StartGame{})
		require.NoError(t, err)
		_, err = g.Apply(game.BeginNight{})
		require.NoError(t, err)
		// Walk to mafia narrate but NOT to mafia act.
		_, err = g.Apply(game.AdvancePhase{}) // opening → mafia narrate
		require.NoError(t, err)
		require.Equal(t, game.NightSubNarrate, g.State().CurrentNightSubPhase())

		// Find a mafia actor.
		var mafia, victim game.PlayerID
		for _, p := range g.State().Players() {
			if mafia == "" && p.Role() == game.RoleMafia {
				mafia = p.ID()
			} else if victim == "" && p.Role() == game.RoleVillager {
				victim = p.ID()
			}
		}
		require.NotEmpty(t, mafia)
		require.NotEmpty(t, victim)

		_, err = g.Apply(game.NightAction{Actor: mafia, Target: victim})
		require.ErrorIs(t, err, game.ErrNotYourTurn,
			"submission during narrate (act window closed) must be ErrNotYourTurn")
	})

	t.Run("unknown target rejected", func(t *testing.T) {
		g := fixedRoster(t)
		_, err := g.Apply(game.NightAction{Actor: "mafia1", Target: "ghost"})
		require.ErrorIs(t, err, game.ErrUnknownPlayer)
	})

	t.Run("mafia cannot target mafia", func(t *testing.T) {
		// Two-mafia game so we have two mafia players to test.
		g := game.New()
		_, err := g.Apply(game.CreateGame{
			GameID: "g1", MinPlayers: 5, MaxPlayers: 20, MafiaCount: 2, Seed: 7,
		})
		require.NoError(t, err)
		for _, id := range []game.PlayerID{"a", "b", "c", "d", "e"} {
			_, err := g.Apply(game.AddPlayer{PlayerID: id, Name: string(id)})
			require.NoError(t, err)
		}
		_, err = g.Apply(game.StartGame{})
		require.NoError(t, err)
		_, err = g.Apply(game.BeginNight{})
		require.NoError(t, err)
		advanceToMafiaAct(t, g)

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

	t.Run("mafia faction-collective: second mafia rejected as wrong turn", func(t *testing.T) {
		g := game.New()
		_, err := g.Apply(game.CreateGame{
			GameID: "g1", MinPlayers: 5, MaxPlayers: 20, MafiaCount: 2, Seed: 7,
		})
		require.NoError(t, err)
		for _, id := range []game.PlayerID{"a", "b", "c", "d", "e"} {
			_, err := g.Apply(game.AddPlayer{PlayerID: id, Name: string(id)})
			require.NoError(t, err)
		}
		_, err = g.Apply(game.StartGame{})
		require.NoError(t, err)
		_, err = g.Apply(game.BeginNight{})
		require.NoError(t, err)
		advanceToMafiaAct(t, g)

		var mafias []game.PlayerID
		var townTarget game.PlayerID
		for _, p := range g.State().Players() {
			switch p.Role() {
			case game.RoleMafia:
				mafias = append(mafias, p.ID())
			case game.RoleVillager:
				if townTarget == "" {
					townTarget = p.ID()
				}
			}
		}
		require.Len(t, mafias, 2)
		require.NotEmpty(t, townTarget)

		_, err = g.Apply(game.NightAction{Actor: mafias[0], Target: townTarget})
		require.NoError(t, err)
		// First mafia submission took us to ponder; second mafia
		// submission must now fail with ErrNotYourTurn (the sub-phase
		// is no longer act).
		_, err = g.Apply(game.NightAction{Actor: mafias[1], Target: townTarget})
		require.ErrorIs(t, err, game.ErrNotYourTurn)
	})

	t.Run("detective cannot self-investigate", func(t *testing.T) {
		g := fixedRoster(t)
		// Walk to detective's act window.
		nightAction(t, g, "mafia1", "town1")
		walkRestOfTurn(t, g)
		require.Equal(t, game.RoleDetective, g.State().CurrentNightRole())
		require.Equal(t, game.NightSubAct, g.State().CurrentNightSubPhase())

		_, err := g.Apply(game.NightAction{Actor: "det", Target: "det"})
		require.ErrorIs(t, err, game.ErrSelfTarget)
	})

	t.Run("doctor can self-save on any night including the first", func(t *testing.T) {
		g := fixedRoster(t)
		// Walk to doctor's act window: submit mafia, walk; submit
		// det, walk.
		nightAction(t, g, "mafia1", "town1")
		walkRestOfTurn(t, g)
		nightAction(t, g, "det", "mafia1")
		walkRestOfTurn(t, g)
		require.Equal(t, game.RoleDoctor, g.State().CurrentNightRole())
		require.Equal(t, game.NightSubAct, g.State().CurrentNightSubPhase())

		_, err := g.Apply(game.NightAction{Actor: "doc", Target: "doc"})
		require.NoError(t, err, "doctor self-save should be legal on night 1")
	})

	t.Run("re-submission by same actor is rejected as wrong turn", func(t *testing.T) {
		// In the new turn model, once mafia1 submits, the sub-phase
		// becomes ponder; a second submission gets ErrNotYourTurn
		// (act window closed), NOT ErrAlreadyActed.
		g := fixedRoster(t)
		_, err := g.Apply(game.NightAction{Actor: "mafia1", Target: "town1"})
		require.NoError(t, err)
		_, err = g.Apply(game.NightAction{Actor: "mafia1", Target: "town2"})
		require.ErrorIs(t, err, game.ErrNotYourTurn)
	})

	t.Run("empty target rejected", func(t *testing.T) {
		g := fixedRoster(t)
		_, err := g.Apply(game.NightAction{Actor: "mafia1", Target: ""})
		require.ErrorIs(t, err, game.ErrUnknownPlayer)
	})

	t.Run("dead target rejected", func(t *testing.T) {
		g := fixedRoster(t)
		// Night 1: mafia kills town1, unsaved (doc targets town2).
		playNight(t, g, map[game.Role]game.PlayerID{
			game.RoleMafia:  "town1",
			game.RoleDoctor: "town2",
		})
		toNextNight(t, g, "det", "town2", "doc")
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
		playNight(t, g, map[game.Role]game.PlayerID{
			game.RoleMafia: "det",
		})
		toNextNight(t, g, "town2", "town1", "doc")
		require.Equal(t, game.PhaseNight, g.State().Phase())

		// Mafia acts; walk to detective's (phantom) ponder.
		nightAction(t, g, "mafia1", "town1")
		walkRestOfTurn(t, g)
		require.Equal(t, game.RoleDetective, g.State().CurrentNightRole())
		require.Equal(t, game.NightSubPonder, g.State().CurrentNightSubPhase())

		_, err := g.Apply(game.NightAction{Actor: "det", Target: "town1"})
		require.ErrorIs(t, err, game.ErrPlayerDead)
	})
}

// advanceToMafiaAct walks the engine from "just entered PhaseNight"
// (i.e. just after BeginNight; sub-phase = opening) to the mafia's
// act window. Used by tests that build their own roster instead of
// going through fixedRoster.
func advanceToMafiaAct(t *testing.T, g *game.Game) {
	t.Helper()
	require.Equal(t, game.NightSubOpening, g.State().CurrentNightSubPhase())
	advancePhase(t, g) // opening → mafia narrate
	require.Equal(t, game.NightSubNarrate, g.State().CurrentNightSubPhase())
	advancePhase(t, g) // mafia narrate → mafia act
	require.Equal(t, game.NightSubAct, g.State().CurrentNightSubPhase())
}

// --- Night resolution -----------------------------------------------------

func TestNightResolution(t *testing.T) {
	t.Run("mafia kill takes effect when not saved", func(t *testing.T) {
		g := fixedRoster(t)
		evts := playNight(t, g, map[game.Role]game.PlayerID{
			game.RoleMafia:     "town1",
			game.RoleDetective: "mafia1",
			game.RoleDoctor:    "town2", // saves the wrong person
		})

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
		evts := playNight(t, g, map[game.Role]game.PlayerID{
			game.RoleMafia:  "town1",
			game.RoleDoctor: "town1",
		})

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
		evts := playNight(t, g, map[game.Role]game.PlayerID{
			game.RoleMafia:     "town2",
			game.RoleDetective: "mafia1",
		})
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

func TestDayVote_Validation(t *testing.T) {
	intoDayVote := func(t *testing.T) *game.Game {
		t.Helper()
		g := fixedRoster(t)
		toDayVote(t, g, map[game.Role]game.PlayerID{
			game.RoleMafia:  "town1",
			game.RoleDoctor: "town1", // save
		})
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
		g := fixedRoster(t)
		toDayVote(t, g, map[game.Role]game.PlayerID{
			game.RoleMafia: "town1", // unsaved
		})
		_, err := g.Apply(game.DayVote{Voter: "town1", Target: "mafia1"})
		require.ErrorIs(t, err, game.ErrPlayerDead)
	})

	t.Run("target dead", func(t *testing.T) {
		g := fixedRoster(t)
		toDayVote(t, g, map[game.Role]game.PlayerID{
			game.RoleMafia: "town1", // unsaved
		})
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
	intoDayVote := func(t *testing.T) *game.Game {
		t.Helper()
		g := fixedRoster(t)
		toDayVote(t, g, map[game.Role]game.PlayerID{
			game.RoleMafia:  "town1",
			game.RoleDoctor: "town1", // save -> nobody dies
		})
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
		playNight(t, g, map[game.Role]game.PlayerID{
			game.RoleMafia:  "town1",
			game.RoleDoctor: "town1",
		})
		require.Equal(t, game.PhaseDayDiscussion, g.State().Phase())
		_, err := g.Apply(game.DayVote{Voter: "town1", Target: "mafia1"})
		require.ErrorIs(t, err, game.ErrWrongPhase)
	})
}

// --- Vote resolution (host-driven) ---------------------------------------

func TestVoteResolution(t *testing.T) {
	intoDayVote := func(t *testing.T) *game.Game {
		t.Helper()
		g := fixedRoster(t)
		toDayVote(t, g, map[game.Role]game.PlayerID{
			game.RoleMafia:  "town1",
			game.RoleDoctor: "town1", // save -> nobody dies
		})
		return g
	}

	t.Run("decisive plurality: FinalizeVotes lynches target", func(t *testing.T) {
		g := intoDayVote(t)
		_, _ = g.Apply(game.DayVote{Voter: "town1", Target: "mafia1"})
		_, _ = g.Apply(game.DayVote{Voter: "town2", Target: "mafia1"})
		_, _ = g.Apply(game.DayVote{Voter: "det", Target: "mafia1"})

		evts, err := g.Apply(game.FinalizeVotes{})
		require.NoError(t, err)
		l, ok := findEvent[game.PlayerLynched](evts)
		require.True(t, ok)
		require.Equal(t, game.PlayerID("mafia1"), l.PlayerID)
		require.Equal(t, "public", l.Visibility().Audience)
	})

	t.Run("FinalizeVotes on tie rejected with ErrNoChange", func(t *testing.T) {
		g := intoDayVote(t)
		_, _ = g.Apply(game.DayVote{Voter: "town1", Target: "mafia1"})
		_, _ = g.Apply(game.DayVote{Voter: "town2", Target: "det"})

		_, err := g.Apply(game.FinalizeVotes{})
		require.ErrorIs(t, err, game.ErrNoChange,
			"tie has no plurality; FinalizeVotes must fail")
		require.Equal(t, game.PhaseDayVote, g.State().Phase(),
			"phase unchanged when finalize is rejected")
	})

	t.Run("FinalizeVotes on empty tally rejected", func(t *testing.T) {
		g := intoDayVote(t)
		_, err := g.Apply(game.FinalizeVotes{})
		require.ErrorIs(t, err, game.ErrNoChange)
	})

	t.Run("ClearVotes wipes tally and stays in DayVote", func(t *testing.T) {
		g := intoDayVote(t)
		_, _ = g.Apply(game.DayVote{Voter: "town1", Target: "mafia1"})
		_, _ = g.Apply(game.DayVote{Voter: "town2", Target: "det"})

		evts, err := g.Apply(game.ClearVotes{})
		require.NoError(t, err)
		_, ok := findEvent[game.VoteCleared](evts)
		require.True(t, ok)
		require.Equal(t, game.PhaseDayVote, g.State().Phase())

		_, err = g.Apply(game.DayVote{Voter: "town1", Target: "mafia1"})
		require.NoError(t, err)
	})

	t.Run("ClearVotes on empty tally rejected", func(t *testing.T) {
		g := intoDayVote(t)
		_, err := g.Apply(game.ClearVotes{})
		require.ErrorIs(t, err, game.ErrNoChange)
	})

	t.Run("re-vote after clear can produce a decisive lynch", func(t *testing.T) {
		g := intoDayVote(t)
		_, _ = g.Apply(game.DayVote{Voter: "town1", Target: "mafia1"})
		_, _ = g.Apply(game.DayVote{Voter: "town2", Target: "det"})
		_, _ = g.Apply(game.ClearVotes{})

		_, _ = g.Apply(game.DayVote{Voter: "town1", Target: "mafia1"})
		_, _ = g.Apply(game.DayVote{Voter: "town2", Target: "mafia1"})
		_, _ = g.Apply(game.DayVote{Voter: "det", Target: "mafia1"})

		evts, err := g.Apply(game.FinalizeVotes{})
		require.NoError(t, err)
		l, ok := findEvent[game.PlayerLynched](evts)
		require.True(t, ok)
		require.Equal(t, game.PlayerID("mafia1"), l.PlayerID)
	})

	t.Run("after lynch: dayLynchResolved set, OpenVoting rejected, BeginNight ok", func(t *testing.T) {
		g := intoDayVote(t)
		_, _ = g.Apply(game.DayVote{Voter: "town1", Target: "det"})
		_, _ = g.Apply(game.DayVote{Voter: "town2", Target: "det"})
		_, _ = g.Apply(game.DayVote{Voter: "mafia1", Target: "det"})

		_, err := g.Apply(game.FinalizeVotes{})
		require.NoError(t, err)
		require.Equal(t, game.PhaseDayDiscussion, g.State().Phase())
		require.True(t, g.State().DayLynchResolved())

		_, err = g.Apply(game.OpenVoting{})
		require.ErrorIs(t, err, game.ErrWrongPhase)

		_, err = g.Apply(game.BeginNight{})
		require.NoError(t, err)
		require.Equal(t, game.PhaseNight, g.State().Phase())
		require.False(t, g.State().DayLynchResolved(),
			"BeginNight clears the lynch-resolved flag")
		// And it begins with an opening sub-phase, not a narrate.
		require.Equal(t, game.NightSubOpening, g.State().CurrentNightSubPhase())
	})
}

// --- Phase machine guard rails -------------------------------------------

func TestAdvancePhase_Guards(t *testing.T) {
	t.Run("Lobby cannot advance via AdvancePhase (internal-only)", func(t *testing.T) {
		g := game.New()
		_, err := g.Apply(game.AdvancePhase{})
		require.ErrorIs(t, err, game.ErrWrongPhase)
	})

	t.Run("DayDiscussion / DayVote reject AdvancePhase", func(t *testing.T) {
		g := fixedRoster(t)
		playNight(t, g, map[game.Role]game.PlayerID{
			game.RoleMafia:  "town1",
			game.RoleDoctor: "town1",
		})
		require.Equal(t, game.PhaseDayDiscussion, g.State().Phase())
		_, err := g.Apply(game.AdvancePhase{})
		require.ErrorIs(t, err, game.ErrWrongPhase)

		_, err = g.Apply(game.OpenVoting{})
		require.NoError(t, err)
		require.Equal(t, game.PhaseDayVote, g.State().Phase())
		_, err = g.Apply(game.AdvancePhase{})
		require.ErrorIs(t, err, game.ErrWrongPhase)
	})

	t.Run("Ended rejects further commands", func(t *testing.T) {
		// 5 players + 2 mafia. One unsaved kill drops town to 2,
		// matching mafia → mafia wins.
		g := game.New()
		_, err := g.Apply(game.CreateGame{
			GameID: "g1", MinPlayers: 5, MaxPlayers: 20, MafiaCount: 2, Seed: 1,
		})
		require.NoError(t, err)
		for _, id := range []game.PlayerID{"a", "b", "c", "d", "e"} {
			_, err := g.Apply(game.AddPlayer{PlayerID: id, Name: string(id)})
			require.NoError(t, err)
		}
		_, err = g.Apply(game.StartGame{})
		require.NoError(t, err)
		_, err = g.Apply(game.BeginNight{})
		require.NoError(t, err)
		advanceToMafiaAct(t, g)

		var mafia, victim game.PlayerID
		for _, p := range g.State().Players() {
			if mafia == "" && p.Role() == game.RoleMafia {
				mafia = p.ID()
			} else if victim == "" && p.Role() == game.RoleVillager {
				victim = p.ID()
			}
		}
		require.NotEmpty(t, mafia)
		require.NotEmpty(t, victim)

		_, err = g.Apply(game.NightAction{Actor: mafia, Target: victim})
		require.NoError(t, err)
		// Walk through everything until the night resolves.
		// walkRestOfTurn stops at the NEXT role's act window, so it
		// takes three walks total to clear three roles:
		//   walk 1: mafia submit → det's act
		//   walk 2: det skip → doc's act
		//   walk 3: doc skip → resolve → ended
		walkRestOfTurn(t, g)
		walkRestOfTurn(t, g)
		walkRestOfTurn(t, g)
		require.Equal(t, game.PhaseEnded, g.State().Phase())

		_, err = g.Apply(game.AdvancePhase{})
		require.ErrorIs(t, err, game.ErrGameEnded)
	})
}

// TestPhaseEnded_AllCommandsReturnErrGameEnded walks every public
// command type against a finished game and asserts each one returns
// ErrGameEnded (not the generic ErrWrongPhase). The wire layer maps
// ErrGameEnded to wire.ErrCodeGameEnded and joinErrorFor surfaces it
// as "This game has already ended.", which is what users see when
// they try to interact with a room that's over.
//
// Adding a new command? Append it to the table below; the test will
// fail loudly until the corresponding apply* handler checks PhaseEnded
// first.
func TestPhaseEnded_AllCommandsReturnErrGameEnded(t *testing.T) {
	mkEnded := func(t *testing.T) *game.Game {
		t.Helper()
		g := game.New()
		_, err := g.Apply(game.CreateGame{
			GameID: "g1", MinPlayers: 5, MaxPlayers: 20, MafiaCount: 2, Seed: 1,
		})
		require.NoError(t, err)
		for _, id := range []game.PlayerID{"a", "b", "c", "d", "e"} {
			_, err := g.Apply(game.AddPlayer{PlayerID: id, Name: string(id)})
			require.NoError(t, err)
		}
		_, err = g.Apply(game.StartGame{})
		require.NoError(t, err)
		_, err = g.Apply(game.BeginNight{})
		require.NoError(t, err)
		advanceToMafiaAct(t, g)

		var mafia, victim game.PlayerID
		for _, p := range g.State().Players() {
			if mafia == "" && p.Role() == game.RoleMafia {
				mafia = p.ID()
			} else if victim == "" && p.Role() == game.RoleVillager {
				victim = p.ID()
			}
		}
		_, _ = g.Apply(game.NightAction{Actor: mafia, Target: victim})
		walkRestOfTurn(t, g) // mafia → det's act
		walkRestOfTurn(t, g) // det skip → doc's act
		walkRestOfTurn(t, g) // doc skip → resolve → ended
		require.Equal(t, game.PhaseEnded, g.State().Phase(),
			"precondition: game must be in PhaseEnded")
		return g
	}

	cases := []struct {
		name string
		cmd  game.Command
	}{
		{"AddPlayer", game.AddPlayer{PlayerID: "z", Name: "Zed"}},
		{"SetMafiaCount", game.SetMafiaCount{Count: 2}},
		{"StartGame", game.StartGame{}},
		{"BeginNight", game.BeginNight{}},
		{"OpenVoting", game.OpenVoting{}},
		{"ClearVotes", game.ClearVotes{}},
		{"FinalizeVotes", game.FinalizeVotes{}},
		{"NightAction", game.NightAction{Actor: "a", Target: "b"}},
		{"DayVote", game.DayVote{Voter: "a", Target: "b"}},
		{"AdvancePhase", game.AdvancePhase{}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g := mkEnded(t)
			_, err := g.Apply(tc.cmd)
			require.ErrorIs(t, err, game.ErrGameEnded,
				"%T against PhaseEnded must return ErrGameEnded, got %v",
				tc.cmd, err)
			require.Equal(t, game.PhaseEnded, g.State().Phase(),
				"rejected %T must not change phase", tc.cmd)
		})
	}
}

// --- Win conditions -------------------------------------------------------

func TestWinConditions(t *testing.T) {
	t.Run("town wins when last mafia is lynched", func(t *testing.T) {
		g := fixedRoster(t)
		toDayVote(t, g, map[game.Role]game.PlayerID{
			game.RoleMafia:  "town1",
			game.RoleDoctor: "town1", // save
		})

		_, _ = g.Apply(game.DayVote{Voter: "town1", Target: "mafia1"})
		_, _ = g.Apply(game.DayVote{Voter: "town2", Target: "mafia1"})
		_, _ = g.Apply(game.DayVote{Voter: "det", Target: "mafia1"})
		_, _ = g.Apply(game.DayVote{Voter: "doc", Target: "mafia1"})

		evts, err := g.Apply(game.FinalizeVotes{})
		require.NoError(t, err)
		ge, ok := findEvent[game.GameEnded](evts)
		require.True(t, ok)
		require.Equal(t, game.FactionTown, ge.Winner)
		require.Equal(t, game.PhaseEnded, g.State().Phase())
	})

	t.Run("FinalRoles is only present at game end", func(t *testing.T) {
		g := fixedRoster(t)
		evts := playNight(t, g, map[game.Role]game.PlayerID{
			game.RoleMafia: "town1", // unsaved; mafia=1 town=3 still alive
		})
		_, ended := findEvent[game.GameEnded](evts)
		require.False(t, ended, "with mafia=1 town=3 alive, no win yet")
	})
}

// TestNight_MafiaTurnNeverPhantom pins the invariant documented on
// beginNextNightTurn: the mafia's night turn is never phantom. The
// reasoning is that checkWin ends the game the instant living mafia
// reaches zero, so the engine never begins a night with no living
// mafia. This guards that reasoning against a future change to the win
// conditions that would silently let a phantom mafia turn slip through
// (narrating "Mafia, wake up" to a room with no mafia to act).
func TestNight_MafiaTurnNeverPhantom(t *testing.T) {
	g := game.New()
	_, err := g.Apply(game.CreateGame{
		GameID: "g1", MinPlayers: 5, MaxPlayers: 20, MafiaCount: 1, Seed: 7,
	})
	require.NoError(t, err)
	for _, id := range []game.PlayerID{"a", "b", "c", "d", "e"} {
		_, err := g.Apply(game.AddPlayer{PlayerID: id, Name: string(id)})
		require.NoError(t, err)
	}
	_, err = g.Apply(game.StartGame{})
	require.NoError(t, err)
	_, err = g.Apply(game.BeginNight{})
	require.NoError(t, err)

	// Opening → first role's narrate. Mafia is always first in the
	// canonical queue, and the game just started, so a mafia is alive.
	evts := advancePhase(t, g)
	require.Equal(t, game.RoleMafia, g.State().CurrentNightRole())

	nn, ok := findEvent[game.NightNarrationStarted](evts)
	require.True(t, ok, "opening should advance into the mafia's narrate")
	require.Equal(t, game.RoleMafia, nn.Role)
	require.False(t, nn.Phantom,
		"mafia narrate must never be phantom: a live game always has a living mafia")
	require.True(t, g.State().HasLivingRole(game.RoleMafia))

	// And the act window opens (not the phantom-substitute ponder).
	advancePhase(t, g) // narrate → act
	require.Equal(t, game.NightSubAct, g.State().CurrentNightSubPhase(),
		"living mafia gets a real act window, not a phantom ponder")
}
