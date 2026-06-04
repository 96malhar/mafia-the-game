// Frontend test harness: loads the REAL web/*.js into a jsdom realm and lets
// tests drive the actual server-message / event handlers, asserting on the
// resulting DOM. This tests the shipped code as-is (no build step, classic
// scripts sharing one global scope) — we do not refactor the app to be
// module-friendly just for tests.
//
// Browser APIs the app touches are stubbed: WebSocket (the app never opens one
// here — we feed events directly), fetch (the /healthz probe), and
// navigator.clipboard. speechSynthesis is deliberately left UNDEFINED, which
// makes speak()/narrate() guarded no-ops (see web/helpers.js), so tests run
// silently and deterministically.

import { JSDOM, VirtualConsole } from "jsdom";
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { dirname, join } from "node:path";

const here = dirname(fileURLToPath(import.meta.url));
const webDir = join(here, "..", "..", "web");
const read = (f) => readFileSync(join(webDir, f), "utf8");

// Load order mirrors index.html exactly.
const SCRIPTS = [
  "helpers.js",
  "render.js",
  "actions.js",
  "events.js",
  "lobby.js",
  "url.js",
  "main.js",
];

// newApp boots a fresh app instance: a jsdom document with the real index.html
// markup and all seven scripts executed in order. Returns handles for driving
// it. hostname defaults to localhost so credStore picks sessionStorage (jsdom
// provides both storages).
export function newApp({ hostname = "localhost" } = {}) {
  // Strip the <script src=...> tags: the local ones we inject manually (so we
  // can install stubs first), and the Tailwind CDN one we skip entirely (no
  // network in tests; we assert on structure/text, not styling).
  const html = read("index.html").replace(
    /<script\s+src=("|')[^"']*\1\s*><\/script>/g,
    "",
  );

  const virtualConsole = new VirtualConsole();
  // Surface real script errors as test failures; ignore benign resource-load
  // noise (there should be none now that src tags are stripped).
  const errors = [];
  virtualConsole.on("jsdomError", (e) => errors.push(e));

  const dom = new JSDOM(html, {
    url: `http://${hostname}/`,
    runScripts: "dangerously",
    pretendToBeVisual: true,
    virtualConsole,
  });
  const { window } = dom;

  // --- stubs, installed BEFORE any app script runs ---
  window.WebSocket = class FakeWebSocket {
    constructor(url) {
      this.url = url;
      this.readyState = 0; // CONNECTING — the app never drives a live socket here
      this.sent = [];
    }
    send(data) {
      this.sent.push(data);
    }
    close() {
      this.readyState = 3;
    }
  };
  window.WebSocket.OPEN = 1;
  window.fetch = () => Promise.resolve({ ok: true, status: 200 });
  if (!window.navigator.clipboard) {
    Object.defineProperty(window.navigator, "clipboard", {
      configurable: true,
      value: { writeText: () => Promise.resolve() },
    });
  }

  // Execute the app scripts in order, inline, in the realm.
  for (const f of SCRIPTS) {
    const s = window.document.createElement("script");
    s.textContent = read(f);
    window.document.body.appendChild(s);
  }

  if (errors.length) {
    throw new Error(`app scripts threw on load:\n${errors.join("\n")}`);
  }

  return {
    dom,
    window,
    doc: window.document,
    errors,
    $: (id) => window.document.getElementById(id),
  };
}

// joinAs simulates a successful first-time join. `events` is the projected
// backlog the server bundles into the join ack (replayed via
// enterRoomFromServer); pass prior playerJoined / gameCreated / roleAssigned
// frames here to set up roster + config + your own role.
export function joinAs(app, { playerId, name = playerId, isHost = false, events = [] }) {
  app.window.handleServerMessage({
    type: "joined",
    data: { playerId, name, secret: "secret", roomCode: "ABCD", isHost, events },
  });
}

// emit feeds a single projected engine event (the inner {type,data} envelope)
// through the live handler, exactly as a server "event" frame would.
export function emit(app, type, data = {}) {
  app.window.handleEvent({ type, data });
}

// rows returns the rendered <li> player rows from the Players panel.
export function rows(app) {
  return [...app.$("players").querySelectorAll("li")];
}

// rowFor finds the player row whose name cell matches `name`.
export function rowFor(app, name) {
  return rows(app).find((li) => {
    const nameEl = li.querySelector(".truncate");
    return nameEl && nameEl.textContent === name;
  });
}

// badgeTexts returns the uppercase badge labels on a row (You / Mafia / Yakuza
// / Target / Recruit / a revealed role).
export function badgeTexts(row) {
  return [...row.querySelectorAll("span")]
    .map((s) => s.textContent.trim())
    .filter(Boolean);
}

// buttonTexts returns the action-button labels rendered on a row.
export function buttonTexts(row) {
  return [...row.querySelectorAll("button")].map((b) => b.textContent.trim());
}

// hintText / headlineText read the action panel's helper line and headline.
export const hintText = (app) => app.$("phase-hint").textContent;
export const headlineText = (app) => app.$("phase-headline").textContent;
export const myRoleText = (app) => app.$("my-role").textContent;

// modalText returns the notice-modal body when shown, or "" when hidden.
export function modalText(app) {
  const m = app.$("notice-modal");
  if (!m || m.classList.contains("hidden")) return "";
  return app.$("notice-modal-body").textContent;
}

// startGameAs boots an app, joins as `me`, deals `me` the given role, and
// (optionally) delivers the faction roster — the lobby-time setup every in-game
// test shares. `players` is [{id,name}]; the first id is the host. mafiaRoster
// is the member-id list (with `yakuza` naming the Yakuza member) delivered only
// to a mafia-faction viewer, exactly as the server scopes it.
export function startGameAs(app, { me, myRole, players, mafiaRoster = null, yakuza = "" }) {
  const backlog = [
    { type: "gameCreated", data: { minPlayers: players.length, maxPlayers: 20, mafiaCount: 1 } },
    ...players.map((p) => ({ type: "playerJoined", data: { playerId: p.id, name: p.name } })),
  ];
  const meName = players.find((p) => p.id === me).name;
  joinAs(app, { playerId: me, name: meName, isHost: players[0].id === me, events: backlog });
  emit(app, "roleAssigned", { playerId: me, role: myRole });
  if (mafiaRoster) emit(app, "mafiaRoster", { members: mafiaRoster, yakuza });
}

// toNightRoleAct drives an already-set-up game into PhaseNight with the given
// role's ACT sub-phase open (the window where target buttons render). Defaults
// to the Mafia turn (where the Yakuza also acts).
export function toNightRoleAct(app, role = "mafia", { day = 0 } = {}) {
  emit(app, "phaseChanged", { from: "lobby", to: "night", day });
  emit(app, "nightActionStarted", { role, deadline: 0 });
}
