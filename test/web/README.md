# Frontend tests

Behavior tests for the browser client (`web/*.js`), run with Node's built-in
test runner against a [jsdom](https://github.com/jsdom/jsdom) DOM.

These are **dev-only**. The shipped app keeps its no-build-step design: plain
classic scripts sharing one global scope, served as-is. We do not bundle,
transpile, or convert the app to modules for testing. Instead the harness loads
the *real* `web/*.js` into a jsdom realm (in `index.html`'s real markup) and
drives the actual `handleServerMessage` / `handleEvent` handlers, asserting on
the resulting DOM — i.e. it tests the shipped code, not a copy.

## Running

```sh
task setup:web   # one-time: npm ci (installs jsdom)
task test:web    # node --test test/web/
```

`task check` (and CI) run `test:web` too; it **skips gracefully** when Node or
the deps are absent, so a Go-only environment never fails on it.

## How it works (`harness.mjs`)

- `newApp()` — strips the `<script src=...>` tags from `index.html`, creates a
  jsdom window, installs stubs (`WebSocket`, `fetch`, `navigator.clipboard`),
  then injects the seven scripts inline in load order. `speechSynthesis` is left
  **undefined**, which makes `speak()`/`narrate()` guarded no-ops, so tests run
  silently and deterministically.
- `joinAs` / `startGameAs` / `toNightRoleAct` — set up a seated game by feeding
  the same server frames the real client receives (the join ack's replayed
  backlog, `roleAssigned`, `mafiaRoster`, `phaseChanged`, `nightActionStarted`…).
- `emit(app, type, data)` — feeds one projected engine event through the live
  handler.
- `rowFor` / `badgeTexts` / `buttonTexts` / `hintText` / `modalText` — read
  observable DOM state (roster badges, action buttons, hints, the notice modal,
  the graveyard feed) so assertions check what a player would actually see.

Because the engine's `Project` already redacts per viewer, these tests deliver
events exactly as a given viewer's socket would receive them (e.g. a town player
is never sent `mafiaRoster`), so they exercise the client's rendering of the
*projected* stream — not redaction (that's covered by the Go projection tests).

## What's covered

- `badges.test.mjs` — roster faction badges: Yakuza distinct from Mafia, town
  sees none, the kill `Target` and recruit `Recruit` badges, graveyard reveal.
- `actions.test.mjs` — the per-row action buttons: the Yakuza's dual
  Kill/Recruit, a plain mafioso's single Kill, the doctor's `Save self`, a
  recruited player's suppressed picker, and day-vote buttons.
- `notices.test.mjs` — the notice modal (distracted / recruited / detective
  result) and the graveyard spectator feed's `recruited` verb.
