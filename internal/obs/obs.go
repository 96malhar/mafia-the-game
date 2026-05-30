// Package obs wires up observability: structured logging plus an
// OpenTelemetry meter provider whose metrics are exposed for Prometheus
// to scrape.
//
// Design:
//   - Logging always goes to stdout via slog (text for local dev, JSON
//     for prod, selectable with LOG_FORMAT). On Fly, `fly logs` streams
//     stdout, so no log exporter is needed.
//   - Metrics use the pull model: Setup installs a Prometheus-backed
//     global MeterProvider and returns an http.Handler that renders the
//     metrics in Prometheus text format. The server mounts it at
//     /metrics, and Fly's managed Prometheus scrapes it (see the
//     [metrics] block in fly.toml). There is no OTLP push.
//   - The same global MeterProvider feeds otelhttp's http.server.*
//     metrics and our custom instruments (room.active, ws.connections
//     .active, the rejection counters), so they all land in /metrics.
//
// The pure game engine (internal/game) never imports this package; only
// the transport/room/server layers emit telemetry.
package obs

import (
	"io"
	"log/slog"
	"net/http"
	"os"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	otelprom "go.opentelemetry.io/otel/exporters/prometheus"
	"go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
)

// stdout is the log sink; a package var so tests could redirect it.
var stdout io.Writer = os.Stdout

// Config controls logging and the metrics resource. Zero values are
// sensible: text logs at info level.
type Config struct {
	ServiceName string     // resource service.name
	Version     string     // resource service.version
	Environment string     // resource deployment.environment (e.g. "prod")
	LogLevel    slog.Level // slog threshold for stdout
	LogFormat   string     // "text" (default) or "json"
}

// Setup configures slog and a Prometheus-backed OpenTelemetry meter
// provider. It returns the application logger and an http.Handler that
// serves the collected metrics in Prometheus text format; mount it at
// /metrics. The handler is always non-nil on success.
func Setup(cfg Config) (*slog.Logger, http.Handler, error) {
	logger := slog.New(consoleHandler(stdout, cfg.LogLevel, cfg.LogFormat))

	res, err := newResource(cfg)
	if err != nil {
		return nil, nil, err
	}

	// Use a dedicated registry rather than the global default so the
	// metrics surface is exactly what we register here (plus the
	// exporter's own target_info), with no process/Go collectors unless
	// we opt in.
	reg := prometheus.NewRegistry()
	exporter, err := otelprom.New(otelprom.WithRegisterer(reg))
	if err != nil {
		return nil, nil, err
	}
	mp := metric.NewMeterProvider(
		metric.WithResource(res),
		metric.WithReader(exporter),
	)
	otel.SetMeterProvider(mp)

	handler := promhttp.HandlerFor(reg, promhttp.HandlerOpts{})
	return logger, handler, nil
}

func newResource(cfg Config) (*resource.Resource, error) {
	attrs := []attribute.KeyValue{
		attribute.String("service.name", cfg.ServiceName),
		attribute.String("service.version", cfg.Version),
	}
	if cfg.Environment != "" {
		attrs = append(attrs, attribute.String("deployment.environment", cfg.Environment))
	}
	// Merge our attributes onto the SDK defaults (telemetry.sdk.*). The
	// extra resource is schemaless, so this never conflicts on schema URL.
	res, err := resource.Merge(resource.Default(), resource.NewSchemaless(attrs...))
	if err != nil {
		// Fall back to just our attributes rather than failing startup.
		return resource.NewSchemaless(attrs...), nil
	}
	return res, nil
}

func consoleHandler(w io.Writer, level slog.Level, format string) slog.Handler {
	opts := &slog.HandlerOptions{Level: level}
	if format == "json" {
		return slog.NewJSONHandler(w, opts)
	}
	return slog.NewTextHandler(w, opts)
}
