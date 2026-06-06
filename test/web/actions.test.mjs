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

test("the Yakuza gets a single Kill button by default, nothing on teammates or self", () => {
  const app = newApp();
  startGameAs(app, { me: "p1", myRole: "yakuza", players: SIX, mafiaRoster: ["p1", "p2"], yakuza: "p1" });
  toNightRoleAct(app, "mafia"); // the Yakuza acts within the Mafia turn

  // Default (kill mode): one Kill button per town row, like a plain mafioso.
  assert.deepEqual(buttonTexts(rowFor(app, "Cara")), ["Kill"], "town row: Kill only");
  assert.deepEqual(buttonTexts(rowFor(app, "Boss")), [], "fellow mafioso: no buttons");
  assert.deepEqual(buttonTexts(rowFor(app, "Yak")), [], "self: no buttons");
});

test("the Yakuza's banner Recruit-mode toggle flips every row button between Kill and Recruit", () => {
  const app = newApp();
  startGameAs(app, { me: "p1", myRole: "yakuza", players: SIX, mafiaRoster: ["p1", "p2"], yakuza: "p1" });
  toNightRoleAct(app, "mafia");

  const toggle = () => app.$("night-banner-actions").querySelector("button");

  // Off by default, advertising the OFF state; rows show Kill.
  assert.match(toggle().textContent, /Recruit mode: OFF/i, "toggle starts OFF");
  assert.deepEqual(buttonTexts(rowFor(app, "Cara")), ["Kill"]);

  // Turning it ON flips the row buttons to Recruit and highlights the toggle.
  toggle().click();
  assert.match(toggle().textContent, /Recruit mode: ON/i, "toggle now ON");
  assert.deepEqual(buttonTexts(rowFor(app, "Cara")), ["Recruit"], "town row: Recruit");
  assert.deepEqual(buttonTexts(rowFor(app, "Yak")), [], "self: still no buttons");

  // Toggling back returns to kill mode.
  toggle().click();
  assert.match(toggle().textContent, /Recruit mode: OFF/i, "toggle back OFF");
  assert.deepEqual(buttonTexts(rowFor(app, "Cara")), ["Kill"]);
});

test("a plain mafioso gets only Kill (no Recruit) on the Mafia turn", () => {
  const app = newApp();
  startGameAs(app, { me: "p2", myRole: "mafia", players: SIX, mafiaRoster: ["p1", "p2"], yakuza: "p1" });
  toNightRoleAct(app, "mafia");

  assert.deepEqual(buttonTexts(rowFor(app, "Cara")), ["Kill"]);
  assert.deepEqual(buttonTexts(rowFor(app, "Yak")), [], "the Yakuza is a teammate — no target button");
  // The Recruit-mode toggle is Yakuza-only; a plain mafioso never sees it.
  assert.equal(app.$("night-banner-actions").querySelector("button"), null, "no recruit toggle");
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
