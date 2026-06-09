import { test } from "node:test";
import assert from "node:assert/strict";
import { newApp, emit, startGameAs } from "./harness.mjs";

// applyPhaseAtmosphere (web/render.js) is the JS half of the "Midnight Noir"
// full-screen mood: it tags <body data-phase> (which styles.css keys its
// per-phase glow/vignette off) and re-tints the browser-chrome theme-color
// so the iOS/Android bars blend into the same field. These tests pin that
// contract — the phase string written to the attribute, and the chrome colour
// per phase — so a renamed phase or a drifted PHASE_CHROME map can't silently
// flatten the atmosphere.
//
// Scope note: jsdom doesn't load the <link>ed styles.css, so we can only assert
// the JS contract (the attribute + the meta colour), not the rendered gradient.
// Keeping PHASE_CHROME in sync with the body[data-phase] rules in styles.css is
// a manual discipline the source comment calls out.

const SIX = [
  { id: "p1", name: "Yak" },
  { id: "p2", name: "Boss" },
  { id: "p3", name: "Cara" },
  { id: "p4", name: "Dee" },
  { id: "p5", name: "Eve" },
  { id: "p6", name: "Finn" },
];

const phaseAttr = (app) => app.window.document.body.dataset.phase;
const chrome = (app) =>
  app.window.document.querySelector('meta[name="theme-color"]').getAttribute("content");

test("the lobby paints the neutral ink before a game starts", () => {
  const app = newApp();
  startGameAs(app, { me: "p1", myRole: "villager", players: SIX });
  assert.equal(phaseAttr(app), "lobby", "body is tagged lobby");
  assert.equal(chrome(app), "#0b1020", "chrome is the neutral lobby ink");
});

test("each phase tags the body and re-tints the browser chrome to match", () => {
  const app = newApp();
  startGameAs(app, { me: "p1", myRole: "villager", players: SIX });

  // The mapping the CSS + OS chrome read. Mirrors PHASE_CHROME in render.js.
  const cases = [
    { to: "night",          attr: "night",          ink: "#060914" },
    { to: "day_discussion", attr: "day_discussion", ink: "#15100a" },
    { to: "day_vote",       attr: "day_vote",       ink: "#140a0e" },
    { to: "ended",          attr: "ended",          ink: "#07140f" },
  ];

  let from = "lobby";
  for (const c of cases) {
    emit(app, "phaseChanged", { from, to: c.to, day: 1 });
    assert.equal(phaseAttr(app), c.attr, `body tagged ${c.attr}`);
    assert.equal(chrome(app), c.ink, `chrome tinted for ${c.attr}`);
    from = c.to;
  }
});

test("a reset returns the atmosphere to the lobby ink", () => {
  const app = newApp();
  startGameAs(app, { me: "p1", myRole: "villager", players: SIX });
  emit(app, "phaseChanged", { from: "lobby", to: "night", day: 1 });
  assert.equal(phaseAttr(app), "night", "moved into the night mood");

  // gameReset replays into a fresh lobby; the atmosphere follows the phase
  // back to neutral.
  emit(app, "gameReset", {});
  assert.equal(phaseAttr(app), "lobby", "back to the lobby tag");
  assert.equal(chrome(app), "#0b1020", "back to the lobby ink");
});
