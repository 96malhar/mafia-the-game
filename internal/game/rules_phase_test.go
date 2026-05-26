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
		if match {
			// fixedRoster used to leave the game in PhaseNight (the
			// old StartGame transitioned automatically). Now the
			// host's BeginNight does that — fire it here so existing
			// tests can keep treating "after fixedRoster" as "start
			// of Night 1".
			_, err = g.Apply(game.BeginNight{})
			require.NoError(t, err)
			return g
		}
	}
	t.Fatalf("could not find a seed yielding the fixed role assignment in 1000 attempts")
	return nil
}

// nightAction submits one night action for the role that is currently
// holding the turn. It fails the test if the engine rejects it. Use
// this in tests that exercise a single role's action; for orchestrating
// a full night, use playNight.
func nightAction(t *testing.T, g *game.Game, actor, target game.PlayerID) []game.Event {
	t.Helper()
	evts, err := g.Apply(game.NightAction{Actor: actor, Target: target})
	require.NoError(t, err)
	return evts
}

// skipCurrentTurn fires AdvancePhase to end the current night turn
// without submitting an action (timeout semantics).
func skipCurrentTurn(t *testing.T, g *game.Game) []game.Event {
	t.Helper()
	evts, err := g.Apply(game.AdvancePhase{})
	require.NoError(t, err)
	return evts
}

// playNight runs through a full Night with the given (role -> target)
// actions. Any role missing from the map is skipped (timeout-style).
// Phantom turns (no living holder of the role) accept no action and
// are likewise skipped via AdvancePhase. Returns every event emitted
// across all turns (including the resolve batch), so callers can
// findEvent for any event type (DetectiveResult fires at action
// time, PlayerKilled / PlayerSaved / GameEnded fire at resolve).
//
// The engine's Night flow is mostly self-resolving: when the LAST turn
// in the queue is either skipped or actioned, that call runs
// resolveNight and transitions to DayDiscussion atomically. The
// exception is the detective's action — the engine intentionally
// stops after recording the result so the room can schedule a
// read-modal pause. This helper bridges that pause by issuing an
// immediate AdvancePhase, mirroring what the room does in production
// (just without the wall-clock wait).
func playNight(t *testing.T, g *game.Game, actions map[game.Role]game.PlayerID) []game.Event {
	t.Helper()
	canonical := []game.Role{game.RoleMafia, game.RoleDetective, game.RoleDoctor}
	var allEvents []game.Event
	resolved := false
	for _, r := range canonical {
		require.Equal(t, r, g.State().CurrentNightRole(),
			"playNight: expected role %s to be current but got %s",
			r, g.State().CurrentNightRole())
		target, ok := actions[r]
		var evts []game.Event
		switch {
		case g.State().CurrentNightTurnIsPhantom():
			evts = skipCurrentTurn(t, g)
		case !ok:
			evts = skipCurrentTurn(t, g)
		default:
			var actor game.PlayerID
			for _, p := range g.State().Players() {
				if p.Role() == r && p.Alive() {
					actor = p.ID()
					break
				}
			}
			require.NotEmpty(t, actor, "no living %s to submit action", r)
			evts = nightAction(t, g, actor, target)
		}
		allEvents = append(allEvents, evts...)

		// Detective action does not auto-advance — see helper comment.
		// Drive the next turn manually so the rest of the night runs.
		if r == game.RoleDetective && ok && g.State().Phase() == game.PhaseNight && g.State().CurrentNightRole() == "" {
			advanceEvts, err := g.Apply(game.AdvancePhase{})
			require.NoError(t, err, "playNight: AdvancePhase after detective action failed")
			allEvents = append(allEvents, advanceEvts...)
		}
		if g.State().Phase() != game.PhaseNight {
			resolved = true
		}
	}
	require.True(t, resolved,
		"playNight: night did not resolve (perhaps the queue was empty before any turn?)")
	return allEvents
}

// toDayVote walks the engine from the start of a Night through to
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
// resolve) and walks to the NEXT PhaseNight by simulating a "no
// lynch" day: open voting, finalize with no decisive vote fails, so
// we use a workaround — the host can never end a day without a lynch
// in the new flow, so for tests that need to reach Night N+1 without
// caring who dies, we simulate a vote on a fixed target and finalize.
//
// Caller passes the lynch target. Use this when the test doesn't care
// who gets lynched and just wants to advance through a day.
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
}

// --- Night turn-order plumbing -------------------------------------------

func TestNight_FirstTurnIsMafia(t *testing.T) {
	g := fixedRoster(t)
	require.Equal(t, game.PhaseNight, g.State().Phase())
	require.Equal(t, game.RoleMafia, g.State().CurrentNightRole(),
		"the first night turn must be the mafia's")
}

