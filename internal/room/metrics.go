package room

import (
	"context"
	"sync"

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
	metricsOnce sync.Once
	cmdRejected metric.Int64Counter
	roomsActive metric.Int64UpDownCounter
	roomPanics  metric.Int64Counter
)

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

func recordRoomOpened() {
	initMetrics()
	roomsActive.Add(context.Background(), 1)
}

func recordRoomClosed() {
	initMetrics()
	roomsActive.Add(context.Background(), -1)
}
