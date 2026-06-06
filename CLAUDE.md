# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

A real-time, browser-based Mafia game: Go backend, event-sourced game engine, WebSocket transport, vanilla-JS frontend (no build step). Go 1.26+. See `README.md` for environment-variable config, deployment (Docker / fly.io), and observability details — not repeated here.

## Commands

Tasks run via [Task](https://taskfile.dev) (`brew install go-task`). The `PKG` variable narrows scope (defaults to `./...`).

```sh
task run                          # start dev server on :8080
task check                        # fmt + vet + lint + race tests — the pre-commit gate
task test                         # all tests with -race
task test PKG=./internal/game/    # one package
task lint                         # golangci-lint (run `task tools` once to install)
task tools                        # install goimports, golangci-lint, govulncheck

task setup:web                    # one-time: npm ci (jsdom) for the frontend tests
task test:web                     # frontend tests (node:test + jsdom) — see test/web/

go test -race -run TestWinConditions ./internal/game/   # a single test
go test -race -run TestRoom ./internal/room/            # tests matching a prefix
```

`task check` is offline and is what CI gates on (plus `task vuln`). Always run it before considering a change done. Lint is strict — `goimports -local github.com/96malhar/mafia-the-game` formatting is enforced.

`task check` also runs the **frontend tests** (`task test:web`) — Node's test runner driving the real `web/*.js` in a jsdom DOM (see `test/web/README.md`). They are dev-only (the shipped app stays no-build-step) and skip gracefully if Node/`node_modules` are absent, so a Go-only run still passes; run `task setup:web` once to enable them.

## Architecture

Data flows **outward** from a pure engine; each layer adds exactly one concern. Read these in order to understand the whole.

### `internal/game` — the engine (pure, deterministic, no I/O)

The heart. `Apply(Command) ([]Event, error)` is the only entry point (`game.go`); it type-switches to per-command `apply*` handlers. **`Command` and `Event` are closed interfaces** (unexported `isCommand()`/`isEvent()` marker methods) — only this package can define new shapes, and the `Apply` switch is compiler-exhaustive.

Two invariants make the engine easy to reason about:

- **Event-sourced**: events are immutable facts appended to a log; all state and all per-player views derive from replaying them. The engine has no clock, no randomness beyond a seeded RNG, and no networking — so it replays and unit-tests trivially.
- **Full-truth then redact**: the engine **never hides anything**. Every `Event` carries a `Visibility` tag (`public` / `player` / `faction` / `dead`), and `projection.go`'s `Project(viewer, events, state)` is the *single* place that redacts the log per viewer. When touching anything secret (mafia roster, detective result, consort block, roster reveals), the rule to enforce lives in projection + the event's `Visibility()`, not in the handlers.

The night runs as a **per-role sub-phase state machine** driven by the internal `AdvancePhase` command (the room's wall-clock timer fires it): `opening → [narrate → act → ponder → sleep → settle] per role → resolve`. A role with no living holder (or a spent one-shot, or a roleblocked actor) becomes a *phantom* turn that skips the act window but keeps the cadence so observers can't infer who's absent. `rules_phase.go` / `rules_night.go` / `rules_day.go` hold this logic; `docs/role-state-transitions.md` diagrams it; `guards.go` holds the shared phase/win/death-resolution helpers (e.g. `resolveDeathsAndMaybeEnd`, which both death paths must share so the promote-before-win-check, reveal-after-promotion ordering can't drift).

### `internal/room` — the hub (one goroutine per game, all in memory)

Each `Room` is a single-goroutine actor: inbound `inJoin`/`inRejoin`/`inLeave`/`inCommand` messages are serialized through a channel (`inbound.go`), run through the engine, and the resulting events are **projected per subscriber** and fanned out (`broadcast.go`, `outbound.go`). Because the room goroutine is the sole owner of engine + subscriber state, the engine needs no locks. `manager.go` allocates short room codes and reaps idle/expired rooms. A subscriber's outbound channel is closed on teardown; the room checks `Subscriber.live()` before acting on any inbound so a stray post-teardown frame can't panic the goroutine. Sentinel→wire error mapping is centralized in `errors.go` (`errorFor` / `errorWithMsg`).

### `internal/transport/ws` — sockets ↔ room

Upgrades HTTP to WebSocket and runs two pumps per connection (`pumps.go`): a read pump (sole reader) and write pump (sole writer), wired so either's exit cancels the shared context and unwinds both. `codec.go` translates the JSON envelope to/from engine commands — `commandFromClient` is the **single source of truth** for which client tags map to engine commands (don't re-list tags elsewhere; an earlier duplicate list silently dropped a new command). `handler.go` enforces a join deadline that reaps never-authenticated connections.

### `internal/wire` — the protocol contract

Stable string tags shared by server and browser so all clients agree. Note: domain enums (Role, Phase, Faction) are **not** duplicated here — they're the engine's own string-typed values written to the wire directly.

