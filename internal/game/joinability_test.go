package game_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/96malhar/mafia-the-game/internal/game"
)

// JoinBlockedReason is the read-only mirror of applyAddPlayer's gate, exposed
// so the room/transport layer can answer a pre-join probe. These cases pin it
// to the SAME sentinels applyAddPlayer returns, since a drift would let the
// lobby's "joinable?" probe disagree with what a real join does.

func TestJoinBlockedReason_OpenLobby(t *testing.T) {
	// A pristine (never-created) engine can't be joined yet: requireActiveGame
	// rejects it with ErrWrongPhase, exactly as AddPlayer would.
	require.ErrorIs(t, game.New().JoinBlockedReason(), game.ErrWrongPhase)

	// A created, partially-filled lobby accepts joins → nil.
	g := fillLobbyN(t, 1, 3)
	require.NoError(t, g.JoinBlockedReason())
}

func TestJoinBlockedReason_Full(t *testing.T) {
	// A lobby filled to MaxPlayers blocks with ErrLobbyFull. Use a tiny
	// MaxPlayers so we don't join 20 players just to hit the cap.
	g := game.New()
	_, err := g.Apply(game.CreateGame{
		GameID: "g1", MinPlayers: 4, MaxPlayers: 4, MafiaCount: 1, Seed: 1,
	})
	require.NoError(t, err)
	addPlayers(t, g, "a", "b", "c", "d")
	require.ErrorIs(t, g.JoinBlockedReason(), game.ErrLobbyFull)
}

func TestJoinBlockedReason_InProgress(t *testing.T) {
	// Once StartGame deals roles the lobby is closed even though the phase is
	// still PhaseLobby — requireLobbyOpen's rolesDealt check fires, surfacing
	// ErrWrongPhase ("already in progress").
	g := fillLobbyN(t, 1, 5)
	_, err := g.Apply(game.StartGame{})
	require.NoError(t, err)
	require.ErrorIs(t, g.JoinBlockedReason(), game.ErrWrongPhase)
}

func TestJoinBlockedReason_Ended(t *testing.T) {
	// A finished game reports ErrGameEnded, distinct from the in-progress
	// ErrWrongPhase so the probe can say "already ended" vs "in progress".
	g := mkEndedGame(t)
	require.ErrorIs(t, g.JoinBlockedReason(), game.ErrGameEnded)
}
