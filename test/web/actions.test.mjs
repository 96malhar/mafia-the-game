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

test("the Yakuza gets both Kill and Recruit on every town row, nothing on teammates or self", () => {
  const app = newApp();
  startGameAs(app, { me: "p1", myRole: "yakuza", players: SIX, mafiaRoster: ["p1", "p2"], yakuza: "p1" });
  toNightRoleAct(app, "mafia"); // the Yakuza acts within the Mafia turn

  // Both choices are always present during the act window — no mode toggle.
  assert.deepEqual(buttonTexts(rowFor(app, "Cara")), ["Kill", "Recruit"], "town row: Kill + Recruit");
  assert.deepEqual(buttonTexts(rowFor(app, "Boss")), [], "fellow mafioso: no buttons");
  assert.deepEqual(buttonTexts(rowFor(app, "Yak")), [], "self: no buttons");
});

test("the Yakuza owns the night countdown bar during the Mafia act window", () => {
  const app = newApp();
  startGameAs(app, { me: "p1", myRole: "yakuza", players: SIX, mafiaRoster: ["p1", "p2"], yakuza: "p1" });
  toNightRoleAct(app, "mafia"); // the Yakuza acts within the Mafia turn

  // The Yakuza has no turn of its own (currentNightRole === "mafia" while
  // myRole === "yakuza"), so a bare role-equality check used to hide its
  // timer; it should own the countdown like any other actor in its act window.
  assert.ok(
    !app.$("night-banner-bar-wrap").classList.contains("hidden"),
    "Yakuza sees the countdown bar",
  );
  assert.ok(
    !app.$("night-banner-countdown").classList.contains("hidden"),
    "Yakuza sees the countdown number",
  );
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

test("the tracker gets a 'Track' button on other rows, none on its own", () => {
  const app = newApp();
  startGameAs(app, { me: "p3", myRole: "tracker", players: SIX });
  toNightRoleAct(app, "tracker");

  assert.deepEqual(buttonTexts(rowFor(app, "Dee")), ["Track"], "other rows: Track");
  assert.deepEqual(buttonTexts(rowFor(app, "Cara")), [], "no track button on your own row");
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

test("day vote: everyone sees the running vote count, then 'Voting completed'", () => {
  const app = newApp();
  startGameAs(app, { me: "p3", myRole: "villager", players: SIX }); // 6 alive
  emit(app, "phaseChanged", { from: "lobby", to: "day_discussion", day: 1 });
  emit(app, "phaseChanged", { from: "day_discussion", to: "day_vote", day: 1 });

  const extras = () => app.$("action-extras").textContent;

  // Before any vote: 0 of 6 (the denominator comes from the local roster).
  assert.match(extras(), /0 of 6 voted/);

  // The server's public count drives the tally — individual votes stay hidden.
  emit(app, "voteProgress", { day: 1, cast: 3 });
  assert.match(extras(), /3 of 6 voted/);

  // All six living players in → the completion message replaces the count.
  emit(app, "voteProgress", { day: 1, cast: 6 });
  assert.match(extras(), /Voting completed/);
  assert.doesNotMatch(extras(), /of 6 voted/, "the count is gone once complete");
});

test("day vote: a player can abstain, and the UI reflects their own abstention", () => {
  const app = newApp();
  startGameAs(app, { me: "p3", myRole: "villager", players: SIX });
  emit(app, "phaseChanged", { from: "lobby", to: "day_discussion", day: 1 });
  emit(app, "phaseChanged", { from: "day_discussion", to: "day_vote", day: 1 });

  const abstainBtn = () =>
    [...app.$("action-extras").querySelectorAll("button")].find((b) =>
      /abstain/i.test(b.textContent),
    );

  // An Abstain button is offered alongside the per-row Vote buttons.
  assert.equal(abstainBtn().textContent, "Abstain", "an Abstain button is offered");

  // Our own (private) abstention flips the button to the undo state and
  // shows a chip. Other players' abstentions never arrive as this event.
  emit(app, "voteAbstained", { voter: "p3" });
  assert.match(abstainBtn().textContent, /Abstaining/i, "button reflects our abstention");
  assert.match(app.$("action-extras").textContent, /You abstained/i);

  // Retracting (the undo path) returns us to the plain Abstain button.
  emit(app, "voteRetracted", { voter: "p3", was: "" });
  assert.equal(abstainBtn().textContent, "Abstain", "undo returns to Abstain");
});

test("day vote: casting a real vote clears our abstention", () => {
  const app = newApp();
  startGameAs(app, { me: "p3", myRole: "villager", players: SIX });
  emit(app, "phaseChanged", { from: "lobby", to: "day_discussion", day: 1 });
  emit(app, "phaseChanged", { from: "day_discussion", to: "day_vote", day: 1 });

  emit(app, "voteAbstained", { voter: "p3" });
  assert.match(app.$("action-extras").textContent, /You abstained/i);

  // Voting a player supersedes the abstention (engine clears it; the client
  // mirrors that on our own voteCast).
  emit(app, "voteCast", { voter: "p3", target: "p1" });
  const extras = app.$("action-extras").textContent;
  assert.doesNotMatch(extras, /You abstained/i, "abstention chip is gone");
  assert.match(extras, /Your vote: Yak/, "the real vote shows instead");
});

test("day vote: the host's Reveal votes is blocked until all living players vote", () => {
  const app = newApp();
  startGameAs(app, { me: "p1", myRole: "villager", players: SIX }); // p1 is host, 6 alive
  emit(app, "phaseChanged", { from: "lobby", to: "day_discussion", day: 1 });
  emit(app, "phaseChanged", { from: "day_discussion", to: "day_vote", day: 1 });

  const revealBtn = () =>
    [...app.$("action-extras").querySelectorAll("button")].find((b) =>
      /reveal votes/i.test(b.textContent),
    );

  // A partial tally: the button is present (so the host sees the action) but
  // disabled and labeled with why.
  emit(app, "voteProgress", { day: 1, cast: 5 });
  let btn = revealBtn();
  assert.ok(btn, "the host sees a Reveal votes button");
  assert.equal(btn.disabled, true, "disabled until everyone has voted");
  assert.match(btn.textContent, /waiting on all votes/i);

  // All six living players in → the button enables.
  emit(app, "voteProgress", { day: 1, cast: 6 });
  btn = revealBtn();
  assert.equal(btn.disabled, false, "enabled once all have voted");
  assert.equal(btn.textContent, "Reveal votes");
});

test("day vote: the progress count resets when the host clears the board", () => {
  const app = newApp();
  startGameAs(app, { me: "p3", myRole: "villager", players: SIX });
  emit(app, "phaseChanged", { from: "lobby", to: "day_discussion", day: 1 });
  emit(app, "phaseChanged", { from: "day_discussion", to: "day_vote", day: 1 });

  emit(app, "voteProgress", { day: 1, cast: 4 });
  assert.match(app.$("action-extras").textContent, /4 of 6 voted/);

  // A clear (re-vote) wipes the tally back to a hidden, empty slate.
  emit(app, "voteCleared", { day: 1 });
  assert.match(app.$("action-extras").textContent, /0 of 6 voted/);
});