func TestNight_TurnOrderMafiaDetectiveDoctor(t *testing.T) {
	g := fixedRoster(t)

	// Mafia submits -> detective turn begins.
	evts := nightAction(t, g, "mafia1", "town1")
	hasTurnEnded(t, evts, game.RoleMafia)
	hasTurnStarted(t, evts, game.RoleDetective)
	require.Equal(t, game.RoleDetective, g.State().CurrentNightRole())

	// Detective submits: the engine intentionally PAUSES here.
	// NightTurnEnded fires for the detective and DetectiveResult is
	// emitted privately, but the doctor's turn is NOT auto-started.
	// The room layer drives a short pause (so the detective can read
	// their result modal) and then sends AdvancePhase to continue.
	evts = nightAction(t, g, "det", "mafia1")
	hasTurnEnded(t, evts, game.RoleDetective)
	requireNoTurnStarted(t, evts)
	require.Equal(t, game.Role(""), g.State().CurrentNightRole(),
		"engine should leave currentNightRole empty during the detective pause")

	// Caller drives the pause forward. AdvancePhase pops the doctor.
	evts, err := g.Apply(game.AdvancePhase{})
	require.NoError(t, err)
	hasTurnStarted(t, evts, game.RoleDoctor)
	require.Equal(t, game.RoleDoctor, g.State().CurrentNightRole())

	// Doctor submits: this was the LAST role in the queue, so the
	// engine atomically resolves the night and transitions to
	// DayDiscussion in the same call. Removes a footgun where callers
	// would otherwise need to know "did the last turn submit or skip?"
	// to decide whether to fire one more AdvancePhase.
	evts = nightAction(t, g, "doc", "town2")
	hasTurnEnded(t, evts, game.RoleDoctor)
	require.Equal(t, game.Role(""), g.State().CurrentNightRole())
	require.Equal(t, game.PhaseDayDiscussion, g.State().Phase(),
		"final action resolves the night atomically")
}

func TestNight_SkippedRolesGetTimeoutAdvance(t *testing.T) {
	g := fixedRoster(t)

	// Mafia's turn: skip via AdvancePhase (simulates timeout).
	evts := skipCurrentTurn(t, g)
	hasTurnEnded(t, evts, game.RoleMafia)
	hasTurnStarted(t, evts, game.RoleDetective)

	// Detective skips too.
	evts = skipCurrentTurn(t, g)
	hasTurnEnded(t, evts, game.RoleDetective)
	hasTurnStarted(t, evts, game.RoleDoctor)

	// Doctor's skip: queue is now empty, so this same AdvancePhase
	// call ends the doctor's turn AND resolves the night, transitioning
	// to PhaseDayDiscussion in one batch.
	evts = skipCurrentTurn(t, g)
	hasTurnEnded(t, evts, game.RoleDoctor)
	require.Equal(t, game.Role(""), g.State().CurrentNightRole())
	require.Equal(t, game.PhaseDayDiscussion, g.State().Phase(),
		"final skip resolves the night atomically")
}

func TestNight_DeadRoleEmitsPhantomTurn(t *testing.T) {
	// Phantom turns exist to hide info leakage: the room must not be
	// able to deduce that a role is dead just from missing audio
	// cues. So on subsequent nights we always queue Mafia → Detective
	// → Doctor; turns whose role has no living holder are emitted as
	// phantom turns that accept no action and are advanced via
	// AdvancePhase (timeout).
	g := fixedRoster(t)

	// Night 1: mafia kills the detective, no save.
	playNight(t, g, map[game.Role]game.PlayerID{
		game.RoleMafia: "det",
	})
	require.Equal(t, game.PhaseDayDiscussion, g.State().Phase())

	// Walk to Night 2 via a host-driven lynch on town2.
	toNextNight(t, g, "town2", "town1", "doc")
	require.Equal(t, game.PhaseNight, g.State().Phase())

	// Mafia's turn: real (mafia1 is still alive).
	require.Equal(t, game.RoleMafia, g.State().CurrentNightRole())
	require.False(t, g.State().CurrentNightTurnIsPhantom(),
		"mafia turn must not be phantom while mafia1 is alive")
	evts := nightAction(t, g, "mafia1", "town1")
	hasTurnStarted(t, evts, game.RoleDetective)

	// Detective's turn: phantom because detective is dead, but it
	// STILL runs and STILL emits a NightTurnStarted so the audio
	// fires identically to a live detective turn.
	require.Equal(t, game.RoleDetective, g.State().CurrentNightRole(),
		"phantom detective turn must still be the current role")
	require.True(t, g.State().CurrentNightTurnIsPhantom(),
		"detective is dead -> turn must be phantom")
	// Dead detective trying to submit fails with ErrPlayerDead, not
	// ErrNotYourTurn — the actor check fires first.
	_, err := g.Apply(game.NightAction{Actor: "det", Target: "town1"})
	require.ErrorIs(t, err, game.ErrPlayerDead)

	// AdvancePhase ends the phantom turn and moves to doctor.
	evts = skipCurrentTurn(t, g)
	hasTurnEnded(t, evts, game.RoleDetective)
	hasTurnStarted(t, evts, game.RoleDoctor)
	require.Equal(t, game.RoleDoctor, g.State().CurrentNightRole())
	require.False(t, g.State().CurrentNightTurnIsPhantom())
}

