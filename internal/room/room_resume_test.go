package room

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// These tests cover the cursor-driven resume protocol: broadcast events carry
// a monotonic sequence, and a rejoin ships only the projected tail since the
// client's cursor (falling back to the full log for a cold or over-range
// cursor) so a reconnect doesn't re-download the whole game.

// TestRoomBroadcast_StampsIncreasingSeq verifies every streamed OutEvent
// carries a positive, strictly increasing sequence — the absolute cursor a
// client tracks across reconnects.
func TestRoomBroadcast_StampsIncreasingSeq(t *testing.T) {
	r, err := newRoom(context.Background(), "SEQ", Config{Logger: silentLogger()})
	require.NoError(t, err)
	go r.Run()
	t.Cleanup(func() { _ = r.Close(context.Background()) })

	subA, _ := connect(t, r, "A") // host; its own PlayerJoined + HostChanged broadcast to it
	connect(t, r, "B")            // a second join → another public event A observes

	var seqs []int
	for _, msg := range drain(subA, 200*time.Millisecond) {
		if oe, ok := msg.(OutEvent); ok {
			seqs = append(seqs, oe.Seq)
		}
	}

	require.NotEmpty(t, seqs, "the host should have observed streamed events with sequences")
	for i, s := range seqs {
		require.Positive(t, s, "sequence must be 1-based positive")
		if i > 0 {
			require.Greater(t, s, seqs[i-1], "sequences must strictly increase along the stream")
		}
	}
}

// TestRoomRejoin_CursorDelta pins the four resume cases. The room stays in the
// lobby, where every event is public, so the projected tail length equals the
// raw slice length and the arithmetic is exact.
func TestRoomRejoin_CursorDelta(t *testing.T) {
	r, err := newRoom(context.Background(), "DLTA", Config{Logger: silentLogger()})
	require.NoError(t, err)
	go r.Run()
	t.Cleanup(func() { _ = r.Close(context.Background()) })

	acks := make([]OutJoined, 5)
	for i := range acks {
		_, acks[i] = connect(t, r, string(rune('A'+i)))
	}

	var total int
	onLoop(t, r, func(rr *Room) { total = len(rr.events) })
	require.Greater(t, total, 5, "log should hold GameCreated + 5 joins + a HostChanged")

	// rejoin attaches a fresh subscriber to player 0's slot with the given
	// cursor and returns the resume reply.
	rejoin := func(since int) OutRejoined {
		s := NewSubscriber()
		require.NoError(t, r.SubmitRejoin(context.Background(), s, acks[0].PlayerID, acks[0].Secret, since))
		return recvType[OutRejoined](t, s)
	}

	t.Run("cold cursor ships the full log", func(t *testing.T) {
		rj := rejoin(0)
		require.Equal(t, 0, rj.FromSeq)
		require.Equal(t, total, rj.LastSeq)
		require.Len(t, rj.Events, total, "a cold rejoin rebuilds from the whole projected log")
	})

	t.Run("mid cursor ships only the tail", func(t *testing.T) {
		const since = 3
		rj := rejoin(since)
		require.Equal(t, since, rj.FromSeq)
		require.Equal(t, total, rj.LastSeq)
		require.Len(t, rj.Events, total-since, "a delta carries only events after the cursor")
	})

	t.Run("up-to-date cursor ships nothing", func(t *testing.T) {
		rj := rejoin(total)
		require.Equal(t, total, rj.FromSeq)
		require.Equal(t, total, rj.LastSeq)
		require.Empty(t, rj.Events, "a caught-up client gets an empty delta")
	})

	t.Run("over-range cursor falls back to a full snapshot", func(t *testing.T) {
		rj := rejoin(total + 50)
		require.Equal(t, 0, rj.FromSeq, "a cursor past the log (e.g. after a reset) rebuilds from scratch")
		require.Equal(t, total, rj.LastSeq)
		require.Len(t, rj.Events, total)
	})
}
