import { test } from "node:test";
import assert from "node:assert/strict";
import { newApp } from "./harness.mjs";

// setName fills the lobby name input and refreshes the buttons' disabled
// state exactly as a real keystroke would (showUnjoinableRoom reads the
// same input when re-enabling Create).
function setName(app, name) {
  app.$("name").value = name;
  app.window.refreshLobbyButtons();
}

test("showUnjoinableRoom pivots the lobby to Create with the typed name", () => {
  const app = newApp();
  setName(app, "Malhar");

  app.window.showUnjoinableRoom("ABCD", "Room ABCD doesn't exist. Create a new room with your name.");

  // Stays on the lobby (never falls through to the in-game view).
  assert.ok(!app.$("lobby").classList.contains("hidden"), "lobby visible");
  assert.ok(app.$("game").classList.contains("hidden"), "game hidden");

  // Reason is surfaced as the subtitle, and the room is named in the title.
  assert.match(app.$("lobby-title").textContent, /Room ABCD unavailable/);
  assert.match(app.$("lobby-subtitle").textContent, /doesn't exist/);
  // The reason reads as an error, in red.
  assert.ok(app.$("lobby-subtitle").classList.contains("text-rose-400"), "reason shown in red");

  // Create takes over from Join (the target room can't accept us) and is
  // enabled because a name is present — one click creates a fresh room
  // with that name.
  assert.ok(!app.$("create").classList.contains("hidden"), "create shown");
  assert.ok(app.$("join").classList.contains("hidden"), "join hidden");
  assert.equal(app.$("create").disabled, false, "create enabled with a name");
  assert.equal(app.$("name").value, "Malhar", "typed name preserved");

  // The URL is cleared so a reload lands on a clean lobby rather than
  // re-running the doomed join.
  assert.equal(app.window.location.search, "", "room param cleared from URL");
});

test("showUnjoinableRoom keeps Create disabled until a name is entered", () => {
  const app = newApp();
  // No name typed.
  app.window.showUnjoinableRoom("ABCD", "This game is already in progress. Create a new room to play.");

  assert.ok(!app.$("create").classList.contains("hidden"), "create shown");
  assert.equal(app.$("create").disabled, true, "create gated on a name");
  assert.match(app.$("lobby-subtitle").textContent, /already in progress/);
});

test("joining a non-existent room (404 probe) offers to create one", async () => {
  const app = newApp({ hostname: "example.com" }); // localStorage credStore, no stored creds
  setName(app, "Alice");
  // The room-existence probe 404s for a code that isn't there.
  app.window.fetch = () => Promise.resolve({ status: 404 });

  await app.window.joinRoom("WXYZ");

  assert.match(app.$("lobby-title").textContent, /Room WXYZ unavailable/);
  assert.ok(!app.$("create").classList.contains("hidden"), "create shown after 404");
  assert.equal(app.$("create").disabled, false, "create enabled with a name");
  assert.ok(app.$("game").classList.contains("hidden"), "never entered the game view");
});

// jsonRes builds a fetch-like response stub carrying a JSON body, matching the
// shape probeRoom reads (status + json()).
function jsonRes(status, body) {
  return { status, json: () => Promise.resolve(body) };
}

test("joining an in-progress room (joinable:false) offers to create one", async () => {
  const app = newApp({ hostname: "example.com" }); // localStorage credStore, no stored creds
  setName(app, "Alice");
  // The room exists but its game has started: the probe returns the
  // server's player-facing message, not a 404.
  app.window.fetch = () =>
    Promise.resolve(
      jsonRes(200, {
        code: "WXYZ",
        joinable: false,
        reason: "wrong_phase",
        message: "This game is already in progress. Create a new room to play.",
      }),
    );

  await app.window.joinRoom("WXYZ");

  assert.match(app.$("lobby-title").textContent, /Room WXYZ unavailable/);
  assert.match(app.$("lobby-subtitle").textContent, /already in progress/);
  assert.ok(!app.$("create").classList.contains("hidden"), "create shown");
  assert.equal(app.$("create").disabled, false, "create enabled with a name");
  assert.ok(app.$("game").classList.contains("hidden"), "never entered the game view");
});

// --- maybeProbeRoomFromURL: auto-detect on landing ------------------------
//
// A share link (?room=CODE) with no stored creds should NOT make the visitor
// type a name and click Join just to discover the room is gone or in progress.
// maybeProbeRoomFromURL probes up front and flips to "create a new room" the
// moment the target can't be joined.

// landOnRoom puts ?room=CODE in the address bar and reshapes the lobby to the
// share-link join view, exactly as page load does before the probe runs.
function landOnRoom(app, code) {
  app.window.history.replaceState(null, "", `/?room=${code}`);
  app.window.applyURLState();
}

test("landing on a share link to a missing room auto-offers create", async () => {
  const app = newApp({ hostname: "example.com" });
  landOnRoom(app, "GONE");
  app.window.fetch = () => Promise.resolve({ status: 404 });

  const launched = await app.window.maybeProbeRoomFromURL();

  assert.equal(launched, true, "a room in the URL launches a probe");
  assert.match(app.$("lobby-title").textContent, /Room GONE unavailable/);
  assert.match(app.$("lobby-subtitle").textContent, /doesn't exist/);
  assert.ok(!app.$("create").classList.contains("hidden"), "create shown");
  assert.ok(app.$("join").classList.contains("hidden"), "join hidden");
});

test("landing on a share link to an in-progress room auto-offers create", async () => {
  const app = newApp({ hostname: "example.com" });
  landOnRoom(app, "PLAY");
  app.window.fetch = () =>
    Promise.resolve(
      jsonRes(200, {
        code: "PLAY",
        joinable: false,
        message: "This game is already in progress. Create a new room to play.",
      }),
    );

  await app.window.maybeProbeRoomFromURL();

  assert.match(app.$("lobby-title").textContent, /Room PLAY unavailable/);
  assert.match(app.$("lobby-subtitle").textContent, /already in progress/);
  assert.ok(!app.$("create").classList.contains("hidden"), "create shown");
  assert.ok(app.$("join").classList.contains("hidden"), "join hidden");
});

test("landing on a share link to a joinable room keeps the join view", async () => {
  const app = newApp({ hostname: "example.com" });
  landOnRoom(app, "OPEN");
  app.window.fetch = () => Promise.resolve(jsonRes(200, { code: "OPEN", joinable: true }));

  await app.window.maybeProbeRoomFromURL();

  // A joinable room leaves the normal "Join room OPEN" prompt untouched.
  assert.match(app.$("lobby-title").textContent, /Join room OPEN/);
  assert.ok(!app.$("join").classList.contains("hidden"), "join still offered");
  assert.ok(app.$("create").classList.contains("hidden"), "create still hidden");
});

test("a share link we hold credentials for skips the join probe", async () => {
  const app = newApp({ hostname: "example.com" });
  landOnRoom(app, "MINE");
  // Stored rejoin creds → tryAutoRejoin owns this link; the probe must not run
  // (a rejoin to an in-progress game is exactly what we want).
  app.window.localStorage.setItem(
    "mafia.room.MINE",
    JSON.stringify({ playerId: "p1", secret: "s" }),
  );
  let fetched = false;
  app.window.fetch = () => {
    fetched = true;
    return Promise.resolve(jsonRes(200, {}));
  };

  const launched = await app.window.maybeProbeRoomFromURL();

  assert.equal(launched, true, "still reports the URL carried a room");
  assert.equal(fetched, false, "no probe when creds exist (auto-rejoin owns it)");
  // The share-link join view is left intact for the rejoin path.
  assert.match(app.$("lobby-title").textContent, /Join room MINE/);
});
