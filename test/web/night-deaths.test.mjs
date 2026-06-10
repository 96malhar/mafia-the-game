import { test } from "node:test";
import assert from "node:assert/strict";
import { newApp, emit } from "./harness.mjs";

// Regression: refreshing the tab during a game replays the whole log. The
// dawn announcement ("Last night, X was killed") is driven by
// dayDiscussionPendingDeaths, which was only cleared inside the dawn narration
// — and that narration is skipped while replaying. So every past night's kills
// accumulated, and the next LIVE dawn announced the entire game's dead.
// Clearing the list at night-start (like lastNightVictims) scopes it to the
// current night.

const PLAYERS = [
  { id: "p1", name: "Alice" },
  { id: "p2", name: "Bob" },
  { id: "p3", name: "Cara" },
  { id: "p4", name: "Dee" },
  { id: "p5", name: "Eve" },
];

function rejoinReplay(app, events) {
  app.window.handleServerMessage({
    type: "rejoined",
    data: { playerId: "p1", name: "Alice", roomCode: "ABCD", isHost: true, fromSeq: 0, lastSeq: events.length, events },
  });
}

test("after a refresh, the dawn announces only last night's victims, not the whole game's dead", () => {
  const app = newApp();

  // Snapshot at refresh time: night 1 killed Bob and resolved; we're now in
  // night 2 (which hasn't resolved yet).
  const snapshot = [
    { type: "gameCreated", data: { minPlayers: 5, maxPlayers: 20, mafiaCount: 1 } },
    ...PLAYERS.map((p) => ({ type: "playerJoined", data: { playerId: p.id, name: p.name } })),
    { type: "roleAssigned", data: { playerId: "p1", role: "villager" } },
    { type: "phaseChanged", data: { from: "lobby", to: "night", day: 0 } },
    { type: "playerKilled", data: { playerId: "p2" } }, // night 1: Bob dies
    { type: "phaseChanged", data: { from: "night", to: "day_discussion", day: 1 } }, // night-1 dawn (replay-skipped)
    { type: "phaseChanged", data: { from: "day_discussion", to: "night", day: 1 } }, // night 2 begins
  ];
  rejoinReplay(app, snapshot);

  // Night 2 resolves LIVE: Cara dies, then the dawn fires (emit = not replaying).
  emit(app, "playerKilled", { playerId: "p3" });
  emit(app, "phaseChanged", { from: "night", to: "day_discussion", day: 2 });

  const card = app.$("narrator-card").textContent;
  assert.match(card, /Last night, Cara was killed/, "announces this night's victim");
  assert.doesNotMatch(card, /Bob/, "must NOT re-announce a previous night's victim");
});

test("a normal (non-refresh) two-night sequence still announces each night's victims correctly", () => {
  // Guards against over-correction: clearing at night-start must not wipe a
  // death that happens within the same live night before the dawn.
  const app = newApp();
  const snapshot = [
    { type: "gameCreated", data: { minPlayers: 5, maxPlayers: 20, mafiaCount: 1 } },
    ...PLAYERS.map((p) => ({ type: "playerJoined", data: { playerId: p.id, name: p.name } })),
    { type: "roleAssigned", data: { playerId: "p1", role: "villager" } },
  ];
  rejoinReplay(app, snapshot);

  // Night 1 (all live): Bob dies, dawn announces Bob.
  emit(app, "phaseChanged", { from: "lobby", to: "night", day: 0 });
  emit(app, "playerKilled", { playerId: "p2" });
  emit(app, "phaseChanged", { from: "night", to: "day_discussion", day: 1 });
  assert.match(app.$("narrator-card").textContent, /Last night, Bob was killed/);

  // Night 2 (all live): Cara dies, dawn announces Cara only (not Bob).
  emit(app, "phaseChanged", { from: "day_discussion", to: "night", day: 1 });
  emit(app, "playerKilled", { playerId: "p3" });
  emit(app, "phaseChanged", { from: "night", to: "day_discussion", day: 2 });
  const card = app.$("narrator-card").textContent;
  assert.match(card, /Last night, Cara was killed/);
  assert.doesNotMatch(card, /Bob/);
});
