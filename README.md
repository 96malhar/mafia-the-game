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
