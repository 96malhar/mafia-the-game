      // ----- helpers ------------------------------------------------------

      const $ = (id) => document.getElementById(id);

      // Rejoin credentials are keyed per room: mafia.room.<code>.
      const storageKey = (code) => `mafia.room.${code}`;

      // Where those credentials live depends on the environment, because
      // two goals conflict:
      //   • Local testing spins up several players in separate tabs of ONE
      //     browser. That needs per-tab identity, which only sessionStorage
      //     provides (each tab gets its own copy) — otherwise the tabs would
      //     share a single seat and stomp on each other.
      //   • A real deployment needs credentials to survive a tab CLOSE, so a
      //     player who closes their tab can reopen the room link in a fresh
      //     tab and land back in their seat. Only localStorage persists
      //     across a tab close. In real life one device = one identity per
      //     room, so localStorage's cross-tab sharing is harmless.
      // So: sessionStorage on localhost (preserves the multi-tab test loop),
      // localStorage everywhere else (enables real-world tab-close rejoin).
      const isLocalhost =
        ["localhost", "127.0.0.1", "::1", "0.0.0.0"].includes(location.hostname);
      const credStore = isLocalhost ? sessionStorage : localStorage;

      // One-shot, manually-dismissed notices (the Yakuza recruit, the
      // consort promotion, and each individual detective result) are
      // remembered once the player ACKNOWLEDGES them, persisted per
      // room+player. This lets the replay path re-pop a notice the player
      // genuinely missed — e.g. their phone was locked when it fired during
      // the night — while still suppressing one they've already seen and
      // dismissed on an ordinary refresh. The distinction is purely "did
      // they tap Got it?", not "is this live vs replay", so a reconnecting
      // convert finally learns they were recruited.
      //
      // Keyed mafia.ack.<code>.<playerId> in the SAME store as credentials:
      // sessionStorage on localhost (per-tab, matching the per-tab seats the
      // multi-tab test loop relies on), localStorage in prod (survives a
      // refresh / tab close, like the rejoin creds). The value is a JSON
      // array of opaque notice ids.
      const ackStorageKey = (code, playerId) => `mafia.ack.${code}.${playerId}`;

      // loadAckedNotices reads the current player's acknowledged-notice set.
      // Returns an empty set if we don't yet have an identity, or if the
      // stored value is missing/corrupt — a bad blob must never wedge the
      // notice path, only (at worst) re-show a card.
      function loadAckedNotices() {
        if (!currentRoomCode || !myId) return new Set();
        try {
          const raw = credStore.getItem(ackStorageKey(currentRoomCode, myId));
          return new Set(raw ? JSON.parse(raw) : []);
        } catch {
          return new Set();
        }
      }

      function hasAckedNotice(id) {
        return loadAckedNotices().has(id);
      }

      // markNoticeAcked records that the player dismissed the notice with
      // this id, so no later replay re-pops it. Read-modify-write the whole
      // set each call: these fire at most a handful of times per game, so
      // simplicity beats caching.
      function markNoticeAcked(id) {
        if (!currentRoomCode || !myId) return;
        const acked = loadAckedNotices();
        if (acked.has(id)) return;
        acked.add(id);
        try {
          credStore.setItem(
            ackStorageKey(currentRoomCode, myId),
            JSON.stringify([...acked]),
          );
        } catch {}
      }

      // clearAckedNotices drops the whole set for this room+player. Called
      // when the host starts a NEW game in the same room (gameReset): the
      // next game reuses the same room code and player id, and its notice
      // ids (recruit, promotion, det:<day>:<target>) can collide with the
      // previous game's, so a stale ack would wrongly suppress a fresh
      // notice on replay.
      function clearAckedNotices() {
        if (!currentRoomCode || !myId) return;
        try {
          credStore.removeItem(ackStorageKey(currentRoomCode, myId));
        } catch {}
      }

      const status = $("status");
      const errorPanel = $("errors");

      let ws = null;
      let myId = null;
      let myIsHost = false;
      let myRole = null;

      // mafiaPeers holds the PlayerIDs of every mafioso (including
      // myself), populated from the faction-scoped mafiaRoster event the
      // server sends only to mafia at game start. For a town viewer this
      // stays empty — they never receive the event — so the "Mafia"
      // roster badge can only ever appear on a mafia player's screen.
      // Lets the mafia see their teammates in the UI at all times.
      let mafiaPeers = new Set();

      // yakuzaId is the PlayerID of the faction's Yakuza, learned from the
      // mafiaRoster event's "yakuza" field (only the mafia faction ever
      // receives it). Lets the mafia (and the Yakuza itself) badge that one
      // teammate "Yakuza" instead of the generic "Mafia". Null until the
      // roster names a Yakuza; persists for the rest of the game (it is not
      // cleared by the re-issued rosters that omit the field), so the Yakuza
      // stays distinctly badged even after it sacrifices itself.
      let yakuzaId = null;

      // hostId is the PlayerID of the room's host, as announced by
      // the server's HostChanged event. The server is the single
      // source of truth — myIsHost is derived (hostId === myId) but
      // we keep both around because some UI paths render the badge
      // on any row (needs hostId), while others gate host-only
      // controls on whether *I* am the host (needs myIsHost).
      // null until the first HostChanged arrives, which is part of
      // every join's prior-event replay.
      let hostId = null;

      // Client-side roster reducer. Keyed by PlayerID. The server is
      // the source of truth; we just fold the public event stream into
      // a small local model so the UI can render a who's-here list
      // without re-querying anything. Rebuilt from scratch on every
      // (re)join so a replay never duplicates entries.
      //
      // Shape: { [pid]: { id, name, alive, deathCause, revealedRole } }
      let players = new Map();

      // nameOf resolves a PlayerID to its current display name, falling
      // back to the raw id when the roster hasn't yet folded in that
      // player's PlayerJoined. That window is real: an id-bearing event
      // (lynch, investigation, vote) can arrive in the same WS batch as,
      // but just before, the naming event. Defined here (helpers.js loads
      // first) so every later file shares the one fallback-correct copy.
      const nameOf = (id) => {
        const p = players.get(id);
        return p ? p.name : id;
      };

      // myNightTurnActive reports whether it is currently the local player's
      // turn to submit a night action. The base case is currentNightRole ===
      // myRole; the Yakuza is the exception — it has no turn of its own and
      // acts during the MAFIA turn, so its window is open when the mafia is
      // up. Mirrors the engine's isActorsTurn. Both render.js (button gating)
      // and actions.js (hint text) call this so they can't drift.
      function myNightTurnActive() {
        return (
          currentNightRole === myRole ||
          (currentNightRole === "mafia" && myRole === "yakuza")
        );
      }

      // iAmMafiaFaction reports whether the LOCAL player shares the mafia
      // faction's collective views (the locked kill target, the recruit
      // target, the fellow-mafia "no target" rule). The Yakuza has no turn of
      // its own — it acts within the Mafia turn and is a full faction member —
      // so the predicate is "mafia OR yakuza", not just "mafia". Centralized
      // so a future mafia-aligned acting role is a one-line change.
      function iAmMafiaFaction() {
        return myRole === "mafia" || myRole === "yakuza";
      }

      // ROLE_VERBS is the single source of truth for each acting role's
      // action verb in its three on-screen forms: the imperative button
      // label (base), the locked-in button label (gerund), and the
      // post-submit confirmation chip (past). render.js and actions.js
      // both read this so a verb rename happens in ONE place instead of
      // drifting across the button and the chip.
      //
      // NOT modeled here (handled at the call sites, because they depend
      // on state a flat table can't express): the doctor's self-row
      // "Save self" variant, and the generic "Target"/"Targeted" fallback
      // for an unknown role. Full-sentence wake/sleep narration is also
      // separate — see ROLE_NARRATION / ROLE_SLEEP in events.js.
      const ROLE_VERBS = {
        mafia:     { base: "Kill",        gerund: "Killing",       past: "Killed" },
        consort:   { base: "Distract",    gerund: "Distracting",   past: "Distracted" },
        detective: { base: "Investigate", gerund: "Investigating", past: "Investigated" },
        vigilante: { base: "Eliminate",   gerund: "Eliminating",   past: "Eliminated" },
        doctor:    { base: "Save",        gerund: "Saving",        past: "Saved" },
        // The Yakuza's PRIMARY action is the faction kill (it acts during the
        // Mafia turn). Its recruit is a separate "Recruit" button with its own
        // fixed copy at the call site, so it isn't a verb-table entry.
        yakuza:    { base: "Kill",        gerund: "Killing",       past: "Killed" },
        tracker:   { base: "Track",       gerund: "Tracking",      past: "Tracked" },
      };

      // Per-phase state. All of these are reset on phaseChanged so a
      // replay reconstructs them correctly from the event stream.
      //   phase        — current Phase string ("lobby", "night", …).
      //   day          — Day number from the engine (0 on first night).
      //   myAction     — Target this player submitted THIS night.
      //                  Populated from the actor's own
      //                  nightActionRecorded event.
      //   mafiaKillTarget — The faction's locked kill target for THIS
      //                  night. The mafia turn is a faction-collective:
      //                  the server sends the nightActionRecorded ack to
      //                  EVERY living mafioso (not just the submitter),
      //                  so co-mafia who didn't click still see who the
      //                  team locked in. Null until the first mafioso
      //                  submits; reset every phase.
      //   votes        — Map<voter, target> for the current day_vote.
      //   winner       — Faction string set by gameEnded.
      let phase = "lobby";
      let day = 0;
      let myAction = null;
      let mafiaKillTarget = null;
      let votes = new Map();
      // votesRevealed mirrors the engine's same-named flag. While false
      // (voting open) the tally is HIDDEN: each client's `votes` map
      // holds only its own vote (the server keeps every other voter's
      // choice private), so we render no counts and no who-voted-for-whom.
      // The host's RevealVotes flips this true and ships the full tally
      // in one votesRevealed event; from then on everyone (incl. dead
      // players) sees the board and the per-row Vote buttons disappear
      // (voting is locked until a Clear & re-vote). Reset to false on
      // any phase change and on voteCleared.
      let votesRevealed = false;
      // votesCastCount is the PUBLIC running count of how many living
      // players have voted in the current (still-hidden) day-vote tally.
      // The individual votes stay private until reveal, so this aggregate —
      // fed by the server's voteProgress event — is the only way a viewer
      // (voter, non-voter, or dead) learns voting progress before the host
      // opens the box. The living total (the "of M" denominator) is NOT
      // carried; we compute it locally from the roster. Reset to 0 on any
      // phase change, on voteCleared, and on votesRevealed.
      let votesCastCount = 0;
      // iAbstained mirrors OUR OWN abstention in the current (hidden) day
      // vote — set by the private voteAbstained event, cleared when we cast a
      // real vote or retract. Like our own vote it's local-only; other
      // players' abstentions stay private (we learn only the aggregate count
      // via votesCastCount). Reset on phase change, voteCleared, votesRevealed.
      let iAbstained = false;
      let winner = null;
      // dayLynchResolved mirrors the engine's same-named flag. Set to
      // true when a PlayerLynched event arrives during day_vote (the
      // PhaseChanged → day_discussion that follows is the post-lynch
      // discussion). Cleared on any PhaseChanged into "night". The
      // host UI keys off this to decide whether to show
      // "Open voting" (false) or "Begin night" (true) while sitting
      // in day_discussion.
      let dayLynchResolved = false;
      // rolesDealt mirrors the engine: true once StartGame has fired
      // and every player has been assigned a role. The host's lobby
      // UI swaps "Start game" → "Begin night" once this becomes true.
      let rolesDealt = false;

      // Lobby configuration mirrored from the server's GameCreated /
      // MafiaCountChanged events.
      //
      // These deliberately start as null rather than hardcoded
      // defaults (5/20/1). The server is the single source of truth
      // for the lobby bounds and the planned mafia count; baking
      // numbers in here too means two places to update if the engine
      // defaults ever change, and a brief window on first paint where
      // the UI shows numbers that don't match the server's. Instead
      // we render placeholders ("…") until the gameCreated event
      // lands — which happens at the top of every join/rejoin
      // handshake — and gate the host's Start button on having
      // received them.
      let lobbyMinPlayers = null;
      let lobbyMaxPlayers = null;
      let mafiaCount = null;

      // consortEnabled mirrors the engine's optional-Consort toggle for
      // the upcoming game. Event-sourced from consortChanged (replayed on
      // join), defaulting to false until the first event lands.
      let consortEnabled = false;

      // vigilanteEnabled mirrors the engine's optional-Vigilante toggle
      // for the upcoming game. Event-sourced from vigilanteChanged
      // (replayed on join), defaulting to false until the first event.
      let vigilanteEnabled = false;

      // yakuzaEnabled mirrors the engine's optional-Yakuza toggle for the
      // upcoming game. Event-sourced from yakuzaChanged (replayed on join),
      // defaulting to false until the first event.
      let yakuzaEnabled = false;

      // trackerEnabled mirrors the engine's optional-Tracker toggle for the
      // upcoming game. Event-sourced from trackerChanged (replayed on join),
      // defaulting to false until the first event.
      let trackerEnabled = false;

      // vigilanteFired tracks whether the LOCAL vigilante has already
      // spent his single bullet (set when our own nightActionRecorded
      // arrives). Used to hide the target picker on subsequent nights —
      // the engine rejects a second shot with ErrAlreadyActed.
      let vigilanteFired = false;

      // heldFireThisTurn tracks whether the LOCAL vigilante pressed "Hold
      // fire" during the CURRENT turn (an explicit NightPass that ends the
      // act window early without spending the bullet). It's a per-turn
      // optimistic flag — set on click, reset at the start of each role
      // turn (nightNarrationStarted) and on entering night — used to hide
      // the picker/button and show a confirmation the instant we pass,
      // before the server's ponder event lands.
      let heldFireThisTurn = false;

      // --- Night turn state (mirrors the engine's per-role sub-phase) ---
      //
      // During PhaseNight each role flows through a five-step sub-phase
      // sequence (narrate → act → ponder → sleep → settle), preceded
      // once per night by a global "opening" sub-phase. The engine is
      // the single source of truth for sub-phase transitions; the
      // client never advances on its own. Every transition arrives as
      // a typed event (nightOpeningStarted, nightNarrationStarted,
      // nightActionStarted, nightPonderStarted, nightSleepStarted,
      // nightSettleStarted) with a deadline stamped by the room.
      //
      // currentNightRole is the role for whom the current sub-phase
      // fires. "" outside a role's sub-phases (i.e. during opening
      // and between roles). nightTurnDeadlineMs is unix-millis at which
      // the CURRENT sub-phase auto-advances on the server. We rebind
      // it on every Night*Started event so the countdown bar tracks
      // the live sub-phase.
      let currentNightRole = "";
      let nightTurnDeadlineMs = 0;

      // iAmBlocked is set true when the server tells us (privately) that
      // the Consort blocked our action this night — delivered at the
      // start of our own act window. It suppresses the target picker for
      // the turn and is cleared at the start of each night.
      let iAmBlocked = false;
      // iAmRecruited is set true when the server tells us (privately) that
      // the Yakuza recruited us into the mafia — delivered at the start of
      // our (now phantom) turn for an active role. Like iAmBlocked it
      // suppresses the target picker for the turn (our original power is
      // gone), and is cleared at the start of each night/turn.
      let iAmRecruited = false;
      // mafiaRecruitTarget is the faction's locked recruit target for THIS
      // night, set from the faction-scoped recruitRecorded event on EVERY
      // living mafia member (the recruiting Yakuza AND co-mafia) — the recruit
      // analogue of mafiaKillTarget. It drives the recruiting Yakuza's
      // "Recruited: X" confirmation, a co-mafioso's "the Yakuza is recruiting
      // X — no kill tonight" notice, and a "Recruit" row badge for the whole
      // faction. Null until a recruit is locked; reset every night. Town never
      // receives recruitRecorded, so it stays null for them.
      let mafiaRecruitTarget = null;
      // currentNightSubPhase mirrors the engine's NightSubPhase value.
      // Drives the audio cue selection (narrate/sleep speak; act/ponder/
      // settle/opening are silent or already-spoken) and the visibility
      // of Target buttons (only during "act" with a living actor).
      // Possible values: "" | "opening" | "narrate" | "act" | "ponder"
      //                | "sleep" | "settle".
      let currentNightSubPhase = "";
      // currentNightTurnPhantom mirrors the engine's per-turn Phantom
      // flag (carried by nightNarrationStarted / nightPonderStarted).
      // True when no living player holds currentNightRole; the
      // moderator audio still plays (so the room can't deduce which
      // role is dead from the absence of cues) but no NightAction is
      // accepted on the server, and no Target buttons render on any
      // client.
      let currentNightTurnPhantom = false;

      // narratorEnabled gates whether THIS device speaks. Only the host
      // narrates — having every player's phone narrate would yield
      // overlapping audio. Toggled by the "Test audio" / "Enable audio"
      // button (which also serves as the user-gesture needed by iOS to
      // unlock speechSynthesis).
      //
      // The user's preference persists in sessionStorage so a refresh
      // mid-game remembers it. BUT a refresh resets the browser's
      // autoplay-gate too — sessionStorage can't satisfy the
      // user-gesture requirement. So after a refresh we land here
      // with narratorEnabled=false even if the host had it on; the
      // host audio bar (#host-audio-bar) makes re-enabling a single
      // tap from any phase.
      const audioPrefKey = "mafia.audio.host";
      let narratorEnabled = false;
      // narratorPreference is the user's INTENT (persisted), distinct
      // from narratorEnabled which is the LIVE state after a gesture
      // this load. After a refresh, preference="on" and enabled=false
      // until the host taps the audio bar.
      let narratorPreference =
        typeof sessionStorage !== "undefined" &&
        sessionStorage.getItem(audioPrefKey) === "on";

      // narrationsSeen is a guard against double-narrating the same
      // event when handleEvent runs over the replay batch on join AND
      // again as live events arrive. We key on a stable string per
      // narration trigger; events not in this set fire normally.
      const narrationsSeen = new Set();

      // dayDiscussionPendingDeaths holds the playerIds killed last night
      // (if any), captured from PlayerKilled in the same event batch
      // as the phaseChanged → day_discussion. The narrator reads it on
      // the dawn cue, then clears it. Empty = "nobody died." It's a LIST
      // because a single night can produce more than one death (e.g. the
      // mafia and the vigilante shoot two different players), and the
      // dawn announcement must name them all.
      let dayDiscussionPendingDeaths = [];

      // lastNightVictims is the same fact as dayDiscussionPendingDeaths,
      // but tracked DURABLY: it stays set across renders and survives
      // page refreshes (rebuilt by the event replay), and is only
      // cleared when the next night begins. The day-discussion banner
      // uses it to surface "Last night: X was killed" as a UI chip,
      // not just a one-shot audio cue. Empty = no death last night
      // (or we haven't seen a night yet).
      //
      // We keep dayDiscussionPendingDeaths separate because the
      // narrator wants single-shot semantics (don't re-narrate a
      // death after a refresh into day_discussion); the banner
      // wants persistent semantics (DO re-display after refresh).
      // Trying to share one variable would mean one of those rules
      // has to give.
      let lastNightVictims = [];

      // spectatorNightActions is the dead player's live feed of the current
      // night's submitted actions, in turn order — each entry is
      // { actor, actorRole, target, targetRole } from a SpectatorNightAction
      // event (graveyard-only, so the living never receive it). Reset at the
      // start of every night so the feed reflects only tonight, and rebuilt
      // from the replay on (re)join. Living players never populate it.
      let spectatorNightActions = [];

      // formatVictimList renders a list of victim ids into a grammatical
      // name string: "Alice", "Alice and Bob", or "Alice, Bob, and Cara"
      // (resolving each id to its current player name). Used by both the
      // dawn narration and the day-discussion banner so a multi-death
      // night names every victim, not just the last one seen.
      function formatVictimList(ids) {
        const names = ids.map((id) => nameOf(id));
        if (names.length === 0) return "";
        if (names.length === 1) return names[0];
        if (names.length === 2) return `${names[0]} and ${names[1]}`;
        return `${names.slice(0, -1).join(", ")}, and ${names[names.length - 1]}`;
      }

      // --- TTS (narrator) ----------------------------------------------
      //
      // We use the browser's built-in speechSynthesis API. No external
      // dependency, no API key, works offline. Quality varies by device
      // but iOS Safari / macOS Safari / modern Chrome all produce
      // intelligible English voices.
      //
      // Quirks worth knowing:
      //   - iOS Safari blocks speechSynthesis until a user gesture has
      //     "unlocked" the audio context. The "Test audio" button in
      //     the lobby satisfies this.
      //   - Calling speak() while another utterance is in progress
      //     queues by default. We rely on that for back-to-back cues
      //     ("Mafia wake up. [pause] Choose your target.").
      //   - feature detection: not all browsers support it. If
      //     ttsSupported is false, the UI falls back to large on-screen
      //     text cards (showNarratorCard).
      const ttsSupported =
        typeof window !== "undefined" &&
        "speechSynthesis" in window &&
        typeof window.SpeechSynthesisUtterance === "function";

      function speak(text, { rate = 0.95, pitch = 1, pauseBefore = 0 } = {}) {
        if (!ttsSupported || !narratorEnabled) return;
        const fire = () => {
          const utter = new SpeechSynthesisUtterance(text);
          utter.rate = rate;
          utter.pitch = pitch;
          window.speechSynthesis.speak(utter);
        };
        if (pauseBefore > 0) {
          setTimeout(fire, pauseBefore);
        } else {
          fire();
        }
      }

      // narrate runs ONLY on the host (gated by narratorEnabled, which
      // only the host ever sets to true). It also shows the spoken
      // text in a visual card so the room can see what the audio said
      // — useful both as an accessibility affordance and as a
      // fallback when TTS is unavailable.
      //
      // key dedupes replays: each narration sets a stable key so we
      // don't repeat the cue when an event arrives twice (once during
      // join replay, once during the live broadcast that immediately
      // follows). Live events that should ALWAYS speak (e.g. a fresh
      // nightOpeningStarted / nightNarrationStarted / nightSleepStarted)
      // pass key=null.
      function narrate(text, { key = null, pauseBefore = 0 } = {}) {
        if (key !== null) {
          if (narrationsSeen.has(key)) return;
          narrationsSeen.add(key);
        }
        showNarratorCard(text);
        if (narratorEnabled) speak(text, { pauseBefore });
      }

      // Shows the spoken text in a top-of-page card for a few seconds.
      // Stacked: multiple lines in a row appear together. The card is
      // visible even on non-host devices, which serves as a fallback
      // when the host's TTS is unavailable — a non-host can read it
      // aloud to the room.
      let narratorCardTimer = null;
      function showNarratorCard(text) {
        const el = $("narrator-card");
        if (!el) return;
        el.textContent = text;
        el.classList.remove("hidden");
        if (narratorCardTimer) clearTimeout(narratorCardTimer);
        narratorCardTimer = setTimeout(() => {
          el.classList.add("hidden");
          el.textContent = "";
        }, 6000);
      }

      // --- Detective toast ---------------------------------------------
      //
      // Shows the detective the result of their last investigation as a
      // prominent modal. Color signals the outcome at a glance: rose
      // (danger) for a mafia hit, emerald for confirmed town.
      //
      // No auto-dismiss: the engine emits detectiveResult in the same
      // batch as phaseChanged → day_discussion, so the underlying UI
      // repaint and "Everybody, wake up" narration fire immediately
      // afterward. A timed dismiss feels like the result vanished
      // mid-read. Instead we REQUIRE the detective to tap "Got it"
      // (or click the dimmed overlay). That gating click is also the
      // detective's own pacing signal — they read, internalize, then
      // dismiss and the day starts for them visually.
      //
      // The dim overlay (bg-slate-950/85) on top of z-50 also masks
      // the day-discussion repaint behind the modal, so the
      // detective isn't visually distracted by phase transitions
      // while reading. The modal is local-only — it never echoes
      // via narrator TTS, since the detective's result is private
      // and we don't want the host's phone speaking it aloud.
      // Modal color palettes, shared by the detective result and the
      // consort-related notices (block / promotion). showModalCard always
      // strips ALL palettes before applying one, so back-to-back notices
      // of different colors never stack classes.
      const MODAL_ROSE    = ["border-rose-500/70",    "bg-rose-950/95",    "text-rose-100"];
      const MODAL_EMERALD = ["border-emerald-500/70", "bg-emerald-950/95", "text-emerald-100"];
      const MODAL_AMBER   = ["border-amber-500/70",   "bg-amber-950/95",   "text-amber-100"];

      // modalAutoDismisses marks the currently-shown modal as one that
      // should clear itself when the actor's night turn advances past its
      // ponder (the "Blocked" notice), rather than waiting for a manual
      // "Got it" tap. The detective result and the promotion announcement
      // leave this false: they must NOT vanish on a timer.
      let modalAutoDismisses = false;

      // pendingNoticeAckId names the one-shot, manually-dismissed notice
      // currently on screen (a recruit / promotion / a specific detective
      // result), or null for transient or no-ack modals. When the player
      // ACTIVELY dismisses the modal (dismissModalCard) we persist this id so
      // the notice is never re-popped on a later replay. A programmatic hide
      // (reconnect teardown via resetGameState, or the Blocked auto-dismiss)
      // discards it WITHOUT marking, so a card the player never actually
      // acknowledged still re-pops on the next replay.
      let pendingNoticeAckId = null;

      function showModalCard(text, palette, eyebrow = "Investigation result", autoDismiss = false, ackId = null) {
        const modal = $("notice-modal");
        const card = $("notice-modal-card");
        const body = $("notice-modal-body");
        if (!modal || !card || !body) return;
        modalAutoDismisses = autoDismiss;
        // Every show resets the pending ack: a card with an ackId is
        // dismiss-to-remember, one without (block notice, future modals)
        // is not, and a fresh modal must never inherit the prior card's id.
        pendingNoticeAckId = ackId;
        // The eyebrow is the small uppercase heading above the body. It
        // defaults to the detective's label, so block/promotion notices
        // must pass their own — otherwise they'd misleadingly read
        // "Investigation result".
        const eyebrowEl = $("notice-modal-eyebrow");
        if (eyebrowEl) eyebrowEl.textContent = eyebrow;
        card.classList.remove(...MODAL_ROSE, ...MODAL_EMERALD, ...MODAL_AMBER);
        card.classList.add(...palette);
        body.textContent = text;
        modal.classList.remove("hidden");
        // Inline display:flex wins over EVERY Tailwind utility, no
        // matter what order .hidden / .flex appear in the generated
        // stylesheet. Intentional defensive overkill.
        modal.style.display = "flex";
      }

      function showDetectiveToast(targetName, isMafia, ackId) {
        showModalCard(
          isMafia
            ? `${targetName} IS a mafia member.`
            : `${targetName} is NOT a mafia member.`,
          isMafia ? MODAL_ROSE : MODAL_EMERALD,
          "Investigation result",
          false,
          ackId
        );
      }

      // showTrackerToast tells the tracker who their target visited tonight.
      // visitedName is null when the target took no action ("stayed home").
      // It reveals only the visit, never what the action was. Same
      // dismiss-to-remember mechanics as the detective result.
      function showTrackerToast(targetName, visitedName, ackId) {
        showModalCard(
          visitedName
            ? `${targetName} visited ${visitedName}.`
            : `${targetName} stayed home.`,
          MODAL_EMERALD,
          "Tracking result",
          false,
          ackId
        );
      }

      // showBlockedToast tells a roleblocked player their action was
      // nullified. Reuses the shared notice modal (same "Got it" dismissal).
      function showBlockedToast() {
        showModalCard(
          "The Consort distracted you. You cannot act.",
          MODAL_AMBER,
          "Distracted",
          true // auto-clears when our ponder elapses (see nightSleepStarted)
        );
      }

      // showPromotedToast announces a consort's promotion to full mafia
      // (the cabal was wiped out and she's taken over the kill).
      function showPromotedToast(ackId) {
        showModalCard(
          "The mafia have been wiped out — you are now the Mafia. You'll choose the kill from the next night on.",
          MODAL_ROSE,
          "You've been promoted",
          false,
          ackId
        );
      }

      // showRecruitedToast tells a player the Yakuza recruited them into the
      // mafia. Like the promotion announcement (and unlike the transient
      // Distracted notice) it does NOT auto-dismiss: a recruit is a permanent
      // role change, so the convert must tap "Got it" to acknowledge it. This
      // also keeps the two delivery paths consistent — an active role learns
      // at their (suppressed) turn, a villager at resolution (no turn, so no
      // sleep cue would ever auto-clear it anyway).
      function showRecruitedToast(ackId) {
        showModalCard(
          "The Yakuza recruited you — you are now the Mafia. Your old power is gone; you'll act with the family from the next night on.",
          MODAL_ROSE,
          "You've been recruited",
          false,
          ackId
        );
      }

      // hideModalCard is the PROGRAMMATIC hide: it tears the card down
      // without acknowledging it (reconnect teardown via resetGameState, the
      // Blocked auto-dismiss). Dropping pendingNoticeAckId unmarked means a
      // one-shot notice the player never tapped through re-pops on the next
      // replay. User-driven dismissal goes through dismissModalCard.
      function hideModalCard() {
        const modal = $("notice-modal");
        if (!modal) return;
        modal.classList.add("hidden");
        modal.style.display = "none";
        const body = $("notice-modal-body");
        if (body) body.textContent = "";
        modalAutoDismisses = false;
        pendingNoticeAckId = null;
      }

      // dismissModalCard is the USER-driven dismissal (Got it / backdrop /
      // Escape). If the card carried an ack id, record it first so the
      // notice is never re-shown on a later replay, then hide.
      function dismissModalCard() {
        if (pendingNoticeAckId) markNoticeAcked(pendingNoticeAckId);
        hideModalCard();
      }

      // --- Night countdown bar -----------------------------------------
      //
      // The night banner shows a per-turn countdown that ticks down to
      // the engine-stamped deadline. Updates ~10×/second so the bar
      // smoothly drains and the seconds number is responsive.
      //
      // Only the actor ticks the countdown, and only during the "act"
      // sub-phase — see viewerOwnsCurrentTimer for the rationale
      // (narrate/ponder/sleep/settle are passive for the actor, and
      // showing timing to non-actors leaks deliberation pace).
      let nightCountdownInterval = null;
      let nightCountdownTotalMs = 0; // initial duration for the bar's %

      function startNightCountdown(deadlineMs) {
        stopNightCountdown();
        if (!deadlineMs) return;
        if (!viewerOwnsCurrentTimer()) return;
        nightCountdownTotalMs = Math.max(1, deadlineMs - Date.now());
        renderNightCountdownFrame();
        nightCountdownInterval = setInterval(renderNightCountdownFrame, 100);
      }

      function stopNightCountdown() {
        if (nightCountdownInterval) {
          clearInterval(nightCountdownInterval);
          nightCountdownInterval = null;
        }
      }

      // The countdown is shown ONLY to the actor of the current
      // sub-phase, AND only while we're in the "act" sub-phase — the
      // window during which they actually need to make a decision.
      //
      // Two reasons to gate this tightly:
      //
      //   1. Information leak. Showing a draining bar to spectators
      //      would tell the room exactly how long the actor took to
      //      decide (60s of deliberation vs an instant click is
      //      meaningful signal that could be used against them in
      //      the next discussion). Dead former-actors are spectators
      //      here too — the alive check covers them.
      //
      //   2. Signal-to-noise. During the actor's OWN narrate /
      //      ponder / sleep / settle sub-phases, there's nothing to
      //      decide — they're just listening to audio or sitting in
      //      a brief pause. A countdown bar in those windows is
      //      visual noise that suggests urgency where there is none.
      //      The only window where the seconds-remaining is
      //      actionable information for the actor is "act".
      //
      // Opening is global ("City, go to sleep"), has no actor, and
      // similarly has nothing actionable — so no countdown either.
      function viewerOwnsCurrentTimer() {
        if (currentNightSubPhase !== "act") return false;
        // myNightTurnActive (not a bare currentNightRole === myRole) so the
        // Yakuza — which has no turn of its own and acts within the MAFIA
        // turn — owns the countdown during its act window too. The bare
        // comparison left the Yakuza with a picker but no timer bar.
        if (!currentNightRole || !myNightTurnActive()) return false;
        const me = players.get(myId);
        return !!(me && me.alive);
      }

      function renderNightCountdownFrame() {
        const text = $("night-banner-countdown");
        const bar = $("night-banner-bar");
        if (!viewerOwnsCurrentTimer()) {
          if (text) text.textContent = "";
          if (bar) bar.style.width = "0%";
          // Stop ticking entirely outside the act window — the next
          // sub-phase event will re-arm the interval if/when this
          // viewer becomes the actor again.
          stopNightCountdown();
          return;
        }
        const remaining = Math.max(0, nightTurnDeadlineMs - Date.now());
        const seconds = Math.ceil(remaining / 1000);
        const pct = Math.max(0, Math.min(100, (remaining / nightCountdownTotalMs) * 100));
        if (text) text.textContent = `${seconds}s`;
        if (bar) bar.style.width = `${pct}%`;
        if (remaining <= 0) stopNightCountdown();
      }

      // Audio enablement (host-only). speechSynthesis on iOS requires
      // a user-gesture before it will speak; this button is what
      // satisfies that requirement. It also lets the host test the
      // voice quality before starting the game.
      function enableNarrator() {
        if (!ttsSupported) {
          showNarratorCard("Text-to-speech is not available on this device. The on-screen card will still show every cue.");
          return;
        }
        narratorEnabled = true;
        narratorPreference = true;
        try { sessionStorage.setItem(audioPrefKey, "on"); } catch {}
        speak("Audio enabled. Ready when you are.", { rate: 1 });
        showNarratorCard("Audio enabled. The host's phone will narrate the game.");
        renderHostAudioBar();
        renderActionPanel(); // rerender so the button switches state
      }

      function disableNarrator() {
        narratorEnabled = false;
        narratorPreference = false;
        try { sessionStorage.removeItem(audioPrefKey); } catch {}
        // Cancel any in-flight utterances so a tap-to-mute is snappy.
        if (ttsSupported) {
          try { window.speechSynthesis.cancel(); } catch {}
        }
        renderHostAudioBar();
        renderActionPanel();
      }

      // renderHostAudioBar paints the persistent audio toggle / status
      // pill that lives above the action panel. Visible only for the
      // host (non-hosts never narrate) AND only after they've joined a
      // room. Three visual states:
      //
      //   - TTS unsupported  -> info chip, no buttons.
      //   - audio not yet
      //     unlocked         -> prominent amber "Enable audio" CTA.
      //                        This is the recovery path after a refresh.
      //   - audio unlocked   -> green "Audio: on" pill with a small
      //                        "Mute" affordance.
      //
      // Non-hosts and the pre-room (lobby) state hide the bar entirely.
      function renderHostAudioBar() {
        const bar = $("host-audio-bar");
        if (!bar) return;
        if (!myIsHost || !myId) {
          bar.classList.add("hidden");
          bar.innerHTML = "";
          return;
        }
        bar.innerHTML = "";
        bar.className =
          "flex items-center justify-between gap-3 rounded border p-3";

        if (!ttsSupported) {
          bar.classList.add("border-slate-700", "bg-slate-800/40", "text-slate-300");
          const note = document.createElement("div");
          note.className = "text-sm";
          note.textContent =
            "Audio narration isn't available on this device. The on-screen card will still show every cue.";
          bar.appendChild(note);
          return;
        }

        if (!narratorEnabled) {
          // Either first load (preference=off) or post-refresh
          // (preference=on but gesture not yet given). Either way
          // the CTA is the same: tap to enable.
          bar.classList.add("border-amber-500/60", "bg-amber-950/40", "text-amber-100");
          const note = document.createElement("div");
          note.className = "min-w-0 text-sm";
          note.textContent = narratorPreference
            ? "Audio paused after refresh. Tap to re-enable."
            : "Narrator audio is off. Enable it on the host's phone.";
          const btn = document.createElement("button");
          btn.className =
            "inline-flex min-h-[44px] shrink-0 items-center justify-center rounded bg-amber-600 px-4 py-2 text-sm font-medium text-white hover:bg-amber-500";
          btn.textContent = narratorPreference ? "Re-enable audio" : "Enable audio";
          btn.addEventListener("click", () => enableNarrator());
          bar.appendChild(note);
          bar.appendChild(btn);
          return;
        }

        bar.classList.add("border-emerald-700/60", "bg-emerald-950/40", "text-emerald-100");
        const note = document.createElement("div");
        note.className = "min-w-0 text-sm";
        note.textContent = "Audio: on. Narration will play on this device.";
        const btn = document.createElement("button");
        btn.className =
          "inline-flex min-h-[44px] shrink-0 items-center justify-center rounded bg-emerald-700 px-4 py-2 text-sm font-medium text-white hover:bg-emerald-600";
        btn.textContent = "Mute";
        btn.addEventListener("click", () => disableNarrator());
        bar.appendChild(note);
        bar.appendChild(btn);
      }

      // When we kick off a rejoin attempt from stored credentials
      // (e.g. on page reload) we remember the room code here. If the
      // server then sends an "error" before we ever receive "rejoined"
      // / "joined", we know the stored credentials are stale and we
      // can recover by clearing them and showing the join lobby. Once
      // a successful (re)join arrives this is cleared.
      let pendingRejoinCode = null;

      // pendingJoinCode is the symmetric flag for first-time joins
      // (no stored credentials). Set immediately before we send the
      // "join" frame and cleared on "joined" ack or on any "error"
      // frame. Lets us distinguish "the lobby is closed" / "lobby
      // full" errors during a join handshake from in-game command
      // errors that arrive over the same channel, so we can show
      // a friendly message instead of the raw engine sentinel.
      let pendingJoinCode = null;

      // --- Auto-reconnect state -----------------------------------------
      //
      // Once we've successfully joined a room, a dropped socket should
      // heal itself rather than stranding the player on a dead screen.
      // Drops are common in this game's usage: phones lock/pocket during
      // the 15–20 min verbal day discussion (mobile browsers suspend the
      // tab and tear down the WebSocket), and idle proxies/NATs cull
      // quiet connections. The server replays our full state on rejoin,
      // so a reconnect is invisible once it lands.
      //
      // currentRoomCode is the room we're connected to (or reconnecting
      // to); creds for it live in credStore under storageKey(code).
      let currentRoomCode = null;
      // lastSeq is the resume cursor: the highest event sequence we've
      // applied this session. On an in-session reconnect we send it as
      // ?since=N so the server replies with only the events we missed
      // instead of the whole log. It is deliberately in-memory only — a
      // full page reload clears the model, so it resets to 0 and does a
      // full resync (a delta would have no base state to apply onto).
      let lastSeq = 0;
      // reconnecting is true while the established-drop retry loop is
      // active. It distinguishes "lost an in-game connection, keep
      // retrying" from the page-load auto-rejoin (pendingRejoinCode),
      // which bounces to the lobby on first failure.
      let reconnecting = false;
      // reconnectAttempts feeds the exponential backoff; reset to 0 on
      // any successful (re)join and on a foreground/network resume.
      let reconnectAttempts = 0;
      // reconnectTimer holds the pending setTimeout handle so we never
      // stack multiple retries.
      let reconnectTimer = null;

      const RECONNECT_BASE_MS = 500;
      const RECONNECT_CAP_MS = 10000;

      // reconnectDelayMs returns the next backoff delay and advances the
      // attempt counter. Exponential (0.5s, 1s, 2s, … capped at 10s)
      // with ±20% jitter so a roomful of clients that dropped together
      // (e.g. a transient server blip) don't reconnect in lockstep.
      function reconnectDelayMs() {
        const raw = Math.min(RECONNECT_CAP_MS, RECONNECT_BASE_MS * 2 ** reconnectAttempts);
        reconnectAttempts++;
        return Math.round(raw * (0.8 + Math.random() * 0.4));
      }

      // cancelReconnect clears any scheduled retry and stops the loop.
      function cancelReconnect() {
        if (reconnectTimer) {
          clearTimeout(reconnectTimer);
          reconnectTimer = null;
        }
        reconnecting = false;
      }

      // scheduleReconnect arms a single backoff retry. Guarded so a
      // burst of close/error events can't stack timers. We retry
      // indefinitely: an in-person game can sit idle for a long time
      // and we'd rather keep trying than give up and force a manual
      // reload. The auth_failed path (stale creds) is what breaks the
      // loop and bounces to the lobby.
      function scheduleReconnect() {
        if (reconnectTimer) return;
        reconnecting = true;
        const delay = reconnectDelayMs();
        showReconnectingBanner(true);
        setStatus(`reconnecting to ${currentRoomCode}…`, "text-amber-400");
        reconnectTimer = setTimeout(() => {
          reconnectTimer = null;
          reconnectNow();
        }, delay);
      }

      // reconnectNow opens a fresh rejoin socket using the stored creds
      // for the current room. If creds are missing/corrupt (shouldn't
      // happen post-join), we bounce to the lobby instead of looping.
      function reconnectNow() {
        const code = currentRoomCode;
        if (!code) return;
        const stored = credStore.getItem(storageKey(code));
        if (!stored) {
          recoverToLobby(code, "Disconnected — rejoin to continue.");
          return;
        }
        let creds;
        try { creds = JSON.parse(stored); }
        catch { recoverToLobby(code, "Disconnected — rejoin to continue."); return; }
        reconnecting = true;
        setStatus(`reconnecting to ${code}…`, "text-amber-400");
        connect(code, null, creds);
      }

      // resumeConnectionIfNeeded fires when the tab returns to the
      // foreground (visibility/pageshow) or the network comes back
      // (online). A suspended mobile tab can't run our backoff timer,
      // so this is the path that actually reconnects a phone taken out
      // of a pocket. We reset the backoff so the resume is snappy.
      function resumeConnectionIfNeeded() {
        if (!myId || !currentRoomCode) return;
        if (ws && (ws.readyState === WebSocket.OPEN || ws.readyState === WebSocket.CONNECTING)) return;
        cancelReconnect();
        reconnectAttempts = 0;
        reconnectNow();
      }

      // showReconnectingBanner toggles a fixed top-of-screen strip so a
      // reconnecting player gets visible feedback even while the in-game
      // view (not the lobby status line) is on screen. Created lazily so
      // it costs nothing until the first drop.
      let reconnectBanner = null;
      function showReconnectingBanner(show) {
        if (show) {
          if (!reconnectBanner) {
            reconnectBanner = document.createElement("div");
            reconnectBanner.className =
              "fixed inset-x-0 top-0 z-50 bg-amber-500 text-slate-900 text-center text-sm font-semibold py-1.5 shadow-lg";
            reconnectBanner.textContent = "Reconnecting…";
            document.body.appendChild(reconnectBanner);
          }
          reconnectBanner.classList.remove("hidden");
        } else if (reconnectBanner) {
          reconnectBanner.classList.add("hidden");
        }
      }

      function setStatus(text, klass = "text-slate-400") {
        status.textContent = text;
        status.className = `text-sm ${klass}`;
      }

      // Builds the canonical share link for a room. Always pointed at
      // the current origin so it Just Works in dev (localhost:8080) and
      // anywhere we deploy later.
      function shareLinkFor(code) {
        return `${location.origin}/?room=${encodeURIComponent(code)}`;
      }

      // Reads ?room=XYZW from the URL and normalises to upper-case.
      // Returns null if there is no room param or it's empty.
      function roomFromURL() {
        const raw = new URLSearchParams(location.search).get("room");
        if (!raw) return null;
        return raw.trim().toUpperCase();
      }

      // Reads ?name=Alice from the URL (trimmed). Returns null when the
      // param is absent or blank. Only consulted by the auto-join demo
      // flow (maybeAutoJoinFromURL); normal share links never carry it.
      function nameFromURL() {
        const raw = new URLSearchParams(location.search).get("name");
        if (!raw) return null;
        const trimmed = raw.trim();
        return trimmed.length > 0 ? trimmed : null;
      }

      function showGame(code) {
        $("lobby").classList.add("hidden");
        $("game").classList.remove("hidden");
        $("room-code").textContent = code;
        $("share-url").textContent = shareLinkFor(code);
      }

      // Renders the YOU box: "Name (host)". Name is server-truth.
      // The wire-level PlayerID (p1, p2, ...) is intentionally
      // omitted: it cluttered the strip without giving players any
      // useful information (they think of each other by name, and
      // the server now rejects duplicate names case-insensitively,
      // so visual ambiguity can't happen). pid stays on the
      // function signature in case a future debug overlay wants it.
      function formatMe(name, _pid, isHost) {
        const safeName = name && name.trim() ? name : "(unnamed)";
        return `${safeName}${isHost ? " (host)" : ""}`;
      }

