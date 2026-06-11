package room

import (
	"context"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/96malhar/mafia-the-game/internal/game"
	"github.com/96malhar/mafia-the-game/internal/wire"
)

// These tests assert the room package's custom metrics emit with the expected
// exported name, type, value, and labels — a golden-ish guard on the metric
// surface (a renamed instrument or a changed label key fails CI, because the
// wrong name/label yields a zero delta).
//
// The instruments are created lazily (a package-level sync.Once) against the
// global MeterProvider. TestMain installs ONE ManualReader provider before any
// test runs, so the lazy init binds to it exactly once (no mid-suite reset →
// no data race on the instrument vars). Tests assert BEFORE/AFTER deltas, which
// makes them robust to unrelated emissions from other tests that share the
// global meter (e.g. a room created elsewhere bumping room.active).

var testMeterReader sdkmetric.Reader

func TestMain(m *testing.M) {
	testMeterReader = sdkmetric.NewManualReader()
	otel.SetMeterProvider(sdkmetric.NewMeterProvider(sdkmetric.WithReader(testMeterReader)))
	os.Exit(m.Run())
}

// metricValue returns the int64 value of the named metric's data point matching
// attrs, or 0 if the metric/point is absent — so it doubles as a baseline read.
func metricValue(t *testing.T, name string, attrs ...attribute.KeyValue) int64 {
	t.Helper()
	var rm metricdata.ResourceMetrics
	require.NoError(t, testMeterReader.Collect(context.Background(), &rm))
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != name {
				continue
			}
			sum, ok := m.Data.(metricdata.Sum[int64])
			if !ok {
				return 0
			}
			for _, dp := range sum.DataPoints {
				if attrsMatch(dp.Attributes, attrs) {
					return dp.Value
				}
			}
		}
	}
	return 0
}

func attrsMatch(set attribute.Set, want []attribute.KeyValue) bool {
	for _, kv := range want {
		v, ok := set.Value(kv.Key)
		if !ok || v.String() != kv.Value.String() {
			return false
		}
	}
	return true
}

func TestMetrics_RoomActiveGauge(t *testing.T) {
	before := metricValue(t, "room.active")
	recordRoomOpened()
	recordRoomOpened()
	recordRoomClosed()
	require.Equal(t, int64(1), metricValue(t, "room.active")-before)
}

func TestMetrics_RoomPanicCounter(t *testing.T) {
	before := metricValue(t, "room.panic")
	recordRoomPanic()
	recordRoomPanic()
	require.Equal(t, int64(2), metricValue(t, "room.panic")-before)
}

func TestMetrics_CommandRejectedLabelled(t *testing.T) {
	code := attribute.String("code", string(wire.ErrCodeDuplicateName))
	before := metricValue(t, "game.command.rejected", code)
	recordCommandRejected(wire.ErrCodeDuplicateName)
	require.Equal(t, int64(1), metricValue(t, "game.command.rejected", code)-before)
}

func TestMetrics_GameStartedAndCompletedLabelled(t *testing.T) {
	mafia := attribute.String("winner", "mafia")
	town := attribute.String("winner", "town")
	beforeStarted := metricValue(t, "game.started")
	beforeMafia := metricValue(t, "game.completed", mafia)
	beforeTown := metricValue(t, "game.completed", town)

	recordGameStarted()
	recordGameCompleted("mafia")
	recordGameCompleted("town")

	require.Equal(t, int64(1), metricValue(t, "game.started")-beforeStarted)
	require.Equal(t, int64(1), metricValue(t, "game.completed", mafia)-beforeMafia)
	require.Equal(t, int64(1), metricValue(t, "game.completed", town)-beforeTown)
}

// TestMetrics_GameInProgressStartEnd drives the gauge through the real wiring
// (recordGameLifecycle in appendAndBroadcast), synchronously on a non-running
// room: a GameStarted raises it, the matching GameEnded releases it.
func TestMetrics_GameInProgressStartEnd(t *testing.T) {
	before := metricValue(t, "game.in_progress")
	r := minimalRoom()

	r.appendAndBroadcast([]game.Event{game.GameStarted{}})
	require.True(t, r.gameInProgress)
	require.Equal(t, int64(1), metricValue(t, "game.in_progress")-before)

	r.appendAndBroadcast([]game.Event{game.GameEnded{Winner: game.FactionMafia}})
	require.False(t, r.gameInProgress)
	require.Equal(t, int64(0), metricValue(t, "game.in_progress")-before)
}

// TestMetrics_GameInProgressReleasedOnAbandon verifies the Run() teardown defer
// releases the gauge when a room is shut down while a game is still being
// played (an abandoned game) — exercising the real defer, not an inline copy.
func TestMetrics_GameInProgressReleasedOnAbandon(t *testing.T) {
	before := metricValue(t, "game.in_progress")
	r, err := newRoom(context.Background(), "ABND", Config{Logger: silentLogger()})
	require.NoError(t, err)
	go r.Run()

	subs := make([]*Subscriber, 5)
	for i := range subs {
		subs[i], _ = connect(t, r, string(rune('A'+i)))
	}
	require.NoError(t, r.submit(context.Background(), inCommand{From: subs[0], Cmd: game.StartGame{}}))
	require.NoError(t, r.submit(context.Background(), inCommand{From: subs[0], Cmd: game.BeginNight{}}))
	onLoop(t, r, func(*Room) {}) // sync: both commands processed

	require.Equal(t, int64(1), metricValue(t, "game.in_progress")-before,
		"a started, unfinished game is counted")

	// Tear the room down mid-game; the Run defer must release the gauge.
	require.NoError(t, r.Close(context.Background()))
	require.Equal(t, int64(0), metricValue(t, "game.in_progress")-before,
		"an abandoned game is released on teardown")
}

// minimalRoom builds a Room with just what appendAndBroadcast touches (no run
// loop, no subscribers), mirroring TestRoom_DisconnectSlowSubscriber.
func minimalRoom() *Room {
	return &Room{
		cfg:     Config{Logger: silentLogger()},
		log:     silentLogger(),
		g:       game.New(),
		players: make(map[game.PlayerID]*playerSlot),
		subs:    make(map[*Subscriber]struct{}),
	}
}
