package room

import (
	"context"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/96malhar/mafia-the-game/internal/wire"
)

// meterName scopes this package's OpenTelemetry instruments.
const meterName = "github.com/96malhar/mafia-the-game/internal/room"

// Instruments are created lazily on first use rather than at import time:
// the global (Prometheus-backed) MeterProvider is installed by obs.Setup
// in main() before any room exists, so the first call here registers
// against it and the values surface at /metrics.
var (
	metricsOnce      sync.Once
	cmdRejected      metric.Int64Counter
	roomsActive      metric.Int64UpDownCounter
	roomPanics       metric.Int64Counter
	gamesStarted     metric.Int64Counter
	gamesCompleted   metric.Int64Counter
	gamesInProgress  metric.Int64UpDownCounter
	gameDuration     metric.Float64Histogram
	playersConnected metric.Int64UpDownCounter
)

// gameDurationBuckets are the explicit histogram boundaries (in seconds) for
// game.duration. Percentiles (p25/p50/p99) are computed query-side from the
// bucket counts via Prometheus histogram_quantile, so resolution comes entirely
// from these boundaries — they're placed densest across the range a real Mafia
// game lands in (a few minutes to ~half an hour) and trail off toward a 2-hour
// tail. 15s … 2h: a quick stomp, a typical session, and a marathon all fall in
// distinct buckets.
var gameDurationBuckets = []float64{
	15, 30, 60, 120, 180, 300, 450, 600, 900, 1200, 1800, 2700, 3600, 5400, 7200,
}

func initMetrics() {
	metricsOnce.Do(func() {
		m := otel.Meter(meterName)
		cmdRejected, _ = m.Int64Counter(
			"game.command.rejected",
			metric.WithDescription("Commands and joins rejected, labelled by wire error code"),
			metric.WithUnit("{rejection}"),
		)
		roomsActive, _ = m.Int64UpDownCounter(
			"room.active",
			metric.WithDescription("Number of rooms currently held in memory"),
			metric.WithUnit("{room}"),
		)
		roomPanics, _ = m.Int64Counter(
			"room.panic",
			metric.WithDescription("Panics recovered in a room goroutine (each is a bug to investigate)"),
			metric.WithUnit("{panic}"),
		)
		gamesStarted, _ = m.Int64Counter(
			"game.started",
			metric.WithDescription("Games started (StartGame applied), counted once per game"),
			metric.WithUnit("{game}"),
		)
		gamesCompleted, _ = m.Int64Counter(
			"game.completed",
			metric.WithDescription("Games played to completion (reached a win), labelled by winning faction"),
			metric.WithUnit("{game}"),
		)
		gamesInProgress, _ = m.Int64UpDownCounter(
			"game.in_progress",
			metric.WithDescription("Games currently being played (started, not yet ended or abandoned)"),
			metric.WithUnit("{game}"),
		)
		gameDuration, _ = m.Float64Histogram(
			"game.duration",
			metric.WithDescription("Wall-clock duration of a completed game, from StartGame to the win"),
			metric.WithUnit("s"),
			metric.WithExplicitBucketBoundaries(gameDurationBuckets...),
		)
		playersConnected, _ = m.Int64UpDownCounter(
			"players.connected",
			metric.WithDescription("Players currently connected and seated in a room (one per attached subscriber)"),
			metric.WithUnit("{player}"),
		)
	})
}

// recordCommandRejected counts a rejection by its wire error code. Called
// from the single error-mapping chokepoint (errorFor) so every rejection
// — wrong phase, duplicate name, forbidden, etc. — is captured as a
// metric, with no per-error log spam. Bounded cardinality: the set of
// wire.ErrorCode values is small and fixed.
func recordCommandRejected(code wire.ErrorCode) {
	initMetrics()
	cmdRejected.Add(context.Background(), 1, metric.WithAttributes(
		attribute.String("code", string(code)),
	))
}

// recordRoomPanic counts a panic recovered in a room goroutine. Recovery
// keeps the process alive, but every panic is a real bug, so this is the
// alertable signal that one occurred (paired with the Error log + stack).
func recordRoomPanic() {
	initMetrics()
	roomPanics.Add(context.Background(), 1)
}

// recordGameStarted counts a game beginning (one GameStarted event). Distinct
// from room.active: a room can sit in the lobby and never start a game.
func recordGameStarted() {
	initMetrics()
	gamesStarted.Add(context.Background(), 1)
}

// recordGameCompleted counts a game reaching a win (one GameEnded event),
// labelled by the winning faction. Paired with game.started, the ratio is the
// completion rate (games finished vs abandoned). winner is the bounded
// faction string ("town"/"mafia").
func recordGameCompleted(winner string) {
	initMetrics()
	gamesCompleted.Add(context.Background(), 1, metric.WithAttributes(
		attribute.String("winner", winner),
	))
}

// recordGameInProgress moves the live "games being played" gauge by delta
// (+1 when a game starts, -1 when it ends or is abandoned mid-play). Unlike
// game.started/completed (cumulative counters), this is the current count of
// active games — distinct from room.active, which also counts lobbies and
// finished-but-unreset rooms.
func recordGameInProgress(delta int64) {
	initMetrics()
	gamesInProgress.Add(context.Background(), delta)
}

// recordGameDuration records one completed game's wall-clock length (StartGame
// → win) into the game.duration histogram, in seconds (Prometheus base unit).
// Only completed games are observed — a game abandoned mid-play (room reaped /
// shut down) never reaches GameEnded and is deliberately absent, so the
// percentiles describe games that actually finished. The query-side
// histogram_quantile over the _bucket series yields p25/p50/p99.
func recordGameDuration(d time.Duration) {
	initMetrics()
	gameDuration.Record(context.Background(), d.Seconds())
}

// recordPlayerAttached / recordPlayerDetached move the live players.connected
// gauge as subscribers attach to / detach from a room's seat. They're the
// accurate "active players" signal: keyed on attached player seats, they ignore
// never-joined sockets and don't blip on a refresh (the room evicts the old
// subscriber and attaches the new one to the same seat on one goroutine, so the
// count holds steady). Called only from attachSubscriber / detachSubscriber, the
// single chokepoints for r.subs membership, so every +1 pairs with one -1.
func recordPlayerAttached() {
	initMetrics()
	playersConnected.Add(context.Background(), 1)
}

func recordPlayerDetached() {
	initMetrics()
	playersConnected.Add(context.Background(), -1)
}

func recordRoomOpened() {
	initMetrics()
	roomsActive.Add(context.Background(), 1)
}

func recordRoomClosed() {
	initMetrics()
	roomsActive.Add(context.Background(), -1)
}
