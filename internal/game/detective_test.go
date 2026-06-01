package game_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/96malhar/mafia-the-game/internal/game"
)

// Detective-specific behaviour: the investigation reads a target's
// FACTION (mafia vs not), the result is delivered immediately and
// privately to the detective, self-investigation is forbidden, and the
// power has no per-game limit (re-investigation across nights works).

// toDetectiveAct walks from the mafia act window (a fixedRoster
// postcondition) to the detective's act window, timing the mafia out so
// nobody dies and the whole roster is available to investigate.
func toDetectiveAct(t *testing.T, g *game.Game) {
	t.Helper()
	walkRestOfTurn(t, g) // mafia (timeout) -> detective act
	require.Equal(t, game.RoleDetective, g.State().CurrentNightRole())
	require.Equal(t, game.NightSubAct, g.State().CurrentNightSubPhase())
}

func TestDetective_FactionIsTown(t *testing.T) {
	require.Equal(t, game.FactionTown, game.RoleDetective.Faction())
}

func TestDetective_ReadsMafiaAsMafia(t *testing.T) {
	g := fixedRoster(t)
	toDetectiveAct(t, g)

	evts := nightAction(t, g, "det", "mafia1")
	res, ok := findEvent[game.DetectiveResult](evts)
	require.True(t, ok)
	require.Equal(t, game.PlayerID("mafia1"), res.Target)
	require.True(t, res.IsMafia, "a mafioso reads as mafia")
}

func TestDetective_ReadsTownRolesAsNotMafia(t *testing.T) {
	// Every town-aligned target reads as NOT mafia, whatever its role.
	for _, target := range []game.PlayerID{"doc", "town1", "town2"} {
		t.Run(string(target), func(t *testing.T) {
			g := fixedRoster(t)
			toDetectiveAct(t, g)

			evts := nightAction(t, g, "det", target)
			res, ok := findEvent[game.DetectiveResult](evts)
			require.True(t, ok)
			require.Equal(t, target, res.Target)
			require.False(t, res.IsMafia, "a town-aligned target reads as NOT mafia")
		})
	}
}

func TestDetective_ResultIsImmediateAndPrivate(t *testing.T) {
	// The result rides the very batch that records the action (immediate
	// feedback), and it is visible ONLY to the detective.
	g := fixedRoster(t)
	toDetectiveAct(t, g)

	evts := nightAction(t, g, "det", "mafia1")
	res, ok := findEvent[game.DetectiveResult](evts)
	require.True(t, ok, "the result is delivered in the submit batch, not at resolve")
	require.Equal(t, game.PlayerID("det"), res.Detective)
	require.Equal(t, "player", res.Visibility().Audience)
	require.Equal(t, game.PlayerID("det"), res.Visibility().Player,
		"the investigation result is private to the detective")
}

func TestDetective_CannotInvestigateSelf(t *testing.T) {
	g := fixedRoster(t)
	toDetectiveAct(t, g)

	_, err := g.Apply(game.NightAction{Actor: "det", Target: "det"})
	require.ErrorIs(t, err, game.ErrSelfTarget,
		"the detective cannot investigate themselves")
}

func TestDetective_ReinvestigatesAcrossNights(t *testing.T) {
	// The detective has no one-shot limit: they investigate every night.
	// Night 1 reads the mafia (true); after a quiet day, night 2 reads
	// the same mafia again (still true).
	g := fixedRoster(t)
	evts1 := playNight(t, g, map[game.Role]game.PlayerID{
		game.RoleDetective: "mafia1",
	})
	res1, ok := findEvent[game.DetectiveResult](evts1)
	require.True(t, ok)
	require.True(t, res1.IsMafia)

	noLynchDay(t, g)
	beginNightToMafiaAct(t, g)

	evts2 := playNight(t, g, map[game.Role]game.PlayerID{
		game.RoleDetective: "mafia1",
	})
	res2, ok := findEvent[game.DetectiveResult](evts2)
	require.True(t, ok, "the detective investigates again on night 2")
	require.True(t, res2.IsMafia)
}

func TestDetective_DeadTargetRejected(t *testing.T) {
	// Night 1: the mafia kills town1 (unsaved). Night 2: investigating the
	// now-dead town1 is rejected.
	g := fixedRoster(t)
	playNight(t, g, map[game.Role]game.PlayerID{
		game.RoleMafia: "town1",
	})
	require.False(t, livingByID(g, "town1"))

	noLynchDay(t, g)
	beginNightToMafiaAct(t, g)
	toDetectiveAct(t, g)

	_, err := g.Apply(game.NightAction{Actor: "det", Target: "town1"})
	require.ErrorIs(t, err, game.ErrPlayerDead,
		"the detective cannot investigate a dead player")
}
