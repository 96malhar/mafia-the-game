import { test } from "node:test";
import assert from "node:assert/strict";
import { newApp, rows } from "./harness.mjs";

// These tests cover the client side of cursor-based resume: the client tracks
// a sequence cursor that advances with each streamed event and is adopted from
// the join/rejoin high-water mark; on reconnect it rides the URL as ?since=N;
// a delta rejoin (fromSeq>0) is applied ON TOP of the existing model while a
// full rejoin (fromSeq===0) rebuilds it from scratch.

// joined drives a "joined" frame with an explicit high-water cursor.
function joined(app, { playerId, isHost = false, lastSeq, events }) {
  app.window.handleServerMessage({
    type: "joined",
    data: { playerId, name: playerId, secret: "secret", roomCode: "ABCD", isHost, lastSeq, events },
  });
}

// serverEvent drives a streamed "event" frame carrying its absolute seq
// (the full path that advances the client cursor — unlike harness.emit, which
// calls handleEvent directly and bypasses the seq tracking).
function serverEvent(app, type, data, seq) {
  app.window.handleServerMessage({ type: "event", data: { event: { type, data }, seq } });
}

// rejoined drives a "rejoined" frame. fromSeq>0 is a delta; 0 is a full snapshot.
function rejoined(app, { playerId, isHost = false, fromSeq, lastSeq, events }) {
  app.window.handleServerMessage({
    type: "rejoined",
    data: { playerId, name: playerId, roomCode: "ABCD", isHost, fromSeq, lastSeq, events },
  });
}

const gameCreated = { type: "gameCreated", data: { minPlayers: 5, maxPlayers: 20, mafiaCount: 1 } };
const playerJoined = (id, name) => ({ type: "playerJoined", data: { playerId: id, name } });

// rosterNames returns the rendered player names, sorted, for set comparison.
function rosterNames(app) {
  return rows(app)
    .map((li) => li.querySelector(".truncate")?.textContent)
    .filter(Boolean)
    .sort();
}

// reconnectURL swaps in a recording WebSocket stub, opens a reconnect socket
// via the app's real connect(), and returns the URL it dialed. (The app's `ws`
// is a top-level `let`, so it isn't reachable as a window property — we capture
// the URL at construction instead.)
function reconnectURL(app, code, creds) {
  let url;
  app.window.WebSocket = class {
    constructor(u) { url = u; this.readyState = 0; }
    send() {}
    close() {}
  };
  app.window.WebSocket.OPEN = 1;
  app.window.connect(code, null, creds);
  return url;
}

test("the resume cursor advances with each event's seq and rides the reconnect URL", () => {
  const app = newApp();
  joined(app, {
    playerId: "p1",
    isHost: true,
    lastSeq: 2,
    events: [gameCreated, playerJoined("p1", "Alice")],
  });

  // Two streamed events advance the cursor to their seq.
  serverEvent(app, "playerJoined", { playerId: "p2", name: "Bob" }, 3);
  serverEvent(app, "playerJoined", { playerId: "p3", name: "Cara" }, 4);

  // Opening a reconnect socket must carry the latest cursor as ?since=.
  const url = reconnectURL(app, "ABCD", { playerId: "p1", secret: "secret" });
  assert.match(url, /[?&]since=4(?:&|$)/, "reconnect URL carries the latest cursor");
  assert.match(url, /[?&]playerId=p1(?:&|$)/, "reconnect URL keeps identity");
});

test("a page-load auto-rejoin (no prior cursor) requests a full snapshot via since=0", () => {
  const app = newApp();
  // No join happened in this fresh realm, so lastSeq is still 0.
  const url = reconnectURL(app, "ABCD", { playerId: "p1", secret: "secret" });
  assert.match(url, /[?&]since=0(?:&|$)/, "a fresh client resumes from 0 (full snapshot)");
});

