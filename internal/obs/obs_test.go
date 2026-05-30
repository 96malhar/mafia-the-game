package obs

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
)

// TestConsoleHandler_Format pins the LOG_FORMAT wiring: "json" must
// produce machine-parseable JSON, while the default/"text" must not.
// A regression here is invisible until you're staring at unparseable
// logs in a collector.
func TestConsoleHandler_Format(t *testing.T) {
	t.Run("json format emits parseable JSON", func(t *testing.T) {
		var buf bytes.Buffer
		l := slog.New(consoleHandler(&buf, slog.LevelInfo, "json"))
		l.Info("hello", "k", "v")

		var m map[string]any
		require.NoError(t, json.Unmarshal(buf.Bytes(), &m),
			"json format must emit a single parseable JSON object")
		require.Equal(t, "hello", m["msg"])
		require.Equal(t, "v", m["k"])
	})

	t.Run("default format is text, not JSON", func(t *testing.T) {
		var buf bytes.Buffer
		l := slog.New(consoleHandler(&buf, slog.LevelInfo, ""))
		l.Info("hello")

		require.Contains(t, buf.String(), "hello")
		var m map[string]any
		require.Error(t, json.Unmarshal(buf.Bytes(), &m),
			"text output must not parse as JSON")
	})
}

// TestConsoleHandler_Level pins the LOG_LEVEL threshold: records below
// the configured level are dropped, records at/above it are emitted.
func TestConsoleHandler_Level(t *testing.T) {
	var buf bytes.Buffer
	l := slog.New(consoleHandler(&buf, slog.LevelInfo, "json"))

	l.Debug("dropped-below-threshold")
	require.Empty(t, buf.String(), "debug record must be suppressed at info level")

	l.Warn("kept-above-threshold")
	require.Contains(t, buf.String(), "kept-above-threshold")
}

// TestSetup_MetricsHandlerRendersRegisteredMetric exercises the whole
// pull pipeline: Setup installs a Prometheus-backed global MeterProvider
// and returns the scrape handler; a counter driven through that provider
// must show up in the handler's output, alongside the resource
// attributes the exporter promotes to target_info.
//
// NB: Setup calls otel.SetMeterProvider (process-global state), so this
// test must stay serial — do NOT add t.Parallel() or a second Setup test
// racing on the otel global.
func TestSetup_MetricsHandlerRendersRegisteredMetric(t *testing.T) {
	logger, handler, err := Setup(Config{
		ServiceName: "mafia-test",
		Version:     "v9.9.9",
		Environment: "test",
		LogLevel:    slog.LevelInfo,
		LogFormat:   "json",
	})
	require.NoError(t, err)
	require.NotNil(t, logger)
	require.NotNil(t, handler)

	// Drive a custom instrument through the global provider Setup just
	// installed; the exporter appends _total to the counter on render.
	ctr, err := otel.Meter("obs_test").Int64Counter("obs_test_calls")
	require.NoError(t, err)
	ctr.Add(context.Background(), 3)

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()

	require.Contains(t, body, "obs_test_calls",
		"a metric driven through the global provider must reach /metrics")
	// target_info carries the resource attributes (dots become
	// underscores in the Prometheus translation).
	require.Contains(t, body, "target_info")
	require.Contains(t, body, `service_name="mafia-test"`)
	require.Contains(t, body, `service_version="v9.9.9"`)
	require.Contains(t, body, `deployment_environment="test"`)
}
