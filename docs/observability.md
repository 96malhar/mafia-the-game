# Observability

How the server emits **logs** and **metrics**, and a complete catalog of the
custom metrics. Wiring lives in `internal/obs` (setup), `cmd/server/main.go`
(config), and the per-package `metrics.go` files (instruments).

Guiding principle, applied to both signals: **the pure game engine
(`internal/game`) emits no telemetry** — no logging, no metrics, no clock. All
observability lives in the transport / room / server layers. Keep it that way.

---

## Logging

Structured logging via the standard library's `log/slog`, configured once in
`obs.Setup` (`internal/obs/obs.go`) and threaded down through every layer.

| Aspect | Behavior |
|---|---|
| **Sink** | Always `stdout`. On Fly, `fly logs` streams stdout — there is no log exporter or file. |
| **Format** | `text` (default, for local dev) or `json` (prod), via `LOG_FORMAT`. |
| **Level** | `LOG_LEVEL` = `debug` \| `info` \| `warn` \| `error`. Default is **`debug`** (verbose for local dev); prod sets `info` (see `fly.toml`). |
| **Handler** | `slog.NewTextHandler` / `slog.NewJSONHandler` with the level threshold (`obs.consoleHandler`). |

### Philosophy: "log when something needs attention"

The level a line gets encodes whether a human should care:

- **Successful requests** — including `/healthz` and `/metrics` scrapes — log
  at **debug**, so they're silent at the prod `info` level. Set
  `LOG_LEVEL=debug` to get a full access log.
- **`4xx`** responses log at **warn**; **`5xx`** and recovered HTTP panics log
  at **error** (`internal/server/middleware.go` — `requestLogger`,
  `recoverer`).
- **Normal user-facing rejections** (wrong phase, duplicate name, voting
  yourself, a malformed WS frame, etc.) are **not logged at all** — they're
  returned to the client and tracked as **metrics** instead (see
  `game_command_rejected` / `ws_message_rejected`). This keeps the logs free of
  routine client-error noise.
- **Things that genuinely need attention** are logged loudly: a recovered
  **room-goroutine panic** logs at **error** with the panic value + full stack
  (`internal/room/recovery.go`), a phase timer that fires in an unexpected
  state logs at **warn**, a slow-subscriber disconnect at **warn**.

### Structured context

Loggers are derived with `.With(...)` so related lines share correlatable
fields:

- HTTP access log: `method`, `path`, `status`, `bytes`, `dur_ms`, `req_id`
  (chi `RequestID`), `remote`.
- Room logger: `room=<code>`.
- WebSocket pump loggers: `pump=read|write`, `room=<code>`.

> Note: there is currently no single session/correlation id threading an HTTP
> upgrade → join → commands → disconnect → rejoin together; lines correlate by
> `room` (and `req_id` on the HTTP side) at best. That's a known gap, not a
> bug.

### Logging config (env)

| Var | Default | Meaning |
|---|---|---|
| `LOG_LEVEL` | `debug` | `debug` \| `info` \| `warn` \| `error`. Prod uses `info`. |
| `LOG_FORMAT` | `text` | `text` \| `json`. Prod uses `json`. |
| `DEPLOY_ENV` | _(empty)_ | Sets the `deployment.environment` resource attribute (also tags metrics). |

---

## Metrics

OpenTelemetry instruments exported to **Prometheus via a pull model** — there
is no OTLP push.

### How it's wired

1. `obs.Setup` (`internal/obs/obs.go`) creates a **dedicated** Prometheus
   registry (not the global default, so the surface is exactly what we
   register plus the exporter's `target_info`), builds an
   `otelprom` exporter over it, installs a `metric.MeterProvider` as the
   **global** provider (`otel.SetMeterProvider`), and returns a
   `promhttp` handler.
2. `cmd/server/main.go` serves that handler at **`GET /metrics`** on its
   **own listener** (`METRICS_ADDR`, default `:9091`) — a separate port from
   the public app (`:8080`). On Fly the public proxy only routes ports under
   `[http_service]`, so `:9091` is reachable **only over Fly's private 6PN
   network**, where Fly's managed Prometheus scrapes it (the `[metrics]` block
   in `fly.toml`). Never internet-reachable. A failure binding the metrics
   port is logged but **non-fatal** — losing metrics must never take down live
   games.
