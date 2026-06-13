package ws

import (
	"context"
	"sync"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"

	"github.com/96malhar/mafia-the-game/internal/wire"
)

// meterName scopes this package's OpenTelemetry instruments.
const meterName = "github.com/96malhar/mafia-the-game/internal/transport/ws"

// Lazily initialised; the global (Prometheus-backed) MeterProvider is set
// by obs.Setup before any connection is served, so these surface at
// /metrics.
// Live player count is NOT tracked here: a WebSocket connection is a poor
// proxy for an active player (a never-joined socket counts, a refresh
// transiently double-counts, two tabs read as two). That gauge lives in the
// room layer as players.connected, keyed on attached player seats. This
// package only owns transport-level frame rejections.
var (
	metricsOnce sync.Once
	msgRejected metric.Int64Counter
)

func initMetrics() {
	metricsOnce.Do(func() {
		m := otel.Meter(meterName)
		msgRejected, _ = m.Int64Counter(
			"ws.message.rejected",
			metric.WithDescription("Inbound WS frames rejected at the transport layer, by reason"),
			metric.WithUnit("{message}"),
		)
	})
}

// recordMessageRejected counts a transport-level rejection (bad frame,
// undecodable message, unknown type) by reason and, when the context
// carries a recording span, attaches it as a span event. These are
// returned to the client but intentionally not logged, so this is the
// only server-side signal for malformed/abusive traffic.
func recordMessageRejected(ctx context.Context, code wire.ErrorCode) {
	initMetrics()
	msgRejected.Add(ctx, 1, metric.WithAttributes(
		attribute.String("reason", string(code)),
	))
	if span := trace.SpanFromContext(ctx); span.IsRecording() {
		span.AddEvent("ws.message.rejected", trace.WithAttributes(
			attribute.String("reason", string(code)),
		))
	}
}
