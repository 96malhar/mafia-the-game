package game_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/96malhar/mafia-the-game/internal/game"
)

// TestResetGame_ReturnsEndedGameToFreshLobby is the happy path: a finished
// game resets to a lobby that keeps every player (id + name) but wipes all
// per-game state, and the lobby reopens so the room can start a fresh game.
func TestResetGame_ReturnsEndedGameToFreshLobby(t *testing.T) {
	g := mkEndedGame(t)
	require.Equal(t, game.PhaseEnded, g.State().Phase(), "precondition")

	// Snapshot the roster (id+name) before the reset so we can assert it
	// survives unchanged.
	type idName struct {
		id   game.PlayerID
		name string
	}
	var before []idName
	for _, p := range g.State().Players() {
		before = append(before, idName{p.ID(), p.Name()})
	}
	require.Len(t, before, 5)

	events, err := g.Apply(game.ResetGame{Seed: 42})
	require.NoError(t, err)

	// Exactly one GameReset event, carrying a self-contained lobby snapshot.
	require.Len(t, events, 1)
	reset, ok := events[0].(game.GameReset)
	require.True(t, ok, "expected a GameReset event, got %T", events[0])
	require.Equal(t, 5, len(reset.Players))
	require.Equal(t, g.State().MinPlayers(), reset.MinPlayers)
	require.Equal(t, g.State().MaxPlayers(), reset.MaxPlayers)
	require.Equal(t, g.State().MafiaCount(), reset.MafiaCount)

	// Phase is back to lobby; day counter reset.
	require.Equal(t, game.PhaseLobby, g.State().Phase())

	// Roster retained, every player alive again with no role.
	var after []idName
	for _, p := range g.State().Players() {
		after = append(after, idName{p.ID(), p.Name()})
		require.True(t, p.Alive(), "player %s should be alive after reset", p.ID())
		require.Equal(t, game.Role(""), p.Role(), "player %s role should be cleared", p.ID())
	}
	require.Equal(t, before, after, "roster (id+name, in order) must be preserved")

	// Config reset to lobby defaults. mkEndedGame deals 2 mafia; the default
	// for a 5-min lobby is 1, so this proves the reset-to-defaults path ran
	// on a field whose value actually changed.
	require.Equal(t, 1, g.State().MafiaCount())
	require.False(t, g.State().ConsortEnabled())
	require.False(t, g.State().VigilanteEnabled())
	require.False(t, g.State().YakuzaEnabled())
	require.False(t, g.State().VigilanteShotUsed())
	require.False(t, g.State().DayLynchResolved())
	require.False(t, g.State().VotesRevealed())
}

// TestResetGame_ReopensLobbyForNewPlayersAndRestart proves the two
// behaviors the feature exists for: a new player can join the reset lobby
// (the rolesDealt lock is cleared), and the host can start a fresh game.
func TestResetGame_ReopensLobbyForNewPlayersAndRestart(t *testing.T) {
	g := mkEndedGame(t)
	_, err := g.Apply(game.ResetGame{Seed: 7})
	require.NoError(t, err)

	// A brand-new player can join the fresh lobby — impossible once roles
	// are dealt, so this confirms the lobby genuinely reopened.
	_, err = g.Apply(game.AddPlayer{PlayerID: "f", Name: "Frank"})
	require.NoError(t, err)
	require.Equal(t, 6, g.State().PlayerCount())

	// And the host can deal a fresh game.
	_, err = g.Apply(game.StartGame{})
	require.NoError(t, err)
	for _, p := range g.State().Players() {
		require.NotEqual(t, game.Role(""), p.Role(), "every player should be dealt a role")
	}
}

// TestResetGame_RejectedBeforeGameEnds confirms ResetGame is only legal from
// PhaseEnded: a pristine engine, a lobby, and a started game all reject it.
func TestResetGame_RejectedBeforeGameEnds(t *testing.T) {
	t.Run("pristine engine", func(t *testing.T) {
		g := game.New()
		_, err := g.Apply(game.ResetGame{Seed: 1})
		require.ErrorIs(t, err, game.ErrWrongPhase)
	})

	t.Run("open lobby", func(t *testing.T) {
		g := game.New()
		_, err := g.Apply(game.CreateGame{GameID: "g", MinPlayers: 5, MaxPlayers: 20})
		require.NoError(t, err)
		addPlayers(t, g, "a", "b", "c", "d", "e")
		_, err = g.Apply(game.ResetGame{Seed: 1})
		require.ErrorIs(t, err, game.ErrWrongPhase)
	})

	t.Run("game in progress", func(t *testing.T) {
		g := game.New()
		_, err := g.Apply(game.CreateGame{GameID: "g", MinPlayers: 5, MaxPlayers: 20, MafiaCount: 1})
		require.NoError(t, err)
		addPlayers(t, g, "a", "b", "c", "d", "e")
		_, err = g.Apply(game.StartGame{})
		require.NoError(t, err)
		_, err = g.Apply(game.BeginNight{})
		require.NoError(t, err)
		_, err = g.Apply(game.ResetGame{Seed: 1})
		require.ErrorIs(t, err, game.ErrWrongPhase)
	})
}

// TestResetGame_VisibilityIsPublic locks in that GameReset is visible to
// everyone — every connected client (living or dead in the prior game) must
// see it to drop back to the lobby, and a post-reset joiner reconstructs the
// lobby from it alone.
func TestResetGame_VisibilityIsPublic(t *testing.T) {
	g := mkEndedGame(t)
	events, err := g.Apply(game.ResetGame{Seed: 1})
	require.NoError(t, err)
	require.Len(t, events, 1)
	require.Equal(t, "public", events[0].Visibility().Audience)
}