3. Each package registers its instruments **lazily** on first use (a
   `sync.Once` `initMetrics`) against the already-installed global provider.

### Resource attributes

Every series carries (via `target_info`): `service.name=mafia`,
`service.version` (the build version from `-ldflags -X main.version`), and
`deployment.environment` (from `DEPLOY_ENV`).

### Naming: OTel → Prometheus

Instruments are named in OTel dotted style (`ws.connections.active`); the
Prometheus exporter rewrites dots to underscores, so you query
**`ws_connections_active`**. Both forms are listed in the catalog below.
Counters additionally get a **`_total`** suffix (`game.command.rejected` →
`game_command_rejected_total`); gauges (UpDownCounters) keep the bare name.
Every series also carries an **`otel_scope_name`** label identifying the
emitting package (e.g. `otel_scope_name="github.com/96malhar/mafia-the-game/internal/room"`).

> ⚠️ **Lazy-registration gotcha.** Because instruments are created on first
> use, a series does **not** appear in `/metrics` until the code path that
> emits it runs at least once since boot. e.g. `ws_connections_active` is
> absent until the first WebSocket connection opens. In PromQL, "absent" then
> means "never happened yet," not `0` — guard dashboards/alerts with
> `... or vector(0)` where a zero baseline matters.

### Metrics config (env)

| Var | Default | Meaning |
|---|---|---|
| `METRICS_ADDR` | `:9091` | Listener for `GET /metrics`. Keep off the public port. |
| `DEPLOY_ENV` | _(empty)_ | `deployment.environment` resource attribute on all series. |

---

## Custom metrics catalog

All nine custom instruments. Prometheus names (dots → underscores) are what
you query.

| Prometheus name | OTel name | Type | Labels | Meaning | Emitted from |
|---|---|---|---|---|---|
| `room_active` | `room.active` | UpDownCounter (gauge) | — | Rooms currently held in memory. `+1` on create, `-1` on reap. | `internal/room/manager.go` (`recordRoomOpened` in `CreateRoom`; `recordRoomClosed` in `reapWhenDone`) |
| `room_panic_total` | `room.panic` | Counter | — | Panics **recovered** in a room goroutine. Each is a real bug — the process survives (see [recovery](../internal/room/recovery.go)) but this should alert. | `internal/room/recovery.go` (`recordRoomPanic`) |
| `game_command_rejected_total` | `game.command.rejected` | Counter | `code` | Engine commands / joins rejected, by `wire.ErrorCode` (wrong phase, duplicate name, forbidden, lobby full, …). The single chokepoint counts every rejection so they need no log line. | `internal/room/errors.go` (`recordCommandRejected`, called from `errorFor`) |
| `game_started_total` | `game.started` | Counter | — | A game began (one `GameStarted` event). Distinct from `room_active` — a room can sit in the lobby and never start. | `internal/room/broadcast.go` (`recordGameLifecycle` in `appendAndBroadcast`) |
| `game_completed_total` | `game.completed` | Counter | `winner` | A game played to completion (one `GameEnded` event), by winning faction (`town`/`mafia`). Pair with `game_started_total` for the completion rate. Fires once per game; a `ResetGame` re-arms it and a panic-recovery replay does not re-fire it. | `internal/room/broadcast.go` (`recordGameLifecycle` in `appendAndBroadcast`) |
| `game_in_progress` | `game.in_progress` | UpDownCounter (gauge) | — | The **live** count of games currently being played (started, not yet ended/abandoned) — the real "active games" number, vs `room_active` which also counts lobbies and finished-but-unreset rooms. +1 on `GameStarted`, −1 on `GameEnded` or on room teardown if abandoned mid-play. | `internal/room/broadcast.go` (`recordGameLifecycle`) + `room.go` (`Run` teardown defer) |
| `game_duration_seconds` | `game.duration` | Histogram | — | Wall-clock length of a **completed** game (`StartGame` → win), in seconds. Only finished games are observed — an abandoned game never reaches `GameEnded`. Explicit buckets (15s…2h) drive percentiles via `histogram_quantile` over `game_duration_seconds_bucket` — e.g. p50: `histogram_quantile(0.5, sum(rate(game_duration_seconds_bucket[$__rate_interval])) by (le))`. | `internal/room/broadcast.go` (`recordGameLifecycle` in `appendAndBroadcast`) |
| `ws_connections_active` | `ws.connections.active` | UpDownCounter (gauge) | — | Currently-open WebSocket connections. `+1` after a successful upgrade, `-1` (deferred) on full teardown — properly paired, so it tracks live connections. | `internal/transport/ws/handler.go` (`recordConnOpen` / `recordConnClose` in `Connect`) |
| `ws_message_rejected_total` | `ws.message.rejected` | Counter | `reason` | Inbound WS frames rejected at the **transport** layer (bad frame, undecodable JSON, unknown type), by reason. The only server-side signal for malformed/abusive traffic, since these aren't logged. | `internal/transport/ws/pumps.go` (`recordMessageRejected`, from the read pump) |

