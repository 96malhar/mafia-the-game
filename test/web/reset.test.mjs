import { test } from "node:test";
import assert from "node:assert/strict";
import {
  newApp, emit, startGameAs,
  rows, rowFor, badgeTexts, hintText, headlineText, myRoleText,
} from "./harness.mjs";

const FIVE = [
  { id: "p1", name: "Alice" },
  { id: "p2", name: "Bob" },
  { id: "p3", name: "Cara" },
  { id: "p4", name: "Dee" },
  { id: "p5", name: "Eve" },
];

const FINAL_ROLES = {
  p1: "mafia",
  p2: "villager",
  p3: "detective",
  p4: "doctor",
  p5: "villager",
};

// extrasButtons returns the labels of the buttons in the action panel's
// "extras" slot (where the host's lobby/end-game controls render).
function extrasButtons(app) {
  return [...app.$("action-extras").querySelectorAll("button")].map((b) =>
    b.textContent.trim(),
  );
}

// endGameAs sets up a started game from `me`'s perspective and drives it to
// the ended screen (Town wins, all roles revealed).
function endGameAs(app, me) {
  startGameAs(app, { me, myRole: FINAL_ROLES[me], players: FIVE });
  emit(app, "phaseChanged", { from: "day_vote", to: "ended", day: 1 });
  emit(app, "gameEnded", { winner: "town", finalRoles: FINAL_ROLES });
}

test("host sees a 'Start new game' button on the ended screen", () => {
  const app = newApp();
  endGameAs(app, "p1"); // p1 is players[0] → host

  assert.equal(headlineText(app), "Town wins");
  assert.ok(
    extrasButtons(app).includes("Start new game"),
    "host should get a Start new game button",
  );
  assert.match(hintText(app), /Start a new game/);
});

test("non-host sees no restart button, just a waiting note", () => {
  const app = newApp();
  endGameAs(app, "p2"); // p2 is not the host

  assert.equal(headlineText(app), "Town wins");
  assert.ok(
    !extrasButtons(app).includes("Start new game"),
    "non-host must not get the restart button",
  );
  assert.match(hintText(app), /Waiting for the host/);
});

test("gameReset returns the client to a fresh lobby with the same players", () => {
  const app = newApp();
  endGameAs(app, "p1");

  // Sanity: the ended screen revealed roles and set our own role.
  assert.equal(myRoleText(app), "mafia");
  assert.ok(
    badgeTexts(rowFor(app, "Cara")).includes("Detective"),
    "ended screen reveals roles",
  );

  // The server broadcasts a self-contained lobby snapshot, then reaffirms
  // the host (exactly what room.handleReset emits).
  emit(app, "gameReset", {
    players: FIVE.map((p) => ({ playerId: p.id, name: p.name })),
    minPlayers: 5,
    maxPlayers: 20,
    mafiaCount: 1,
  });
  emit(app, "hostChanged", { playerId: "p1" });

  // Back in the lobby: same five players, no revealed roles, own role cleared.
  assert.equal(headlineText(app), "Lobby");
  assert.equal(myRoleText(app), "—", "own role display is cleared");
  assert.equal(rows(app).length, 5, "all five players are retained");
  assert.ok(
    !badgeTexts(rowFor(app, "Cara")).includes("Detective"),
    "revealed-role badges are gone after the reset",
  );

  // The lobby is ready and the host's Start control is back.
  assert.match(hintText(app), /Ready to start/);
  assert.ok(
    extrasButtons(app).includes("Start game"),
    "the host's Start game control returns in the fresh lobby",
  );
});

test("a new player can appear in the lobby after a reset", () => {
  const app = newApp();
  endGameAs(app, "p1");
  emit(app, "gameReset", {
    players: FIVE.map((p) => ({ playerId: p.id, name: p.name })),
    minPlayers: 5,
    maxPlayers: 20,
    mafiaCount: 1,
  });
  emit(app, "hostChanged", { playerId: "p1" });

  // A late joiner's playerJoined now folds into the reopened lobby.
  emit(app, "playerJoined", { playerId: "p6", name: "Finn" });

  assert.equal(rows(app).length, 6, "the new player joins the reset lobby");
  assert.ok(rowFor(app, "Finn"), "the new player's row is rendered");
});
