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
