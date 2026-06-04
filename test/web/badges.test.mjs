import { test } from "node:test";
import assert from "node:assert/strict";
import { newApp, emit, startGameAs, rowFor, badgeTexts } from "./harness.mjs";

const SIX = [
  { id: "p1", name: "Yak" },
  { id: "p2", name: "Boss" },
  { id: "p3", name: "Cara" },
  { id: "p4", name: "Dee" },
  { id: "p5", name: "Eve" },
  { id: "p6", name: "Finn" },
];

test("a mafia-faction viewer badges the Yakuza distinctly from plain mafia", () => {
  const app = newApp();
  // Yakuza (p1) sees the roster: p1 is the Yakuza, p2 a plain mafioso.
  startGameAs(app, { me: "p1", myRole: "yakuza", players: SIX, mafiaRoster: ["p1", "p2"], yakuza: "p1" });

  assert.deepEqual(badgeTexts(rowFor(app, "Yak")).sort(), ["Yakuza", "You"].sort());
  assert.ok(badgeTexts(rowFor(app, "Boss")).includes("Mafia"));
  assert.ok(!badgeTexts(rowFor(app, "Boss")).includes("Yakuza"));
  // A town player carries no faction badge for the mafia viewer.
  assert.deepEqual(badgeTexts(rowFor(app, "Cara")), []);
});

test("a town viewer never sees any faction badge (no roster delivered)", () => {
  const app = newApp();
  // A detective (p3) is never sent the mafiaRoster, so mafiaPeers stays empty.
  startGameAs(app, { me: "p3", myRole: "detective", players: SIX });

  for (const name of ["Yak", "Boss", "Cara", "Dee"]) {
    const badges = badgeTexts(rowFor(app, name));
    assert.ok(!badges.includes("Mafia"), `${name} must not be badged Mafia to town`);
    assert.ok(!badges.includes("Yakuza"), `${name} must not be badged Yakuza to town`);
  }
  // The detective sees only its own self badges.
  assert.deepEqual(badgeTexts(rowFor(app, "Cara")).sort(), ["Detective", "You"].sort());
});

test("the faction kill locks a 'Target' badge on every mafia member's roster", () => {
  const app = newApp();
  startGameAs(app, { me: "p1", myRole: "yakuza", players: SIX, mafiaRoster: ["p1", "p2"], yakuza: "p1" });
  emit(app, "phaseChanged", { from: "lobby", to: "night", day: 0 });
  emit(app, "nightActionStarted", { role: "mafia", deadline: 0 });

  // A teammate locks the kill on Dee — the faction ack reaches us too.
  emit(app, "nightActionRecorded", { actor: "p2", target: "p4", faction: "mafia" });

  assert.ok(badgeTexts(rowFor(app, "Dee")).includes("Target"));
});

test("a Yakuza recruit shows a 'Recruit' badge to the faction and clears the kill target", () => {
  const app = newApp();
  startGameAs(app, { me: "p2", myRole: "mafia", players: SIX, mafiaRoster: ["p1", "p2"], yakuza: "p1" });
  emit(app, "phaseChanged", { from: "lobby", to: "night", day: 0 });
  emit(app, "nightActionStarted", { role: "mafia", deadline: 0 });

  // First a kill is tentatively locked, then the Yakuza recruits instead.
  emit(app, "nightActionRecorded", { actor: "p1", target: "p4", faction: "mafia" });
  assert.ok(badgeTexts(rowFor(app, "Dee")).includes("Target"));

  emit(app, "recruitRecorded", { yakuza: "p1", target: "p5" });
  // The kill target is cleared and the recruit target is badged instead.
  assert.ok(!badgeTexts(rowFor(app, "Dee")).includes("Target"), "kill target cleared on recruit");
  assert.ok(badgeTexts(rowFor(app, "Eve")).includes("Recruit"));
});

test("the graveyard sees revealed roles; the recruit's flip shows after the roster refresh", () => {
  const app = newApp();
  // A dead villager (p4) spectates: rosterRevealed hands the dead the full map.
  startGameAs(app, { me: "p4", myRole: "villager", players: SIX });
  emit(app, "playerKilled", { playerId: "p4" }); // we die -> spectator
  emit(app, "rosterRevealed", {
    roles: { p1: "yakuza", p2: "mafia", p3: "detective", p4: "villager", p5: "mafia", p6: "villager" },
  });

  // p5 was recruited (now mafia) — the dead see the revealed role.
  assert.ok(badgeTexts(rowFor(app, "Eve")).includes("Mafia"));
  assert.ok(badgeTexts(rowFor(app, "Yak")).includes("Yakuza"));
});
