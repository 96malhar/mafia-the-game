import { test } from "node:test";
import assert from "node:assert/strict";
import { newApp, joinAs, emit, hintText } from "./harness.mjs";

// The lobby mirrors applyStartGame's roster rules (web/actions.js) to gate the
// host's "Start game" button and surface a hint. These tests pin the two
// rejection messages the host should see — kept in sync with the server's
// startBlockMessage (internal/room/errors.go).

const FIVE = [
  { id: "p1", name: "Alice" },
  { id: "p2", name: "Bob" },
  { id: "p3", name: "Cara" },
  { id: "p4", name: "Dan" },
  { id: "p5", name: "Eve" },
];

// lobbyAsHost joins p1 as host into a fresh 5-player lobby (minPlayers 5,
// maxPlayers 20, mafiaCount 1), optionally bumping the mafia count.
function lobbyAsHost(app, { mafiaCount = 1 } = {}) {
  joinAs(app, {
    playerId: "p1",
    name: "Alice",
    isHost: true,
    events: [
      { type: "gameCreated", data: { minPlayers: 5, maxPlayers: 20, mafiaCount: 1 } },
      ...FIVE.map((p) => ({ type: "playerJoined", data: { playerId: p.id, name: p.name } })),
    ],
  });
  if (mafiaCount !== 1) emit(app, "mafiaCountChanged", { from: 1, to: mafiaCount });
}

const hintClass = (app) => app.$("phase-hint").className;

test("a town-majority lobby is ready to start (green)", () => {
  const app = newApp();
  lobbyAsHost(app, { mafiaCount: 1 }); // 5 players: town 4 vs mafia 1
  assert.match(hintText(app), /Ready to start/);
  assert.match(hintClass(app), /text-emerald-400/, "ready hint is green");
});

test("too many mafia blocks Start with the town-majority prompt (red)", () => {
  const app = newApp();
  lobbyAsHost(app, { mafiaCount: 3 }); // 5 players: town (det+doc)=2 < mafia 3
  const hint = hintText(app);
  assert.match(hint, /more than half the seats/);
  assert.match(hint, /Yakuza or Consort/, "points the host at the real levers");
  assert.match(hintClass(app), /text-rose-400/, "blocking error hint is red");
});

test("a mafia-aligned optional can tip the lobby below a majority", () => {
  const app = newApp();
  lobbyAsHost(app, { mafiaCount: 2 });             // 5 players, 2 mafia
  emit(app, "consortChanged", { enabled: true });  // + Consort → 3 mafia-aligned vs 2 town
  assert.match(hintText(app), /more than half the seats/);
});

test("the roster summary lists the fixed Detective and Doctor plus villagers", () => {
  const app = newApp();
  lobbyAsHost(app, { mafiaCount: 1 }); // 5 players, no optionals
  const extras = app.$("action-extras").textContent;
  assert.match(extras, /Roster:/, "a roster readout is shown");
  assert.match(extras, /Detective, Doctor/, "the always-present town core is listed");
  assert.match(extras, /2 Villagers/, "5 - 1 mafia - 2 (det+doc) = 2 villagers");
});

test("the roster summary reflects an enabled mafia-aligned optional", () => {
  const app = newApp();
  lobbyAsHost(app, { mafiaCount: 1 });
  emit(app, "consortChanged", { enabled: true }); // 5p, 1 mafia + consort
  const extras = app.$("action-extras").textContent;
  // mafia side gains the Consort; villagers drop to 5 - 1 - 2 - 1 = 1.
  assert.match(extras, /1 Mafia, Consort/);
  // Singular "1 Villager" (not "Villagers"); the next chip's text follows it.
  assert.match(extras, /Detective, Doctor, 1 Villager(?!s)/);
});
