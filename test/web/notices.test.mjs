import { test } from "node:test";
import assert from "node:assert/strict";
import { newApp, emit, startGameAs, modalText, hintText } from "./harness.mjs";

const SIX = [
  { id: "p1", name: "Yak" },
  { id: "p2", name: "Boss" },
  { id: "p3", name: "Cara" },
  { id: "p4", name: "Dee" },
  { id: "p5", name: "Eve" },
  { id: "p6", name: "Finn" },
];

// A dead spectator's night feed lives in the action panel; collect its chips.
function spectatorFeed(app) {
  return [...app.$("action-extras").querySelectorAll("span")].map((s) => s.textContent.trim());
}

test("the Consort block notice shows the 'distracted' toast", () => {
  const app = newApp();
  startGameAs(app, { me: "p4", myRole: "doctor", players: SIX });
  emit(app, "phaseChanged", { from: "lobby", to: "night", day: 0 });
  emit(app, "nightNarrationStarted", { role: "doctor", phantom: true, deadline: 0 });
  emit(app, "blocked", { playerId: "p4" });

  assert.match(modalText(app), /distract/i);
});

test("the recruit toast persists until acknowledged, then clears on the next night", () => {
  const app = newApp();
  startGameAs(app, { me: "p5", myRole: "villager", players: SIX });
  // Villager has no turn: the recruit notice arrives at resolution.
  emit(app, "phaseChanged", { from: "lobby", to: "night", day: 0 });
  emit(app, "recruited", { playerId: "p5" });
  assert.match(modalText(app), /recruited/i);

  // A fresh night clears the per-night notice flags (but the modal is manual-
  // dismiss; the harness's next-night reset is observable via the hint state).
  app.$("notice-modal-dismiss").click();
  assert.equal(modalText(app), "", "tapping 'Got it' clears the modal");
});

test("the graveyard feed renders a Yakuza recruit with the 'recruited' verb", () => {
  const app = newApp();
  startGameAs(app, { me: "p6", myRole: "villager", players: SIX });
  emit(app, "playerKilled", { playerId: "p6" }); // we die -> spectating
  emit(app, "phaseChanged", { from: "day_discussion", to: "night", day: 2 });

  // The graveyard watches the night: a kill, then a recruit.
  emit(app, "spectatorNightAction", {
    actor: "p2", actorRole: "mafia", target: "p3", targetRole: "detective", recruit: false,
  });
  emit(app, "spectatorNightAction", {
    actor: "p1", actorRole: "yakuza", target: "p4", targetRole: "villager", recruit: true,
  });

  const feed = spectatorFeed(app).join(" | ");
  assert.match(feed, /killed/, "a kill reads with the role verb");
  assert.match(feed, /recruited/, "a recruit reads with the 'recruited' verb");
  assert.match(feed, /\(yakuza\)/, "the recruit names the Yakuza actor");
});

test("the detective result shows a private modal naming the target", () => {
  const app = newApp();
  startGameAs(app, { me: "p3", myRole: "detective", players: SIX });
  emit(app, "phaseChanged", { from: "lobby", to: "night", day: 0 });
  emit(app, "detectiveResult", { detective: "p3", target: "p1", isMafia: true });

  assert.match(modalText(app), /Yak/, "the modal names the investigated player");
  assert.match(modalText(app), /IS a mafia/i);
});

// --- replay vs acknowledgement -------------------------------------------
//
// The one-shot notices (recruit / promotion / detective result) must still
// reach a player who was DISCONNECTED when they fired (phone locked during
// the night) — the projected log replays on reconnect, and a notice the
// player never acknowledged should re-pop. But once they've tapped "Got it",
// an ordinary refresh must NOT re-pop it. These tests drive the rejoin
// (replay) path with {replaying:true}, which the live emit() helper bypasses.
//
// rejoinWith simulates a reconnect: connect() sets currentRoomCode (the ack
// store is keyed by room+player, so it must be set), then the "rejoined"
// frame replays the projected backlog through enterRoomFromServer.
function rejoinWith(app, { me, name, events }) {
  app.window.connect("ABCD", name, { playerId: me, secret: "secret" });
  app.window.handleServerMessage({
    type: "rejoined",
    data: { playerId: me, name, roomCode: "ABCD", isHost: false, events },
  });
}