> Prometheus appends `_total` to counter series exported from OTel; gauges
> (UpDownCounters) keep their bare name.

**Label cardinality** is bounded by design: `code` and `reason` both range over
the small, fixed `wire.ErrorCode` set; no per-room or per-player labels exist,
so the series count stays flat regardless of traffic. Keep it that way when
adding metrics.

### Also present (not custom)

- **`http_server_*`** — request duration/size/count for every HTTP request,
  from the `otelhttp` wrapper around the chi router (`internal/server/server.go`).
  The WebSocket upgrade flows through it too (`otelhttp` forwards `Hijack()`).
- **`target_info`** — the resource attributes above, from the Prometheus
  exporter.

---

## Tracing — wired but inactive

`otelhttp` would also produce spans, and `recordMessageRejected` adds a
`ws.message.rejected` span event — but **no `TracerProvider` is installed**
(`obs.Setup` sets only a `MeterProvider`). With the global no-op tracer, those
span paths are cheap no-ops, so:

- `http_server_*` **metrics** are real (they use the meter provider) ✅
- distributed **traces / spans** are **not emitted** ❌

To enable tracing later: construct a `trace.NewTracerProvider` (with an OTLP
exporter or a debug sink) and a propagator in `obs.Setup`, call
`otel.SetTracerProvider`, and the existing `otelhttp` wrapper + the
`span.IsRecording()`-guarded event in `recordMessageRejected` light up with no
other code changes.

---

## Viewing in production (Fly.io)

- **Logs:** `fly logs` (streams stdout; JSON at the prod `info` level).
- **Metrics:** scraped automatically via the `[metrics]` block in `fly.toml`
  (`port = 9091`, `path = "/metrics"`). View in Fly's hosted Grafana at
  [fly-metrics.net](https://fly-metrics.net) (or `fly dashboard metrics`).
- **Dashboard:** a prebuilt Grafana dashboard for the custom metrics lives at
  [`grafana-dashboard.json`](../grafana-dashboard.json) (repo root). Import it
  via Grafana → Dashboards → Import → upload the file, keep the UID
  `mafia-app-metrics` (so a re-import overwrites in place rather than
  duplicating), and select your Prometheus data source.
- **Health:** `GET /healthz` on the public port returns a static `200` — note
  it does **not** reflect internal health (manager/room-goroutine liveness); a
  green check does not guarantee rooms are advancing. Treat it as a liveness
  ping only.

---

## Adding a new metric

1. Declare the instrument in the owning package's `metrics.go`, inside the
   `initMetrics` `sync.Once` (mirror the existing ones). Use an OTel dotted
   name and a `WithUnit`.
2. Add a `record*` helper that calls `initMetrics()` then `.Add(...)`.
3. Call it from the **single chokepoint** for that event (like `errorFor` for
   rejections) so you don't sprinkle emit calls.
4. Keep labels low-cardinality (no player/room ids).
5. **Never** add metrics to `internal/game` — emit from the room/transport
   layer instead.
6. Update the catalog table above and the one-line list in `README.md`.
