import { test } from "node:test";
import assert from "node:assert/strict";
import {
  newApp, emit, startGameAs, toNightRoleAct,
  rowFor, buttonTexts, hintText, modalText,
} from "./harness.mjs";

const SIX = [
  { id: "p1", name: "Yak" },
  { id: "p2", name: "Boss" },
  { id: "p3", name: "Cara" },
  { id: "p4", name: "Dee" },
  { id: "p5", name: "Eve" },
  { id: "p6", name: "Finn" },
];

test("the Yakuza gets Kill AND Recruit on town rows, nothing on teammates or self", () => {
  const app = newApp();
  startGameAs(app, { me: "p1", myRole: "yakuza", players: SIX, mafiaRoster: ["p1", "p2"], yakuza: "p1" });
  toNightRoleAct(app, "mafia"); // the Yakuza acts within the Mafia turn

  assert.deepEqual(buttonTexts(rowFor(app, "Cara")), ["Kill", "Recruit"], "town row: both buttons");
  assert.deepEqual(buttonTexts(rowFor(app, "Boss")), [], "fellow mafioso: no buttons");
  assert.deepEqual(buttonTexts(rowFor(app, "Yak")), [], "self: no buttons");
});

test("a plain mafioso gets only Kill (no Recruit) on the Mafia turn", () => {
  const app = newApp();
  startGameAs(app, { me: "p2", myRole: "mafia", players: SIX, mafiaRoster: ["p1", "p2"], yakuza: "p1" });
  toNightRoleAct(app, "mafia");

  assert.deepEqual(buttonTexts(rowFor(app, "Cara")), ["Kill"]);
  assert.deepEqual(buttonTexts(rowFor(app, "Yak")), [], "the Yakuza is a teammate — no target button");
});

test("the doctor gets a 'Save self' button on its own row", () => {
  const app = newApp();
  startGameAs(app, { me: "p4", myRole: "doctor", players: SIX });
  toNightRoleAct(app, "doctor");

  assert.deepEqual(buttonTexts(rowFor(app, "Dee")), ["Save self"]);
  assert.deepEqual(buttonTexts(rowFor(app, "Cara")), ["Save"], "other rows: plain Save");
});

test("a recruited player's hint and toast announce the recruit; no picker", () => {
  const app = newApp();
  startGameAs(app, { me: "p4", myRole: "doctor", players: SIX });
  emit(app, "phaseChanged", { from: "lobby", to: "night", day: 0 });
  // The recruited doctor's turn is phantom; the private notice lands at it.
  emit(app, "nightNarrationStarted", { role: "doctor", phantom: true, deadline: 0 });
  emit(app, "recruited", { playerId: "p4" });

  assert.match(hintText(app), /recruited/i, "hint announces the recruit");
  assert.match(modalText(app), /recruited/i, "a toast announces the recruit");
  // The picker never opens for a (phantom, recruited) turn.
  assert.deepEqual(buttonTexts(rowFor(app, "Cara")), []);
});

test("day vote: a Vote button on every other living row, none on self", () => {
  const app = newApp();
  startGameAs(app, { me: "p3", myRole: "villager", players: SIX });
  emit(app, "phaseChanged", { from: "lobby", to: "day_discussion", day: 1 });
  emit(app, "phaseChanged", { from: "day_discussion", to: "day_vote", day: 1 });

  assert.deepEqual(buttonTexts(rowFor(app, "Yak")), ["Vote"]);
  assert.deepEqual(buttonTexts(rowFor(app, "Cara")), [], "no vote button on your own row");
});