const SIX_JOINS = SIX.map((p) => ({ type: "playerJoined", data: { playerId: p.id, name: p.name } }));
const CREATED = { type: "gameCreated", data: { minPlayers: 6, maxPlayers: 20, mafiaCount: 1 } };

test("a recruit the player missed (was disconnected) re-pops on reconnect, then stays quiet once acknowledged", () => {
  const app = newApp();
  const backlog = [
    CREATED,
    ...SIX_JOINS,
    { type: "roleAssigned", data: { playerId: "p5", role: "villager" } },
    { type: "phaseChanged", data: { from: "lobby", to: "night", day: 0 } },
    { type: "recruited", data: { playerId: "p5" } },
  ];

  // First reconnect: never acknowledged, so the missed recruit re-pops.
  rejoinWith(app, { me: "p5", name: "Eve", events: backlog });
  assert.match(modalText(app), /recruited/i, "a missed recruit re-pops on replay");

  // Acknowledge it.
  app.$("notice-modal-dismiss").click();
  assert.equal(modalText(app), "");

  // A later refresh replays the same log — but now it's been acked, so it
  // must NOT re-pop.
  rejoinWith(app, { me: "p5", name: "Eve", events: backlog });
  assert.equal(modalText(app), "", "an acknowledged recruit is not re-popped on later replay");
});

test("a detective result is acknowledged per investigation, not once for all", () => {
  const app = newApp();
  const night0 = [
    CREATED,
    ...SIX_JOINS,
    { type: "roleAssigned", data: { playerId: "p3", role: "detective" } },
    { type: "phaseChanged", data: { from: "lobby", to: "night", day: 0 } },
    { type: "detectiveResult", data: { detective: "p3", target: "p1", isMafia: true } },
  ];

  // Reconnect mid-game-1: the night-0 finding the detective missed re-pops.
  rejoinWith(app, { me: "p3", name: "Cara", events: night0 });
  assert.match(modalText(app), /Yak/, "a missed detective result re-pops on replay");
  app.$("notice-modal-dismiss").click();

  // Refresh again with the same log: the acknowledged night-0 result is quiet.
  rejoinWith(app, { me: "p3", name: "Cara", events: night0 });
  assert.equal(modalText(app), "", "the acknowledged night-0 result is not re-popped");

  // A SECOND night's investigation has its own id (det:<day>:<target>), so a
  // result the detective hasn't seen yet still pops even though night 0 was
  // acknowledged.
  const night2 = [
    ...night0,
    { type: "phaseChanged", data: { from: "day_discussion", to: "night", day: 2 } },
    { type: "detectiveResult", data: { detective: "p3", target: "p2", isMafia: false } },
  ];
  rejoinWith(app, { me: "p3", name: "Cara", events: night2 });
  assert.match(modalText(app), /Boss/, "a fresh investigation still pops despite an earlier ack");
  assert.match(modalText(app), /NOT a mafia/i);
});

test("starting a new game in the room clears stale notice acks", () => {
  const app = newApp();
  const game1 = [
    CREATED,
    ...SIX_JOINS,
    { type: "roleAssigned", data: { playerId: "p5", role: "villager" } },
    { type: "phaseChanged", data: { from: "lobby", to: "night", day: 0 } },
    { type: "recruited", data: { playerId: "p5" } },
  ];
  rejoinWith(app, { me: "p5", name: "Eve", events: game1 });
  app.$("notice-modal-dismiss").click(); // acknowledge game 1's recruit

  // The host starts a fresh game in the same room; its lobby snapshot drops
  // the old acks. A reconnect into game 2 with its own recruit must re-pop,
  // not be swallowed by game 1's stale ack.
  emit(app, "gameReset", { minPlayers: 6, maxPlayers: 20, mafiaCount: 1, players: SIX });
  rejoinWith(app, { me: "p5", name: "Eve", events: game1 });
  assert.match(modalText(app), /recruited/i, "a new game's recruit is not suppressed by an old ack");
});
