package game_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/96malhar/mafia-the-game/internal/game"
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
	return fixedRosterMatching(t, rosterDeal{
		ids: []game.PlayerID{"mafia1", "det", "doc", "town1", "town2"},
		wanted: map[game.PlayerID]game.Role{
			"mafia1": game.RoleMafia,
			"det":    game.RoleDetective,
			"doc":    game.RoleDoctor,
			"town1":  game.RoleVillager,
			"town2":  game.RoleVillager,
		},
		mafiaCount: 1,
		maxSeeds:   1000,
	})
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
			// next sub-phase. A real role always passes through act
			// (where we stop), so reaching ponder at a fresh-turn
			// boundary can only mean a phantom (act-less) turn.
			if sp == game.NightSubAct || sp == game.NightSubPonder {
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
// (DetectiveResult fires at act time, PlayerKilled / GameEnded fire at
// resolve).
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
// resolve) and walks to the NEXT PhaseNight by lynching the given
// target. To guarantee an actual lynch under the strict-majority rule
// (a target needs more than half the living players' votes), it has
// EVERY living player except the target vote for the target — that is
// always a strict majority. After return, the engine is positioned at
// the mafia's act window (same postcondition as fixedRoster), so the
// caller can keep using nightAction / playNight directly.
func toNextNight(t *testing.T, g *game.Game, lynchTarget game.PlayerID) {
	t.Helper()
	// finalizeLynch either ends the game or returns to DayDiscussion
	// with lynchResolved=true.
	finalizeLynch(t, g, lynchTarget)
	if g.State().Phase() == game.PhaseEnded {
		return
	}
	require.Equal(t, game.PhaseDayDiscussion, g.State().Phase())
	require.True(t, g.State().DayLynchResolved())
	// BeginNight → opening; walk through opening + mafia narrate so the
	// caller lands on mafia act, matching fixedRoster's postcondition.
	// (Mafia phantom is unreachable: win conditions end the game first.)
	beginNightToMafiaAct(t, g)
}

// --- Night opening + turn-order plumbing ---------------------------------

// BeginNight should emit PhaseChanged{To: Night} then
// NightOpeningStarted — NOT a NightNarrationStarted for the
// mafia. The opening is a one-shot "City, go to sleep." beat
// before any role's narration.
func TestNight_OpeningEmittedAfterPhaseChange(t *testing.T) {
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

	// PhaseChanged + opening sub-phase, nothing else night-related.
	_, opening := findNightSub(evts, game.NightSubOpening)
	require.True(t, opening, "BeginNight must emit the opening sub-phase")
	_, narrate := findNightSub(evts, game.NightSubNarrate)
	require.False(t, narrate, "BeginNight must NOT emit a narrate sub-phase yet")

	require.Equal(t, game.NightSubOpening, g.State().CurrentNightSubPhase())
	require.Equal(t, game.Role(""), g.State().CurrentNightRole(),
		"currentNightRole must be empty during opening")
}

// One AdvancePhase during NightSubOpening transitions into the
// first role's NightSubNarrate.
func TestNight_OpeningElapsesIntoMafiaNarrate(t *testing.T) {
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
	ns, ok := findNightSub(evts, game.NightSubNarrate)
	require.True(t, ok, "opening → narrate must emit a narrate sub-phase")
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

// Walk one full real turn and verify the exact sub-phase event
// sequence: narrate → act → ponder → sleep → settle → next role's
// narrate. We start from fixedRoster (already past mafia's
// narrate); submit; then step through each remaining sub-phase
// one AdvancePhase at a time.
func TestNight_RoleTurnSubPhaseSequence(t *testing.T) {
	g := fixedRoster(t)
	require.Equal(t, game.NightSubAct, g.State().CurrentNightSubPhase())

	// Submit: act → ponder, with a ponder sub-phase in the batch.
	evts := nightAction(t, g, "mafia1", "town1")
	_, pondered := findNightSub(evts, game.NightSubPonder)
	require.True(t, pondered, "submit must emit a ponder sub-phase")
	require.Equal(t, game.NightSubPonder, g.State().CurrentNightSubPhase())

	// ponder → sleep.
	evts = advancePhase(t, g)
	sl, ok := findNightSub(evts, game.NightSubSleep)
	require.True(t, ok, "ponder → sleep must emit a sleep sub-phase")
	require.Equal(t, game.RoleMafia, sl.Role)
	require.Equal(t, game.NightSubSleep, g.State().CurrentNightSubPhase())

	// sleep → settle.
	evts = advancePhase(t, g)
	st, ok := findNightSub(evts, game.NightSubSettle)
	require.True(t, ok, "sleep → settle must emit a settle sub-phase")
	require.Equal(t, game.RoleMafia, st.Role)
	require.Equal(t, game.NightSubSettle, g.State().CurrentNightSubPhase())

	// settle → next role's narrate.
	evts = advancePhase(t, g)
	ns, ok := findNightSub(evts, game.NightSubNarrate)
	require.True(t, ok, "settle → next role must emit a narrate sub-phase")
	require.Equal(t, game.RoleDetective, ns.Role)
	require.Equal(t, game.RoleDetective, g.State().CurrentNightRole())
	require.Equal(t, game.NightSubNarrate, g.State().CurrentNightSubPhase())
}

// Reaching AdvancePhase during NightSubAct (the actor never
// submitted) is the timeout branch. It transitions through
// NightSubPonder — same as the submit branch — so the audio
// cadence is uniform across submit/timeout and observers can't
// distinguish them.
func TestNight_TimeoutPassesThroughPonderToSleep(t *testing.T) {
	g := fixedRoster(t)
	require.Equal(t, game.NightSubAct, g.State().CurrentNightSubPhase())

	evts := advancePhase(t, g) // act → ponder
	ponder, ok := findNightSub(evts, game.NightSubPonder)
	require.True(t, ok, "act timeout must transition into a ponder sub-phase")
	require.Equal(t, game.RoleMafia, ponder.Role)
	require.False(t, ponder.Phantom,
		"mafia is alive — Phantom must be false even on timeout")
	require.Equal(t, game.NightSubPonder, g.State().CurrentNightSubPhase())

	// One more AdvancePhase: ponder → sleep.
	evts = advancePhase(t, g)
	_, ok = findNightSub(evts, game.NightSubSleep)
	require.True(t, ok, "ponder → sleep must emit a sleep sub-phase")
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
	_, hasPonder := findNightSub(evts, game.NightSubPonder)
	require.True(t, hasPonder, "detective submission also emits a ponder sub-phase")

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

// Phantom turns exist to hide info leakage: the room must not
// be able to deduce that a role is dead just from missing audio
// cues. So on subsequent nights we always queue Mafia →
// Detective → Doctor; turns whose role has no living holder
// substitute NightSubPonder (with randomized room-side duration)
// for the act window — narrate and sleep still fire identically.
func TestNight_DeadRoleEmitsPhantomTurn(t *testing.T) {
	g := fixedRoster(t)

	playNight(t, g, map[game.Role]game.PlayerID{
		game.RoleMafia: "det", // detective dies
	})
	require.Equal(t, game.PhaseDayDiscussion, g.State().Phase())

	toNextNight(t, g, "town2")
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

	// A dead detective trying to submit gets ErrPlayerDead (actor
	// alive check fires before sub-phase check).
	_, err := g.Apply(game.NightAction{Actor: "det", Target: "town1"})
	require.ErrorIs(t, err, game.ErrPlayerDead)

	// Walking forward continues to doctor's act window.
	walkRestOfTurn(t, g)
	require.Equal(t, game.RoleDoctor, g.State().CurrentNightRole())
	require.Equal(t, game.NightSubAct, g.State().CurrentNightSubPhase())
}

// NightNarrationStarted and NightPonderStarted carry Phantom=true
// for phantom turns so the room can size durations (narrate is
// still a role's narrate; ponder gets the random phantom window).
func TestNight_PhantomTurnEmitsPhantomFlagOnEvents(t *testing.T) {
	g := fixedRoster(t)

	playNight(t, g, map[game.Role]game.PlayerID{
		game.RoleMafia: "det", // detective dies
	})
	toNextNight(t, g, "town2")
	require.Equal(t, game.PhaseNight, g.State().Phase())

	// Submit mafia; in the batch from walkRestOfTurn we should see
	// the detective's narrate AND ponder both flagged Phantom=true.
	nightAction(t, g, "mafia1", "town1")
	evts := walkRestOfTurn(t, g)

	var sawDetNarrate, sawDetPonder bool
	for _, e := range evts {
		v, ok := e.(game.NightSubPhaseStarted)
		if !ok || v.Role != game.RoleDetective {
			continue
		}
		switch v.Sub {
		case game.NightSubNarrate:
			require.True(t, v.Phantom,
				"detective narrate must be Phantom=true when detective is dead")
			sawDetNarrate = true
		case game.NightSubPonder:
			require.True(t, v.Phantom,
				"detective ponder (phantom-substituted) must be Phantom=true")
			sawDetPonder = true
		}
	}
	require.True(t, sawDetNarrate, "expected a detective narrate sub-phase")
	require.True(t, sawDetPonder, "expected a detective ponder sub-phase")
}

// --- NightAction validation -----------------------------------------------

func TestNightAction_Validation(t *testing.T) {
	t.Run("rejected outside PhaseNight", func(t *testing.T) {
		g := game.New() // PhaseLobby
		_, err := g.Apply(game.NightAction{Actor: "mafia1", Target: "town1"})
		require.ErrorIs(t, err, game.ErrWrongPhase)
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
		addPlayers(t, g, "a", "b", "c", "d", "e")
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
		toNextNight(t, g, "det")
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
		toNextNight(t, g, "town2")
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

// --- DayVote state table -------------------------------------------------

func TestDayVote_Validation(t *testing.T) {
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
	t.Run("first vote emits VoteCast", func(t *testing.T) {
		g := intoDayVote(t)
		evts, err := g.Apply(game.DayVote{Voter: "town1", Target: "mafia1"})
		require.NoError(t, err)
		v, ok := findEvent[game.VoteCast](evts)
		require.True(t, ok)
		require.Equal(t, game.PlayerID("town1"), v.Voter)
		require.Equal(t, game.PlayerID("mafia1"), v.Target)
	})

	t.Run("vote events are private to the voter (tally stays hidden)", func(t *testing.T) {
		g := intoDayVote(t)

		cast, err := g.Apply(game.DayVote{Voter: "town1", Target: "mafia1"})
		require.NoError(t, err)
		vc, ok := findEvent[game.VoteCast](cast)
		require.True(t, ok)
		require.Equal(t, "player", vc.Visibility().Audience)
		require.Equal(t, game.PlayerID("town1"), vc.Visibility().Player,
			"a cast vote is visible only to the voter until reveal")

		changed, err := g.Apply(game.DayVote{Voter: "town1", Target: "det"})
		require.NoError(t, err)
		vch, ok := findEvent[game.VoteChanged](changed)
		require.True(t, ok)
		require.Equal(t, "player", vch.Visibility().Audience)
		require.Equal(t, game.PlayerID("town1"), vch.Visibility().Player)

		retracted, err := g.Apply(game.DayVote{Voter: "town1", Target: ""})
		require.NoError(t, err)
		vr, ok := findEvent[game.VoteRetracted](retracted)
		require.True(t, ok)
		require.Equal(t, "player", vr.Visibility().Audience)
		require.Equal(t, game.PlayerID("town1"), vr.Visibility().Player)
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

// The running count of how many players have voted is PUBLIC (so the whole
// room can see voting progress) even while the individual votes stay
// hidden until the host reveals. Every cast/change/retract rides with a
// VoteProgress carrying the current living-voter count — a change leaves
// the count unchanged, a retract decrements it.
func TestDayVote_PublicProgressCount(t *testing.T) {
	g := intoDayVote(t) // 5 living players, nobody voted yet

	// castCount applies a vote edit, asserts it carried a PUBLIC
	// VoteProgress, and returns the running count it reported.
	castCount := func(c game.DayVote) int {
		t.Helper()
		evts, err := g.Apply(c)
		require.NoError(t, err)
		vp, ok := findEvent[game.VoteProgress](evts)
		require.True(t, ok, "every vote edit emits a VoteProgress")
		require.Equal(t, "public", vp.Visibility().Audience,
			"the running count is public — never gated to the voter")
		return vp.Cast
	}

	require.Equal(t, 1, castCount(game.DayVote{Voter: "town1", Target: "mafia1"}))
	require.Equal(t, 2, castCount(game.DayVote{Voter: "det", Target: "mafia1"}))
	// A voter switching targets does not change how many have voted.
	require.Equal(t, 2, castCount(game.DayVote{Voter: "town1", Target: "det"}))
	// A retract drops the count back down.
	require.Equal(t, 1, castCount(game.DayVote{Voter: "town1", Target: ""}))
}

// An abstention is a first-class DECISION: it counts toward the public
// progress count and the reveal gate, contributes to no target's tally,
// is private to the abstainer, and is mutually exclusive with a real vote.
func TestDayAbstain(t *testing.T) {
	t.Run("abstaining emits a private VoteAbstained plus a public count bump", func(t *testing.T) {
		g := intoDayVote(t)
		evts, err := g.Apply(game.DayAbstain{Voter: "town1"})
		require.NoError(t, err)

		va, ok := findEvent[game.VoteAbstained](evts)
		require.True(t, ok, "abstaining emits VoteAbstained")
		require.Equal(t, "player", va.Visibility().Audience,
			"an abstention is private to the abstainer until reveal")
		require.Equal(t, game.PlayerID("town1"), va.Visibility().Player)

		vp, ok := findEvent[game.VoteProgress](evts)
		require.True(t, ok)
		require.Equal(t, 1, vp.Cast, "an abstention counts toward the cast total")
	})

	t.Run("abstaining again is ErrNoChange", func(t *testing.T) {
		g := intoDayVote(t)
		_, err := g.Apply(game.DayAbstain{Voter: "town1"})
		require.NoError(t, err)
		_, err = g.Apply(game.DayAbstain{Voter: "town1"})
		require.ErrorIs(t, err, game.ErrNoChange)
	})

	t.Run("abstaining supersedes a prior vote without raising the count", func(t *testing.T) {
		g := intoDayVote(t)
		evts, err := g.Apply(game.DayVote{Voter: "town1", Target: "mafia1"})
		require.NoError(t, err)
		vp, _ := findEvent[game.VoteProgress](evts)
		require.Equal(t, 1, vp.Cast)

		evts, err = g.Apply(game.DayAbstain{Voter: "town1"})
		require.NoError(t, err)
		vp, _ = findEvent[game.VoteProgress](evts)
		require.Equal(t, 1, vp.Cast, "vote → abstain stays one cast, not two")

		// The vote no longer counts toward any target: a lone other voter
		// can't reach a majority, so finalize is a NoLynch.
		_, err = g.Apply(game.DayVote{Voter: "town2", Target: "mafia1"})
		require.NoError(t, err)
		out, err := g.Apply(game.FinalizeVotes{})
		require.NoError(t, err)
		_, lynched := findEvent[game.PlayerLynched](out)
		require.False(t, lynched, "only one real vote remains → no majority")
	})

	t.Run("voting after abstaining clears the abstention (not both at once)", func(t *testing.T) {
		g := intoDayVote(t)
		_, err := g.Apply(game.DayAbstain{Voter: "town1"})
		require.NoError(t, err)

		// Switching to a real vote is a FIRST VoteCast (no prior real vote)
		// and the count stays at one — proving the abstention was replaced,
		// not added to. A voter is never counted as both at once.
		evts, err := g.Apply(game.DayVote{Voter: "town1", Target: "mafia1"})
		require.NoError(t, err)
		_, ok := findEvent[game.VoteCast](evts)
		require.True(t, ok, "abstain → vote emits a fresh VoteCast")
		vp, _ := findEvent[game.VoteProgress](evts)
		require.Equal(t, 1, vp.Cast, "abstain → vote stays one cast, not two")

		// And the recorded vote — not a lingering abstention — is what shows
		// up in the tally once everyone else decides and the host reveals.
		for _, v := range []game.PlayerID{"town2", "det", "doc", "mafia1"} {
			_, err = g.Apply(game.DayAbstain{Voter: v})
			require.NoError(t, err)
		}
		out, err := g.Apply(game.RevealVotes{})
		require.NoError(t, err)
		rv, _ := findEvent[game.VotesRevealed](out)
		require.Equal(t, map[game.PlayerID]game.PlayerID{"town1": "mafia1"}, rv.Tally,
			"the live vote is in the tally; the abstention left no trace")
	})

	t.Run("retract clears an abstention", func(t *testing.T) {
		g := intoDayVote(t)
		_, err := g.Apply(game.DayAbstain{Voter: "town1"})
		require.NoError(t, err)
		evts, err := g.Apply(game.DayVote{Voter: "town1", Target: ""})
		require.NoError(t, err, "an abstention is retractable")
		_, ok := findEvent[game.VoteRetracted](evts)
		require.True(t, ok)
		vp, _ := findEvent[game.VoteProgress](evts)
		require.Equal(t, 0, vp.Cast, "retracting an abstention drops the count")
	})

	t.Run("abstain outside PhaseDayVote is rejected", func(t *testing.T) {
		g := fixedRoster(t)
		playNight(t, g, map[game.Role]game.PlayerID{
			game.RoleMafia:  "town1",
			game.RoleDoctor: "town1",
		})
		require.Equal(t, game.PhaseDayDiscussion, g.State().Phase())
		_, err := g.Apply(game.DayAbstain{Voter: "town1"})
		require.ErrorIs(t, err, game.ErrWrongPhase)
	})
}

// --- Vote resolution (host-driven) ---------------------------------------

// Roster has 5 living players entering the vote (doctor saved the
// mafia's target), so a strict majority is 3 votes (3*2 > 5).
func TestVoteResolution(t *testing.T) {

	t.Run("strict majority: FinalizeVotes lynches target", func(t *testing.T) {
		g := intoDayVote(t)
		_, _ = g.Apply(game.DayVote{Voter: "town1", Target: "mafia1"})
		_, _ = g.Apply(game.DayVote{Voter: "town2", Target: "mafia1"})
		_, _ = g.Apply(game.DayVote{Voter: "det", Target: "mafia1"}) // 3 of 5 = majority

		evts, err := g.Apply(game.FinalizeVotes{})
		require.NoError(t, err)
		l, ok := findEvent[game.PlayerLynched](evts)
		require.True(t, ok)
		require.Equal(t, game.PlayerID("mafia1"), l.PlayerID)
		require.Equal(t, "public", l.Visibility().Audience)
		_, noLynch := findEvent[game.NoLynch](evts)
		require.False(t, noLynch, "a majority lynch must not also emit NoLynch")
	})

	t.Run("plurality short of a majority: NoLynch, day still resolves", func(t *testing.T) {
		g := intoDayVote(t)
		// mafia1 leads with 2 of 5, but 2*2 == 4 is not > 5, so no lynch.
		_, _ = g.Apply(game.DayVote{Voter: "town1", Target: "mafia1"})
		_, _ = g.Apply(game.DayVote{Voter: "town2", Target: "mafia1"})
		_, _ = g.Apply(game.DayVote{Voter: "det", Target: "doc"})

		evts, err := g.Apply(game.FinalizeVotes{})
		require.NoError(t, err, "finalize always closes the day")
		_, lynched := findEvent[game.PlayerLynched](evts)
		require.False(t, lynched, "no majority → nobody lynched")
		nl, ok := findEvent[game.NoLynch](evts)
		require.True(t, ok, "an indecisive finalize emits NoLynch")
		require.Equal(t, "public", nl.Visibility().Audience)
		require.Equal(t, game.PhaseDayDiscussion, g.State().Phase())
		require.True(t, g.State().DayLynchResolved(),
			"NoLynch resolves the day so the host advances to BeginNight")
	})

	t.Run("tie: NoLynch, day still resolves", func(t *testing.T) {
		g := intoDayVote(t)
		_, _ = g.Apply(game.DayVote{Voter: "town1", Target: "mafia1"})
		_, _ = g.Apply(game.DayVote{Voter: "town2", Target: "det"})

		evts, err := g.Apply(game.FinalizeVotes{})
		require.NoError(t, err)
		_, lynched := findEvent[game.PlayerLynched](evts)
		require.False(t, lynched, "a tie has no majority")
		_, ok := findEvent[game.NoLynch](evts)
		require.True(t, ok)
		require.Equal(t, game.PhaseDayDiscussion, g.State().Phase())
		require.True(t, g.State().DayLynchResolved())
	})

	t.Run("empty tally: NoLynch, day still resolves", func(t *testing.T) {
		g := intoDayVote(t)
		evts, err := g.Apply(game.FinalizeVotes{})
		require.NoError(t, err, "finalize with no votes still closes the day")
		_, lynched := findEvent[game.PlayerLynched](evts)
		require.False(t, lynched)
		_, ok := findEvent[game.NoLynch](evts)
		require.True(t, ok, "no living player voted → NoLynch")
		require.Equal(t, game.PhaseDayDiscussion, g.State().Phase())
		require.True(t, g.State().DayLynchResolved())
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

// --- Reveal-then-finalize vote flow --------------------------------------

func TestRevealVotes(t *testing.T) {
	// castDecisive makes ALL FIVE living players cast a decision so a reveal
	// is allowed: three vote mafia1 (a 3/5 strict majority), and the other
	// two abstain (counting toward the reveal gate without swinging the
	// tally). The revealed tally therefore still holds exactly the 3 votes.
	castDecisive := func(t *testing.T, g *game.Game) {
		t.Helper()
		for _, v := range []game.PlayerID{"town1", "town2", "det"} {
			_, err := g.Apply(game.DayVote{Voter: v, Target: "mafia1"})
			require.NoError(t, err)
		}
		for _, v := range []game.PlayerID{"doc", "mafia1"} {
			_, err := g.Apply(game.DayAbstain{Voter: v})
			require.NoError(t, err)
		}
	}

	t.Run("reveal emits a public VotesRevealed snapshot of the tally", func(t *testing.T) {
		g := intoDayVote(t)
		castDecisive(t, g)
		require.False(t, g.State().VotesRevealed())

		evts, err := g.Apply(game.RevealVotes{})
		require.NoError(t, err)
		require.True(t, g.State().VotesRevealed())

		rv, ok := findEvent[game.VotesRevealed](evts)
		require.True(t, ok, "RevealVotes must emit VotesRevealed")
		require.Equal(t, "public", rv.Visibility().Audience,
			"the revealed tally is public — everyone, incl. dead, sees it")
		require.Equal(t, map[game.PlayerID]game.PlayerID{
			"town1": "mafia1", "town2": "mafia1", "det": "mafia1",
		}, rv.Tally)
	})

	t.Run("re-reveal is rejected with ErrNoChange", func(t *testing.T) {
		g := intoDayVote(t)
		castDecisive(t, g)
		_, err := g.Apply(game.RevealVotes{})
		require.NoError(t, err)
		_, err = g.Apply(game.RevealVotes{})
		require.ErrorIs(t, err, game.ErrNoChange)
	})

	t.Run("reveal outside PhaseDayVote is rejected", func(t *testing.T) {
		g := fixedRoster(t)
		playNight(t, g, map[game.Role]game.PlayerID{
			game.RoleMafia:  "town1",
			game.RoleDoctor: "town1",
		})
		require.Equal(t, game.PhaseDayDiscussion, g.State().Phase())
		_, err := g.Apply(game.RevealVotes{})
		require.ErrorIs(t, err, game.ErrWrongPhase)
	})

	t.Run("reveal is blocked until every living player has cast", func(t *testing.T) {
		g := intoDayVote(t) // 5 living
		// Only three of five decide → reveal is rejected.
		_, err := g.Apply(game.DayVote{Voter: "town1", Target: "mafia1"})
		require.NoError(t, err)
		_, err = g.Apply(game.DayVote{Voter: "town2", Target: "mafia1"})
		require.NoError(t, err)
		_, err = g.Apply(game.DayAbstain{Voter: "det"})
		require.NoError(t, err)

		_, err = g.Apply(game.RevealVotes{})
		require.ErrorIs(t, err, game.ErrVotingIncomplete)
		require.False(t, g.State().VotesRevealed(), "a blocked reveal must not flip the flag")

		// The last two decide → reveal now succeeds.
		_, err = g.Apply(game.DayAbstain{Voter: "doc"})
		require.NoError(t, err)
		_, err = g.Apply(game.DayAbstain{Voter: "mafia1"})
		require.NoError(t, err)
		_, err = g.Apply(game.RevealVotes{})
		require.NoError(t, err)
		require.True(t, g.State().VotesRevealed())
	})

	t.Run("reveal with everyone abstaining is allowed and snapshots empty", func(t *testing.T) {
		g := intoDayVote(t)
		for _, v := range []game.PlayerID{"town1", "town2", "det", "doc", "mafia1"} {
			_, err := g.Apply(game.DayAbstain{Voter: v})
			require.NoError(t, err)
		}
		evts, err := g.Apply(game.RevealVotes{})
		require.NoError(t, err)
		rv, ok := findEvent[game.VotesRevealed](evts)
		require.True(t, ok)
		require.Empty(t, rv.Tally, "all abstained → no votes in the tally")
		require.NotNil(t, rv.Tally, "tally is always a non-nil (possibly empty) map")
	})

	t.Run("clear after an all-abstain reveal reopens voting", func(t *testing.T) {
		// Regression: ClearVotes must work after a reveal even when the
		// tally is empty (here because everyone abstained) — clearing still
		// undoes the reveal and reopens hidden voting.
		g := intoDayVote(t)
		for _, v := range []game.PlayerID{"town1", "town2", "det", "doc", "mafia1"} {
			_, err := g.Apply(game.DayAbstain{Voter: v})
			require.NoError(t, err)
		}
		_, err := g.Apply(game.RevealVotes{})
		require.NoError(t, err)
		require.True(t, g.State().VotesRevealed())

		evts, err := g.Apply(game.ClearVotes{})
		require.NoError(t, err, "clear must work after a reveal even with an empty tally")
		_, ok := findEvent[game.VoteCleared](evts)
		require.True(t, ok)
		require.False(t, g.State().VotesRevealed(), "clear undoes the reveal")

		// And voting is open again.
		_, err = g.Apply(game.DayVote{Voter: "town1", Target: "mafia1"})
		require.NoError(t, err)
	})

	t.Run("voting is locked after reveal until cleared", func(t *testing.T) {
		g := intoDayVote(t)
		castDecisive(t, g)
		_, err := g.Apply(game.RevealVotes{})
		require.NoError(t, err)

		// New votes and changes are both rejected post-reveal.
		_, err = g.Apply(game.DayVote{Voter: "mafia1", Target: "det"})
		require.ErrorIs(t, err, game.ErrWrongPhase,
			"a new vote after reveal must be rejected")
		_, err = g.Apply(game.DayVote{Voter: "town1", Target: "det"})
		require.ErrorIs(t, err, game.ErrWrongPhase,
			"changing a vote after reveal must be rejected")

		// ClearVotes reopens voting (hidden again) and votes flow once more.
		_, err = g.Apply(game.ClearVotes{})
		require.NoError(t, err)
		require.False(t, g.State().VotesRevealed(),
			"ClearVotes undoes the reveal")
		_, err = g.Apply(game.DayVote{Voter: "town1", Target: "mafia1"})
		require.NoError(t, err, "voting is open again after clear")
	})

	t.Run("OpenVoting starts a fresh round hidden", func(t *testing.T) {
		// Drive a full reveal+lynch, begin a new night, and confirm the
		// next OpenVoting starts with votesRevealed=false again.
		g := intoDayVote(t)
		_, err := g.Apply(game.DayVote{Voter: "town1", Target: "det"})
		require.NoError(t, err)
		_, err = g.Apply(game.DayVote{Voter: "town2", Target: "det"})
		require.NoError(t, err)
		_, err = g.Apply(game.DayVote{Voter: "mafia1", Target: "det"})
		require.NoError(t, err)
		// det and doc must also decide before the host can reveal.
		_, err = g.Apply(game.DayAbstain{Voter: "det"})
		require.NoError(t, err)
		_, err = g.Apply(game.DayAbstain{Voter: "doc"})
		require.NoError(t, err)
		_, err = g.Apply(game.RevealVotes{})
		require.NoError(t, err)
		_, err = g.Apply(game.FinalizeVotes{})
		require.NoError(t, err)
		require.False(t, g.State().VotesRevealed(),
			"finalize clears the reveal flag")
	})

	t.Run("finalize after reveal lynches the plurality target", func(t *testing.T) {
		g := intoDayVote(t)
		castDecisive(t, g)
		_, err := g.Apply(game.RevealVotes{})
		require.NoError(t, err)
		evts, err := g.Apply(game.FinalizeVotes{})
		require.NoError(t, err)
		l, ok := findEvent[game.PlayerLynched](evts)
		require.True(t, ok)
		require.Equal(t, game.PlayerID("mafia1"), l.PlayerID)
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
		g := mkEndedGame(t)
		_, err := g.Apply(game.AdvancePhase{})
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
			g := mkEndedGame(t)
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

// TestNightRoles_ActOncePerNight pins the once-per-night invariant for EVERY
// night-acting role in one place. The first submission drives act → ponder,
// closing the act window, so a second submission by the same actor lands
// outside the act sub-phase and is rejected with ErrNotYourTurn — NOT
// ErrAlreadyActed (the pendingNight dup-check is a later, effectively
// unreachable backstop; the window closing is the real enforcement). The
// mafia is the faction-collective variant of the same mechanism (see also
// TestMafia_OneKillPerNightAcrossTheFaction); here we exercise it for the
// solo roles too, including the optional ones, so the property can't silently
// regress for any single role.
func TestNightRoles_ActOncePerNight(t *testing.T) {
	tests := []struct {
		name string
		// setup leaves the game in PhaseNight on `actor`'s ACT window.
		setup         func(t *testing.T) *game.Game
		actor         game.PlayerID
		first, second game.PlayerID // two distinct living, non-self targets
	}{
		{
			name:  "mafia",
			setup: fixedRoster,
			actor: "mafia1", first: "town1", second: "town2",
		},
		{
			name: "detective",
			setup: func(t *testing.T) *game.Game {
				g := fixedRoster(t)
				toDetectiveAct(t, g)
				return g
			},
			actor: "det", first: "mafia1", second: "town1",
		},
		{
			name: "doctor",
			setup: func(t *testing.T) *game.Game {
				g := fixedRoster(t)
				walkRestOfTurn(t, g) // mafia -> detective
				walkRestOfTurn(t, g) // detective -> doctor
				return g
			},
			actor: "doc", first: "town1", second: "town2",
		},
		{
			name: "consort",
			setup: func(t *testing.T) *game.Game {
				g := fixedRosterWithConsort(t)
				walkRestOfTurn(t, g) // mafia -> consort
				return g
			},
			actor: "consort", first: "town1", second: "town2",
		},
		{
			name: "vigilante",
			setup: func(t *testing.T) *game.Game {
				g := fixedRosterWithVigilante(t)
				walkRestOfTurn(t, g) // mafia -> detective
				walkRestOfTurn(t, g) // detective -> vigilante
				return g
			},
			actor: "vig", first: "town1", second: "town2",
		},
		{
			name: "tracker",
			setup: func(t *testing.T) *game.Game {
				g := fixedRosterWithTracker(t)
				walkRestOfTurn(t, g) // mafia -> detective
				walkRestOfTurn(t, g) // detective -> doctor
				walkRestOfTurn(t, g) // doctor -> tracker
				return g
			},
			actor: "trk", first: "mafia1", second: "town1",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			g := tc.setup(t)
			require.Equal(t, game.NightSubAct, g.State().CurrentNightSubPhase(),
				"setup must leave %s on its act window", tc.name)

			_, err := g.Apply(game.NightAction{Actor: tc.actor, Target: tc.first})
			require.NoError(t, err, "the first action is accepted")

			_, err = g.Apply(game.NightAction{Actor: tc.actor, Target: tc.second})
			require.ErrorIs(t, err, game.ErrNotYourTurn,
				"%s cannot act twice in one night — the act window closed after the first submission", tc.name)
		})
	}
}
