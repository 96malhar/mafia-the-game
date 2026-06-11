package ws

import (
	"context"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/96malhar/mafia-the-game/internal/wire"
)

// Asserts the transport package's custom metrics emit with the expected name,
// type, value, and labels. Like internal/room/metrics_test.go: TestMain installs
// one ManualReader provider before any test runs (the lazy sync.Once binds to
// it once — no mid-suite reset, no race), and assertions use BEFORE/AFTER deltas
// so they're robust against the connection lifecycle in the handler tests, which
// also moves ws.connections.active / ws.message.rejected.

var testMeterReader sdkmetric.Reader

func TestMain(m *testing.M) {
	testMeterReader = sdkmetric.NewManualReader()
	otel.SetMeterProvider(sdkmetric.NewMeterProvider(sdkmetric.WithReader(testMeterReader)))
	os.Exit(m.Run())
}

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

func TestMetrics_WSConnectionsActiveGauge(t *testing.T) {
	before := metricValue(t, "ws.connections.active")
	recordConnOpen()
	recordConnOpen()
	recordConnOpen()
	recordConnClose()
	require.Equal(t, int64(2), metricValue(t, "ws.connections.active")-before)
}

func TestMetrics_WSMessageRejectedLabelled(t *testing.T) {
	badMsg := attribute.String("reason", string(wire.ErrCodeBadMessage))
	badFrame := attribute.String("reason", string(wire.ErrCodeBadFrame))
	beforeMsg := metricValue(t, "ws.message.rejected", badMsg)
	beforeFrame := metricValue(t, "ws.message.rejected", badFrame)

	recordMessageRejected(context.Background(), wire.ErrCodeBadMessage)
	recordMessageRejected(context.Background(), wire.ErrCodeBadMessage)
	recordMessageRejected(context.Background(), wire.ErrCodeBadFrame)

	require.Equal(t, int64(2), metricValue(t, "ws.message.rejected", badMsg)-beforeMsg)
	require.Equal(t, int64(1), metricValue(t, "ws.message.rejected", badFrame)-beforeFrame)
}
