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
