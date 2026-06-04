import { test } from "node:test";
import assert from "node:assert/strict";
import { newApp, joinAs, emit } from "./harness.mjs";

test("app boots without script errors and shows the lobby", () => {
  const app = newApp();
  assert.equal(app.errors.length, 0, "no script errors on load");
  // The lobby headline renders from renderActionPanel's default branch.
  assert.match(app.$("phase-headline").textContent, /Lobby/);
});

test("joining adopts identity and renders the roster from the replayed backlog", () => {
  const app = newApp();
  joinAs(app, {
    playerId: "p1",
    name: "Alice",
    isHost: true,
    events: [
      { type: "gameCreated", data: { minPlayers: 6, maxPlayers: 20, mafiaCount: 1 } },
      { type: "playerJoined", data: { playerId: "p1", name: "Alice" } },
      { type: "playerJoined", data: { playerId: "p2", name: "Bob" } },
    ],
  });

  const text = app.$("players").textContent;
  assert.match(text, /Alice/);
  assert.match(text, /Bob/);
});