func TestNight_PhantomTurnEmitsPhantomFlagOnEvent(t *testing.T) {
	// The NightTurnStarted event carries Phantom=true so the room
	// can shorten its wall-clock timer and clients can render
	// accordingly.
	g := fixedRoster(t)

	playNight(t, g, map[game.Role]game.PlayerID{
		game.RoleMafia: "det", // detective dies
	})
	toNextNight(t, g, "town2", "town1", "doc")
	require.Equal(t, game.PhaseNight, g.State().Phase())

	// Submit the mafia action; the batch should include a
	// NightTurnStarted for detective with Phantom=true.
	evts := nightAction(t, g, "mafia1", "town1")
	var detStart game.NightTurnStarted
	var found bool
	for _, e := range evts {
		if ts, ok := e.(game.NightTurnStarted); ok && ts.Role == game.RoleDetective {
			detStart = ts
			found = true
			break
		}
	}
	require.True(t, found, "expected NightTurnStarted for detective in batch")
	require.True(t, detStart.Phantom,
		"detective turn must be marked Phantom when detective is dead")
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
		// Villagers have no NightAction at all; rejected with
		// ErrNotYourAction regardless of which role's turn it is.
		_, err := g.Apply(game.NightAction{Actor: "town1", Target: "town2"})
		require.ErrorIs(t, err, game.ErrNotYourAction)
	})

	t.Run("unknown actor rejected", func(t *testing.T) {
		g := fixedRoster(t)
		_, err := g.Apply(game.NightAction{Actor: "ghost", Target: "town1"})
		require.ErrorIs(t, err, game.ErrUnknownPlayer)
	})

	t.Run("wrong role on wrong turn", func(t *testing.T) {
		g := fixedRoster(t) // currentNightRole = mafia
		_, err := g.Apply(game.NightAction{Actor: "det", Target: "mafia1"})
		require.ErrorIs(t, err, game.ErrNotYourTurn,
			"detective cannot act during the mafia's turn")
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
		// First mafia submits and ends the mafia turn; a second mafia
		// trying to submit gets ErrNotYourTurn because currentRole is
		// now detective.
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

		_, err = g.Apply(game.NightAction{Actor: mafias[1], Target: townTarget})
		require.ErrorIs(t, err, game.ErrNotYourTurn,
			"second mafia submits after the mafia turn has ended")
	})

	t.Run("detective cannot self-investigate", func(t *testing.T) {
		g := fixedRoster(t)
		// Walk to detective's turn.
		nightAction(t, g, "mafia1", "town1")
		require.Equal(t, game.RoleDetective, g.State().CurrentNightRole())

		_, err := g.Apply(game.NightAction{Actor: "det", Target: "det"})
		require.ErrorIs(t, err, game.ErrSelfTarget)
	})

	t.Run("doctor can self-save on any night including the first", func(t *testing.T) {
		// The doctor has no self-save restriction. This used to be
		// gated to night 2+ but the carve-out confused new players;
		// the role is intentionally permissive now.
		g := fixedRoster(t)
		// Walk to doctor's turn (mafia + det submit). Detective's
		// action does not auto-advance (the room schedules a brief
		// pause so the detective can read their result modal), so
		// we manually AdvancePhase to pop the doctor's turn.
		nightAction(t, g, "mafia1", "town1")
		nightAction(t, g, "det", "mafia1")
		_, err := g.Apply(game.AdvancePhase{})
		require.NoError(t, err)
		require.Equal(t, game.RoleDoctor, g.State().CurrentNightRole())

		_, err = g.Apply(game.NightAction{Actor: "doc", Target: "doc"})
		require.NoError(t, err, "doctor self-save should be legal on night 1")
	})

	t.Run("re-submission by same actor is rejected", func(t *testing.T) {
		// In the new turn model, once mafia1 submits, the mafia turn
		// is over and currentRole moves on. So a second mafia1
		// submission gets ErrNotYourTurn (the turn moved past mafia),
		// NOT ErrAlreadyActed. ErrAlreadyActed remains reachable in
		// future role designs where a single role can act multiple
		// times — keep the sentinel for that path.
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
		// town1 is dead; lynch det (non-mafia, so game continues).
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
		// Mafia kills detective on Night 1 (unsaved). On Night 2 the
		// detective turn is still queued — as a phantom — so a dead
		// detective trying to submit gets ErrPlayerDead. We test in
		// the phantom slot itself rather than waiting for it to pass.
		g := fixedRoster(t)
		playNight(t, g, map[game.Role]game.PlayerID{
			game.RoleMafia: "det",
		})
		toNextNight(t, g, "town2", "town1", "doc")
		require.Equal(t, game.PhaseNight, g.State().Phase())

		// Mafia acts; queue advances to the (phantom) detective turn.
		nightAction(t, g, "mafia1", "town1")
		require.Equal(t, game.RoleDetective, g.State().CurrentNightRole())
		require.True(t, g.State().CurrentNightTurnIsPhantom())

		_, err := g.Apply(game.NightAction{Actor: "det", Target: "town1"})
		require.ErrorIs(t, err, game.ErrPlayerDead)
	})
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

		// Tally must be empty: an identical re-vote no longer trips
		// ErrNoChange because the prior vote was cleared.
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
		// Lynch a villager so the game continues.
		g := intoDayVote(t)
		_, _ = g.Apply(game.DayVote{Voter: "town1", Target: "det"})
		_, _ = g.Apply(game.DayVote{Voter: "town2", Target: "det"})
		_, _ = g.Apply(game.DayVote{Voter: "mafia1", Target: "det"})

		_, err := g.Apply(game.FinalizeVotes{})
		require.NoError(t, err)
		require.Equal(t, game.PhaseDayDiscussion, g.State().Phase())
		require.True(t, g.State().DayLynchResolved())

		// OpenVoting must be rejected after the lynch.
		_, err = g.Apply(game.OpenVoting{})
		require.ErrorIs(t, err, game.ErrWrongPhase)

		// BeginNight is the only valid path forward.
		_, err = g.Apply(game.BeginNight{})
		require.NoError(t, err)
		require.Equal(t, game.PhaseNight, g.State().Phase())
		require.False(t, g.State().DayLynchResolved(),
			"BeginNight clears the lynch-resolved flag")
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
		// AdvancePhase is now night-turn-internal: day phases reject it.
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

		// Find a mafia and a villager.
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

		// Walk through the night: mafia kills, det skips, doc skip
		// resolves the night (final skip is the resolving call).
		_, _ = g.Apply(game.NightAction{Actor: mafia, Target: victim})
		// Now currentRole is Detective; skip → moves to Doctor turn.
		_, _ = g.Apply(game.AdvancePhase{})
		// Doctor skip → queue empty → resolveNight + win-check.
		evts, err := g.Apply(game.AdvancePhase{})
		require.NoError(t, err)

		ge, ok := findEvent[game.GameEnded](evts)
		require.True(t, ok, "GameEnded must fire (2 mafia >= 2 town after kill)")
		require.Equal(t, game.FactionMafia, ge.Winner)
		require.Equal(t, game.PhaseEnded, g.State().Phase())
		require.Len(t, ge.FinalRoles, 5)

		_, err = g.Apply(game.AdvancePhase{})
		require.ErrorIs(t, err, game.ErrGameEnded)
	})
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

// --- helpers --------------------------------------------------------------

func hasTurnEnded(t *testing.T, evts []game.Event, role game.Role) {
	t.Helper()
	for _, e := range evts {
		if te, ok := e.(game.NightTurnEnded); ok && te.Role == role {
			return
		}
	}
	t.Fatalf("expected NightTurnEnded{role=%s} in events; got %d events", role, len(evts))
}

func hasTurnStarted(t *testing.T, evts []game.Event, role game.Role) {
	t.Helper()
	for _, e := range evts {
		if ts, ok := e.(game.NightTurnStarted); ok && ts.Role == role {
			return
		}
	}
	t.Fatalf("expected NightTurnStarted{role=%s} in events; got %d events", role, len(evts))
}

func requireNoTurnStarted(t *testing.T, evts []game.Event) {
	t.Helper()
	for _, e := range evts {
		if ts, ok := e.(game.NightTurnStarted); ok {
			t.Fatalf("did not expect NightTurnStarted in events, but got role=%s", ts.Role)
		}
	}
}
