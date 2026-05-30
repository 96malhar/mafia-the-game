package ws

import (
	"context"
	"sync"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"

	"github.com/malhar/mafia-the-game/internal/wire"
)

// meterName scopes this package's OpenTelemetry instruments.
const meterName = "github.com/malhar/mafia-the-game/internal/transport/ws"

// Lazily initialised; the global (Prometheus-backed) MeterProvider is set
// by obs.Setup before any connection is served, so these surface at
// /metrics.
var (
	metricsOnce sync.Once
	wsActive    metric.Int64UpDownCounter
	msgRejected metric.Int64Counter
)

func initMetrics() {
	metricsOnce.Do(func() {
		m := otel.Meter(meterName)
		wsActive, _ = m.Int64UpDownCounter(
			"ws.connections.active",
			metric.WithDescription("Currently open WebSocket connections"),
			metric.WithUnit("{connection}"),
		)
		msgRejected, _ = m.Int64Counter(
			"ws.message.rejected",
			metric.WithDescription("Inbound WS frames rejected at the transport layer, by reason"),
			metric.WithUnit("{message}"),
		)
	})
}

func recordConnOpen() {
	initMetrics()
	wsActive.Add(context.Background(), 1)
}

func recordConnClose() {
	initMetrics()
	wsActive.Add(context.Background(), -1)
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
