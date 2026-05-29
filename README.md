# Mafia

A real-time, browser-based Mafia (Werewolf) game. Go backend, vanilla-JS
frontend, WebSocket transport, event-driven game engine.

## Status

Step 1 of 6: minimal HTTP server serving the landing page.

## Requirements

- Go 1.22 or newer (developed on 1.25).

## Run

With [Task](https://taskfile.dev) (`brew install go-task`):

```sh
task            # run the dev server
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

Local dev needs no other configuration: the secure defaults work for
same-origin access over `http://localhost:8080`.

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

The server is stateless on disk but keeps all room state **in memory in a
single process**, so it must run as exactly **one instance** (no horizontal
scaling without sticky-by-room routing).

### Docker

```sh
docker build -t mafia-the-game .
docker run --rm -p 8080:8080 mafia-the-game
```

A multi-stage build produces a ~9 MB image: a static binary on a
`distroless/static:nonroot` base. The `web/` assets are read from disk at
runtime, so they're copied into the image alongside the binary. Health is
exposed at `GET /healthz` for orchestrator probes (the distroless image has
no shell, so there's no in-image `HEALTHCHECK`).

### fly.io

`fly.toml` is included and tuned for this app: TLS terminates at fly's edge
(`force_https`, so clients use `wss://` while the app speaks plain HTTP
internally), a single always-on machine, a `/healthz` check, and env wiring
for `TRUSTED_CLIENT_IP_HEADER=Fly-Client-IP` and a room-create rate limit.

```sh
fly launch --no-deploy   # first time: set app name + region
fly deploy
```

Edit `app`, `primary_region`, and (optionally) `ALLOWED_ORIGINS` in
`fly.toml` before deploying.

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
| `task clean`      | Remove build artifacts                                        |

## Project layout

```
cmd/server/         entry point; wires config + signals
internal/server/    HTTP server, routing, middleware
internal/room/      (future) room/hub managing WS clients + game
internal/game/      (future) pure event-driven game engine
web/                static frontend (HTML/JS/CSS, no build step)
```

## Architecture (target)

- **Transport**: WebSocket + JSON between browser and Go server.
- **Engine**: pure `Apply(state, Command) -> ([]Event, error)`, fully testable.
- **Room**: owns the engine + an append-only event log + a set of WS
  subscribers. Per-player redaction happens in the projection layer.
- **Persistence**: none in v1. The room lives in memory.

See the design notes in chat history for the rationale on WebSocket vs gRPC
and why we skip a database for v1.
