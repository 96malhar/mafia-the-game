# Mafia

A real-time, browser-based Mafia (Werewolf) game. Go backend, vanilla-JS frontend, WebSocket transport, event-driven game engine.

## Status

Playable end-to-end: create/join a room, automatic role assignment, the night/day phase loop, hidden-then-revealed day voting with a strict living-majority lynch, and win detection — all over WebSockets.

## Requirements

- Go 1.25 or newer (matches the toolchain in `go.mod`).

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
| `ADDR`                       | `:8080`   | TCP listen address.                                                                      |
| `LOG_LEVEL`                  | `info`    | `debug` \| `info` \| `warn` \| `error`.                                                  |
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

A multi-stage build produces a small image: a static binary on a digest-pinned `distroless/static:nonroot` base (no shell, runs as a non-root user). The `web/` assets are read from disk at runtime, so they're copied into the image alongside the binary. Health is exposed at `GET /healthz` for orchestrator probes (the distroless image has no shell, so there's no in-image `HEALTHCHECK`).

### fly.io

`fly.toml` is included and tuned for this app: TLS terminates at fly's edge (`force_https`, so clients use `wss://` while the app speaks plain HTTP internally), a single always-on machine, a `/healthz` check, and env wiring for `TRUSTED_CLIENT_IP_HEADER=Fly-Client-IP` and a room-create rate limit.

```sh
fly launch --no-deploy   # first time: set app name + region
fly deploy
```

Edit `app`, `primary_region`, and (optionally) `ALLOWED_ORIGINS` in `fly.toml` before deploying.

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
cmd/sim/               headless bot harness that plays one full game over the wire
internal/game/         pure, deterministic event-driven game engine
internal/room/         in-memory room/hub: an engine plus its WS subscribers
internal/transport/ws/ WebSocket upgrade, JSON codec, per-connection pumps
internal/wire/         stable string tags shared by every client and the server
internal/server/       HTTP server, routing, and middleware
web/                   static frontend (single index.html, no build step)
```

## Architecture

A short tour of each package's concern, from the core outward:

- **`internal/game`** — the engine. A deterministic `Apply(command) -> ([]Event, error)` over in-memory state, with no I/O, time, or networking, so it replays and unit-tests trivially. It emits **full-truth** events; `projection.go` is the single place that redacts them per viewer based on each event's visibility (public / private / faction-only).
- **`internal/room`** — one goroutine-owned hub per game. It serializes inbound commands, runs them through the engine, and fans the projected events out to that room's WebSocket subscribers. A manager allocates short room codes and tracks live rooms. Everything is in memory.
- **`internal/transport/ws`** — upgrades HTTP to WebSocket, encodes/decodes the JSON message envelopes, and runs the read/write pumps for each connection. The bridge between raw sockets and the room's command/event channels.
- **`internal/wire`** — the stable string contract (message-type tags, event tags, and the on-wire spellings of domain enums) shared by the server, the browser, and the sim, so all clients agree on the protocol.
- **`internal/server`** — the chi HTTP server: routing, middleware (real client IP, security headers, body-size cap, per-IP rate limiting), static file serving, and the `/healthz` endpoint.
- **`web`** — a single `index.html` (vanilla JS + Tailwind via CDN) that speaks the JSON-over-WebSocket protocol; no build step.

**Persistence**: none. All room state lives in memory in a single process, which is why the server runs as exactly one instance.