### `internal/server` — HTTP edge

chi router + middleware (real client IP, security headers, body cap, per-IP rate limit), static file serving, `/healthz`. `web/` assets are `go:embed`-ed into the binary.

### `web` — frontend (no build step)

`index.html` + ordered classic (non-module) vanilla-JS files sharing one global scope: `helpers.js → render.js → actions.js → events.js → lobby.js → url.js → main.js`. Tailwind via CDN. Speaks the JSON-over-WebSocket protocol.

## Adding a new command end-to-end

A new engine command typically touches several layers in lockstep:

1. `internal/game/command.go` — define the type + `isCommand()`; add a case to `Apply` and an `apply*` handler.
2. `internal/game/event.go` — any new event type + its `Visibility()`; `projection.go` if redaction is non-trivial.
3. `internal/wire/wire.go` — the stable client/server tag.
4. `internal/transport/ws/` — `protocol.go` payload struct, `codec.go` `commandFromClient` mapping.
5. `internal/room/` — only if it needs host-only gating (`isHostOnly`) or special routing.
6. `web/` — client send + event handling.

## Adding a new role end-to-end

A role fans out further than a command. The engine **registry** (`rolespec.go`) drives most of it, but the night cadence, the optional-role toggle, the clients, and the docs all need touching. Using the Tracker as the worked example:

**Engine (`internal/game`)** — the registry is the single source of truth; `TestEveryRoleHasASpec` fails until the spec exists:
1. `role.go` — the `Role` constant (+ a doc comment on what it does).
2. `rolespec.go` — a `roleSpecs` entry: `Faction` and, for an acting role, a `NightAction` with its `Phase` / `Validate` / `Apply`. `Role.Valid()`, `Role.Faction()`, `allRoles()` all read from here.
3. `rules_phase.go` — `beginNightTurns` appends the role to `nightTurnQueue` **in wake order** (position matters: the Tracker reads everyone else's targets, so it wakes last).
4. Result delivery — either an immediate private result emitted in `applyNightAction` (`rules_night.go`, like the detective/tracker) **or** the spec's reveal-phase `Apply`. New result/notice events go in `event.go` with a `Visibility()`; redaction stays in `projection.go` (full-truth-then-redact), never the handler.

**Optional (host-toggled) role** adds a lobby-toggle slice: `state.go` (`xEnabled` field), `command.go` (`SetX`), `game.go` (`Apply` case), `rules.go` (`applySetX` via `applyLobbyToggle` + an `optionalRoles` table entry + a reset line in `applyResetGame`), `event.go` (`XChanged`, public), `export_test.go` (an `XEnabled` test accessor). `composeRoster` consumes the `optionalRoles` table automatically.

**Transport / room** — `wire/wire.go` tags (`ClientMsgSetX`, `EventXChanged`, `EventXResult`); `transport/ws/protocol.go` payload struct + `clientMsgType`; `transport/ws/codec.go` inbound decode + `commandFromClient` + **both** outbound encode cases (the encoder errors on unknown event types); `room/room.go` `isHostOnly` for the toggle; `room/config.go` **only** for custom sub-phase timing — a night role otherwise falls through to the defaults (e.g. the result-modal ponder reuses `DefaultPonderResultSubmit`).

**Frontend (`web/`)** — `helpers.js` (state mirror, `ROLE_VERBS`, a result toast), `actions.js` (toggle render + lobby append, act-window phrasing), `events.js` (`xChanged`/`xResult` handlers, optional-role slot count, `ROLE_NARRATION`/`ROLE_SLEEP` cues), `render.js` (`canActAtNight`, reset block), `index.html` (role-guide entry).

**Tests + docs** — a behavior-driven `internal/game/<role>_test.go` suite (mirror `detective_test.go` / `consort_test.go`), plus cases in `projection_test.go` and `spectator_test.go`; `test/web/` notices/actions/roleguide tests; and a role section in `docs/role-state-transitions.md`.

## Conventions

- **Single instance only**: all room state is in memory in one process. No persistence, no horizontal scaling without sticky-by-room routing.
- **Engine emits no telemetry**: metrics/logging live in the transport/room/server layers. Keep `internal/game` free of I/O, time, and observability.
- **User-facing rejections are not logged** (wrong phase, duplicate name, bad frame, etc.) — they return to the client and are tracked as metrics (`game_command_rejected{code}`, `ws_message_rejected{reason}`). Reserve logs for things that need attention.
- **Tests are behavior-driven through the public API.** The `game_test` package drives real command sequences via shared fixtures/helpers in `helpers_test.go` (e.g. `fixedRoster`, `runNightToDay`, `finalizeLynch`, `mkEndedGame`) rather than poking internal state; mirror that style. Comments in this codebase are unusually thorough and explain *why* — preserve that density when editing.
