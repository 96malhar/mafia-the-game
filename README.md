# Mafia

[![CI](https://github.com/96malhar/mafia-the-game/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/96malhar/mafia-the-game/actions/workflows/ci.yml?query=branch%3Amain)
[![Release](https://img.shields.io/github/v/release/96malhar/mafia-the-game?sort=semver)](https://github.com/96malhar/mafia-the-game/releases/latest)
[![Go Version](https://img.shields.io/github/go-mod/go-version/96malhar/mafia-the-game)](go.mod)
[![Go Report Card](https://goreportcard.com/badge/github.com/96malhar/mafia-the-game)](https://goreportcard.com/report/github.com/96malhar/mafia-the-game)

A real-time, browser-based Mafia (Werewolf) game. Go backend, vanilla-JS frontend, WebSocket transport, event-driven game engine.

## Status

Playable end-to-end: create/join a room, automatic role assignment, the night/day phase loop, hidden-then-revealed day voting with a strict living-majority lynch, and win detection — all over WebSockets.

## Requirements

- Go 1.26 or newer (matches the toolchain in `go.mod`).

## Run

With [Task](https://taskfile.dev) (`brew install go-task`):

```sh
task run        # start the dev server
task --list     # see all available tasks
```

Or plain Go:

```sh
go run ./cmd/server
```

Then open <http://localhost:8080>. Override the listen address with `ADDR`:

```sh
ADDR=:9000 task run
```

Local dev needs no other configuration: the secure defaults work for same-origin access over `http://localhost:8080`.

## Configuration

All configuration is via environment variables (read in `cmd/server/main.go`):

| Variable                     | Default   | Purpose                                                                                  |
| ---------------------------- | --------- | ---------------------------------------------------------------------------------------- |
| `ADDR`                       | `:8080`   | Public TCP listen address (app + WebSocket).                                             |
| `METRICS_ADDR`               | `:9091`   | Separate listen address for `GET /metrics`. Kept off the public port so it isn't internet-reachable. |
| `LOG_LEVEL`                  | `debug`   | `debug` \| `info` \| `warn` \| `error`. Production sets `info` (see `fly.toml`).         |
| `LOG_FORMAT`                 | `text`    | `text` (human-readable) or `json` (structured, recommended for prod).                    |
| `DEPLOY_ENV`                 | _(empty)_ | Sets the `deployment.environment` resource attribute on exported metrics (e.g. `prod`). |
| `ALLOWED_ORIGINS`            | _(empty)_ | Comma-separated WebSocket origin allowlist. Empty enforces **same-origin**.              |
| `INSECURE_SKIP_ORIGIN_CHECK` | `false`   | Disables WS origin checking. **Never enable in production**; only for cross-origin dev.  |
| `TRUSTED_CLIENT_IP_HEADER`   | _(empty)_ | Proxy header to read the real client IP from (e.g. `Fly-Client-IP`). Empty = don't trust any header. |
| `ROOM_CREATE_RPM`            | `0`       | Per-IP rate limit on `POST /api/rooms` (requests/minute). `0` disables.                  |

## Deployment

The server is stateless on disk but keeps all room state **in memory in a single process**, so it must run as exactly **one instance** (no horizontal scaling without sticky-by-room routing).

### Docker

```sh
task docker:build                 # or: docker build -t mafia-the-game .
task docker:run                   # or: docker run --rm -p 8080:8080 mafia-the-game
```

A multi-stage build produces a small image: a static binary on a digest-pinned `distroless/static:nonroot` base (no shell, runs as a non-root user). The `web/` assets are embedded into the binary via `go:embed`, so the image is just the single self-contained executable — no `web/` directory to copy. Health is exposed at `GET /healthz` for orchestrator probes (the distroless image has no shell, so there's no in-image `HEALTHCHECK`).

### fly.io

`fly.toml` is included and tuned for this app: TLS terminates at fly's edge (`force_https`, so clients use `wss://` while the app speaks plain HTTP internally), a single always-on machine, a `/healthz` check, and env wiring for `TRUSTED_CLIENT_IP_HEADER=Fly-Client-IP` and a room-create rate limit.

```sh
fly launch --no-deploy   # first time: set app name + region
fly deploy
```

Edit `app`, `primary_region`, and (optionally) `ALLOWED_ORIGINS` in `fly.toml` before deploying.

## Observability

Logging is structured via `slog` and always goes to stdout (`text` locally, `json` in prod via `LOG_FORMAT`). The philosophy is "log when something needs attention": successful requests (including `/healthz` and `/metrics` scrapes) are dropped at the default `info` level and only appear under `LOG_LEVEL=debug`, while `4xx` log at warn, `5xx` and recovered panics at error. Normal user-facing rejections (wrong phase, duplicate name, voting yourself, malformed frames) are returned to the client and **not** logged — they're tracked as metrics instead.

Metrics use OpenTelemetry with a **Prometheus pull** model. `GET /metrics` serves them in Prometheus text format on a **dedicated port** (`METRICS_ADDR`, default `:9091`) that is intentionally separate from the public app port. On Fly this matters: the public proxy only routes ports declared under `[http_service]`, so by keeping `9091` out of that list the scrape endpoint is reachable only over Fly's private 6PN network — never from the internet — while Fly's managed Prometheus (configured via the `[metrics]` block in `fly.toml`) still scrapes it. View the series in Fly's hosted Grafana at [fly-metrics.net](https://fly-metrics.net). Alongside `otelhttp`'s `http_server_*` series, the app exposes custom instruments: `room_active`, `ws_connections_active`, `game_command_rejected{code}`, and `ws_message_rejected{reason}`. The pure game engine emits no telemetry; only the transport/room/server layers do.

## Common tasks

| Command           | What it does                                                  |
| ----------------- | ------------------------------------------------------------- |
| `task run`        | Start the dev server                                          |
| `task build`      | Build release binary to `bin/server`                          |
| `task test`       | Run tests with race detector                                  |
| `task test:cover` | Tests + open HTML coverage report                             |
| `task check`      | fmt + vet + lint + test (fast pre-commit gate, offline)       |
| `task vuln`       | Scan dependencies via `govulncheck` (needs network)           |
| `task ci`         | Full CI pipeline: `check` + `vuln`                            |
| `task tools`      | Install dev tools (`goimports`, `golangci-lint`, `govulncheck`) |
| `task docker:build` | Build the production image (`TAG=...`, default `latest`)    |
| `task docker:run` | Build then run the image, publishing `PORT` (default 8080)    |
| `task docker:smoke` | Build, boot, and verify `GET /healthz` (also run in CI)     |
| `task clean`      | Remove build artifacts                                        |

## Project layout

```
cmd/server/            HTTP server entry point: env config, logging, signals
internal/game/         pure, deterministic event-driven game engine
internal/room/         in-memory room/hub: an engine plus its WS subscribers
internal/transport/ws/ WebSocket upgrade, JSON codec, per-connection pumps
internal/wire/         stable string tags shared by every client and the server
internal/server/       HTTP server, routing, and middleware
web/                   static frontend (index.html + ordered vanilla-JS files, no build step)
```

## Architecture

A short tour of each package's concern, from the core outward:

- **`internal/game`** — the engine. A deterministic `Apply(command) -> ([]Event, error)` over in-memory state, with no I/O, time, or networking, so it replays and unit-tests trivially. It emits **full-truth** events; `projection.go` is the single place that redacts them per viewer based on each event's visibility (public / private / faction-only).
- **`internal/room`** — one goroutine-owned hub per game. It serializes inbound commands, runs them through the engine, and fans the projected events out to that room's WebSocket subscribers. A manager allocates short room codes and tracks live rooms. Everything is in memory.
- **`internal/transport/ws`** — upgrades HTTP to WebSocket, encodes/decodes the JSON message envelopes, and runs the read/write pumps for each connection. The bridge between raw sockets and the room's command/event channels.
- **`internal/wire`** — the stable string contract (message-type tags and event tags) shared by the server and the browser, so all clients agree on the protocol. Domain-enum spellings (Role, Phase, Faction) are not duplicated here: they are the engine's own string-typed values, written to the wire directly.
- **`internal/server`** — the chi HTTP server: routing, middleware (real client IP, security headers, body-size cap, per-IP rate limiting), static file serving, and the `/healthz` endpoint.
- **`web`** — the frontend: `index.html` plus a set of ordered, classic (non-module) vanilla-JS files (`helpers.js` → `render.js` → `actions.js` → `events.js` → `lobby.js` → `url.js` → `main.js`) that share one global scope, and `styles.css`. Tailwind via CDN; speaks the JSON-over-WebSocket protocol. Still no build step — the files are embedded and served as-is.

**Persistence**: none. All room state lives in memory in a single process, which is why the server runs as exactly one instance.
