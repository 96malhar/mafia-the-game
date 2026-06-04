import { test } from "node:test";
import assert from "node:assert/strict";
import { newApp, joinAs, emit, startGameAs, toNightRoleAct } from "./harness.mjs";

const guide = (app) => app.$("role-guide");
const visible = (app) => !guide(app).classList.contains("hidden");

const SIX = [
  { id: "p1", name: "Alice" }, { id: "p2", name: "Bob" }, { id: "p3", name: "Cara" },
  { id: "p4", name: "Dee" }, { id: "p5", name: "Eve" }, { id: "p6", name: "Finn" },
];

function lobbyApp() {
  const app = newApp();
  joinAs(app, {
    playerId: "p1", name: "Alice", isHost: true,
    events: [
      { type: "gameCreated", data: { minPlayers: 6, maxPlayers: 20, mafiaCount: 1 } },
      { type: "playerJoined", data: { playerId: "p1", name: "Alice" } },
    ],
  });
  return app;
}

test("the role guide is visible and collapsed in the lobby", () => {
  const app = lobbyApp();
  assert.ok(visible(app), "visible in the lobby");
  assert.equal(guide(app).open, false, "collapsed by default (no open attribute)");
});

test("the role guide lists all seven roles, grouped by faction", () => {
  const text = guide(lobbyApp()).textContent;
  for (const role of ["Villager", "Detective", "Doctor", "Vigilante", "Mafia", "Consort", "Yakuza"]) {
    assert.match(text, new RegExp(role), `lists ${role}`);
  }
  assert.match(text, /Town/, "has a Town faction heading");
  assert.match(text, /Mafia/, "has a Mafia faction heading");
});

test("the guide surfaces the key role gotchas", () => {
  const text = guide(lobbyApp()).textContent;
  assert.match(text, /not mafia/i, "the Consort/Yakuza-reads-clean gotcha");
  assert.match(text, /silent/i, "the doctor's silent save");
  assert.match(text, /hold fire/i, "the vigilante can hold fire");
  assert.match(text, /spends the bullet/i, "a saved vigilante shot still spends the bullet");
  assert.match(text, /forgoing the kill/i, "recruit forgoes the kill");
  assert.match(text, /can't be prevented/i, "the Yakuza sacrifice can't be stopped by the doctor");
});

test("it stays visible in the roles-dealt window (after Start game, before Begin night)", () => {
  const app = newApp();
  // startGameAs deals roles (roleAssigned) but, like the engine, keeps the
  // phase at "lobby" — no phaseChanged. Only BeginNight flips it to night.
  startGameAs(app, { me: "p1", myRole: "mafia", players: SIX, mafiaRoster: ["p1"], yakuza: "" });
  assert.ok(visible(app), "still visible while roles are dealt, before the night begins");
});

test("it hides once the night begins and returns by day / vote / end", () => {
  const app = newApp();
  startGameAs(app, { me: "p1", myRole: "mafia", players: SIX, mafiaRoster: ["p1"], yakuza: "" });

  toNightRoleAct(app, "mafia"); // BeginNight -> phase "night"
  assert.ok(!visible(app), "hidden during the night");

  emit(app, "phaseChanged", { from: "night", to: "day_discussion", day: 1 });
  assert.ok(visible(app), "back at daybreak");
  emit(app, "phaseChanged", { from: "day_discussion", to: "day_vote", day: 1 });
  assert.ok(visible(app), "visible during voting");
  emit(app, "phaseChanged", { from: "day_vote", to: "ended", day: 1 });
  assert.ok(visible(app), "visible after the game ends");
});

test("the guide content is identical for town and mafia viewers (static, not faction-aware)", () => {
  const town = newApp();
  startGameAs(town, { me: "p3", myRole: "detective", players: SIX });
  const mafia = newApp();
  startGameAs(mafia, { me: "p1", myRole: "mafia", players: SIX, mafiaRoster: ["p1"], yakuza: "" });

  const norm = (app) => guide(app).textContent.replace(/\s+/g, " ").trim();
  assert.equal(norm(town), norm(mafia), "same guide regardless of the viewer's role");
});