test("a delta rejoin keeps the existing model and applies only the tail", () => {
  const app = newApp();
  joined(app, {
    playerId: "p1",
    isHost: true,
    lastSeq: 3,
    events: [gameCreated, playerJoined("p1", "Alice"), playerJoined("p2", "Bob")],
  });
  serverEvent(app, "playerJoined", { playerId: "p3", name: "Cara" }, 4);
  assert.deepEqual(rosterNames(app), ["Alice", "Bob", "Cara"]);

  // Delta rejoin: tail since cursor 4 carries one more joiner; the existing
  // roster must be preserved, not wiped.
  rejoined(app, {
    playerId: "p1",
    isHost: true,
    fromSeq: 4,
    lastSeq: 5,
    events: [playerJoined("p4", "Dee")],
  });
  assert.deepEqual(rosterNames(app), ["Alice", "Bob", "Cara", "Dee"],
    "a delta rejoin appends to the existing model");
});

test("a full rejoin (fromSeq 0) rebuilds the model from scratch", () => {
  const app = newApp();
  joined(app, {
    playerId: "p1",
    isHost: true,
    lastSeq: 4,
    events: [gameCreated, playerJoined("p1", "Alice"), playerJoined("p2", "Bob"), playerJoined("p3", "Cara")],
  });
  assert.deepEqual(rosterNames(app), ["Alice", "Bob", "Cara"]);

  // A full snapshot (fromSeq 0) with a smaller roster replaces the model
  // entirely — e.g. after a reset rebaselined the server log.
  rejoined(app, {
    playerId: "p1",
    isHost: true,
    fromSeq: 0,
    lastSeq: 2,
    events: [gameCreated, playerJoined("p1", "Alice")],
  });
  assert.deepEqual(rosterNames(app), ["Alice"], "a full rejoin rebuilds from only the snapshot");
});

test("a rejoin mid-act-window restarts the actor's countdown instead of freezing it", () => {
  // Regression: refreshing the tab during your turn to act left the countdown
  // bar frozen full — the per-sub-phase handlers skip startNightCountdown
  // while replaying, and nothing restarted it once the replay settled.
  const app = newApp();
  const deadline = Date.now() + 30000;
  const events = [
    { type: "gameCreated", data: { minPlayers: 5, maxPlayers: 20, mafiaCount: 1 } },
    playerJoined("p1", "Alice"),
    playerJoined("p2", "Bob"),
    playerJoined("p3", "Hannah"),
    { type: "roleAssigned", data: { playerId: "p3", role: "doctor" } },
    { type: "phaseChanged", data: { from: "lobby", to: "night", day: 0 } },
    { type: "nightActionStarted", data: { role: "doctor", deadline } },
  ];
  // Rejoin as the doctor (p3) mid-act — a full snapshot, as a tab refresh does.
  rejoined(app, { playerId: "p3", isHost: false, fromSeq: 0, lastSeq: events.length, events });

  // startNightCountdown renders one frame synchronously, so the banner shows
  // the remaining seconds and the bar has a real width right away.
  assert.match(app.$("night-banner-countdown").textContent, /^\d+s$/,
    "the actor's countdown shows remaining seconds after a mid-act rejoin");
  assert.match(app.$("night-banner-bar").style.width, /^\d/,
    "the countdown bar has a non-empty width");

  // Stop the interval so it doesn't leak into other tests.
  app.window.stopNightCountdown();
});

test("live-applied events and a delta rejoin converge to the same roster", () => {
  // App A receives every event live.
  const a = newApp();
  joined(a, { playerId: "p1", isHost: true, lastSeq: 2, events: [gameCreated, playerJoined("p1", "Alice")] });
  serverEvent(a, "playerJoined", { playerId: "p2", name: "Bob" }, 3);
  serverEvent(a, "playerJoined", { playerId: "p3", name: "Cara" }, 4);

  // App B drops after the join and catches up via one delta rejoin.
  const b = newApp();
  joined(b, { playerId: "p1", isHost: true, lastSeq: 2, events: [gameCreated, playerJoined("p1", "Alice")] });
  rejoined(b, {
    playerId: "p1",
    isHost: true,
    fromSeq: 2,
    lastSeq: 4,
    events: [playerJoined("p2", "Bob"), playerJoined("p3", "Cara")],
  });

  assert.deepEqual(rosterNames(a), rosterNames(b),
    "the delta-resumed client matches the live one");
});
