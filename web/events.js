      // ----- WebSocket plumbing ------------------------------------------

      function send(type, data = {}) {
        if (!ws || ws.readyState !== WebSocket.OPEN) return;
        ws.send(JSON.stringify({ type, data }));
      }

      // enterRoomFromServer applies the session-entry setup shared by a
      // join and a rejoin. With {delta:true} (a cursor-resume rejoin that
      // carries only the events we missed) it KEEPS the current model and
      // applies the tail on top; otherwise it rebuilds from scratch
      // (resetGameState) and replays the full projected log. Either way it
      // adopts the server's high-water mark (d.lastSeq) as our cursor — this
      // covers events filtered from our view (we never received them but
      // must still advance past them) and the join backlog (whose events
      // carry no per-event sequence).
      function enterRoomFromServer(d, { delta = false } = {}) {
        myId = d.playerId;
        myIsHost = !!d.isHost;
        pendingRejoinCode = null;
        cancelReconnect();
        reconnectAttempts = 0;
        showReconnectingBanner(false);
        if (!delta) resetGameState();
        $("me").textContent = formatMe(d.name, d.playerId, d.isHost);
        showGame(d.roomCode);
        // (d.events || []) makes the presence guard universal: a rejoin
        // always carries events (full log or delta tail), a fresh join may
        // omit them.
        for (const env of (d.events || [])) handleEvent(env, { replaying: true });
        if (typeof d.lastSeq === "number") lastSeq = d.lastSeq;
        // Re-arm the act-window countdown after a replay. The per-sub-phase
        // handlers skip startNightCountdown while replaying (so a reconnect
        // doesn't restart it on every historical sub-phase) — but once the
        // replay has settled the model on the CURRENT sub-phase, the actor's
        // timer must keep draining. Without this, a refresh mid-act leaves the
        // bar frozen full. startNightCountdown self-gates on
        // viewerOwnsCurrentTimer (only the current actor, only during "act")
        // and on a non-zero deadline, so it's a no-op for a fresh join, a
        // non-actor, or any non-act sub-phase.
        startNightCountdown(nightTurnDeadlineMs);
      }

      function handleServerMessage(msg) {
        switch (msg.type) {
          case "joined": {
            const d = msg.data;
            // Fresh join: also clear the pending-join guard and persist
            // our credentials so a later tab refresh can silently
            // auto-rejoin. The server attaches its projected view of prior
            // events; our own PlayerJoined arrives as a separate "event"
            // frame immediately after.
            pendingJoinCode = null;
            credStore.setItem(storageKey(d.roomCode), JSON.stringify({
              playerId: d.playerId,
              secret: d.secret,
            }));
            enterRoomFromServer(d);
            break;
          }
          case "rejoined": {
            // Reconnect (or page-load auto-rejoin) succeeded. fromSeq>0 means
            // the server sent only the tail since our cursor (a delta we
            // apply onto the existing model); fromSeq===0 is a full snapshot
            // we rebuild from scratch.
            enterRoomFromServer(msg.data, { delta: msg.data.fromSeq > 0 });
            break;
          }
          case "event":
            handleEvent(msg.data.event);
            // Advance the resume cursor. Set unconditionally (not max-ed):
            // the WebSocket delivers events in order, and a gameReset
            // rebaselines the log to a LOWER sequence that we must adopt.
            if (typeof msg.data.seq === "number") lastSeq = msg.data.seq;
            break;
          case "error": {
            const code = msg.data && msg.data.code;
            const message =
              (msg.data && (msg.data.message || msg.data.code)) || "error";

            // First-time join error: the server has already shaped a
            // player-facing message (see room.joinErrorFor); we just
            // render it and pivot the lobby appropriately.
            if (pendingJoinCode) {
              const failedCode = pendingJoinCode;
              pendingJoinCode = null;
              if (ws) {
                ws.onclose = null;
                ws.onerror = null;
                try { ws.close(); } catch {}
                ws = null;
              }
              // A duplicate name is the one join error the SAME room can
              // still accept — the player just needs a different name. Keep
              // the "Join room XYZ" screen (recoverToLobby with the code) so
              // they can rename and retry without re-entering the code. Every
              // other join error (in progress, full, ended) means the room
              // can't take them at all, so offer to create a fresh one with
              // the name they typed.
              if (code === "duplicate_name") {
                recoverToLobby(failedCode, message);
              } else {
                showUnjoinableRoom(failedCode, message);
              }
              break;
            }

            showError(message);

            // If we were (auto-)rejoining and the server can't
            // authenticate us, the stored credentials are stale —
            // typically because the server restarted, the room was
            // closed, or the player slot was evicted. This covers both
            // the page-load auto-rejoin (pendingRejoinCode) and the
            // in-game reconnect loop (reconnecting); in the latter case
            // auth_failed is the signal that breaks the retry loop
            // instead of looping forever against dead creds. Clear them
            // and drop the user on the join screen for this room so
            // they can re-enter as a fresh player.
            if ((pendingRejoinCode || reconnecting) && code === "auth_failed") {
              const stale = pendingRejoinCode || currentRoomCode;
              pendingRejoinCode = null;
              cancelReconnect();
              showReconnectingBanner(false);
              credStore.removeItem(storageKey(stale));
              // Detach handlers so the synthetic close from our own
              // teardown doesn't overwrite the recovery status message.
              if (ws) {
                ws.onclose = null;
                ws.onerror = null;
                try { ws.close(); } catch {}
                ws = null;
              }
              recoverToLobby(stale, "Previous session expired — rejoin as a new player.");
            }
            break;
          }
          default:
            // Unknown top-level frame — should never happen in a
            // version-matched client/server pair. Surface it so we
            // notice during dev (and don't silently drop something
            // important).
            showError(`Unknown server message: ${msg.type}`);
        }
      }

      function handleEvent(env, { replaying = false } = {}) {
        switch (env.type) {
          case "gameCreated":
            // The server's GameCreated event is authoritative for
            // these three fields and always populates them; we don't
            // fall back to a previous value (or a hardcoded default)
            // because that would just hide a wire-protocol regression.
            lobbyMinPlayers = env.data.minPlayers;
            lobbyMaxPlayers = env.data.maxPlayers;
            mafiaCount = env.data.mafiaCount;
            renderAll();
            break;

          case "mafiaCountChanged":
            mafiaCount = env.data.to;
            renderActionPanel();
            break;

          case "consortChanged":
            consortEnabled = env.data.enabled;
            renderActionPanel();
            break;

          case "vigilanteChanged":
            vigilanteEnabled = env.data.enabled;
            renderActionPanel();
            break;

          case "yakuzaChanged":
            yakuzaEnabled = env.data.enabled;
            renderActionPanel();
            break;

          case "trackerChanged":
            trackerEnabled = env.data.enabled;
            renderActionPanel();
            break;

          case "playerJoined":
            upsertPlayer(env.data.playerId, {
              name: env.data.name,
              alive: true,
            });
            break;

          case "hostChanged":
            // The server is authoritative for who the host is.
            // We keep myIsHost in sync (used to gate host-only
            // controls) and re-render so the "Host" badge appears
            // next to the right player for everyone in the room,
            // not just the host themselves.
            hostId = env.data.playerId;
            myIsHost = hostId === myId;
            renderAll();
            break;

          case "playerKilled":
            upsertPlayer(env.data.playerId, {
              alive: false,
              deathCause: "killed",
            });
            // Stash the death twice: dayDiscussionPendingDeaths
            // drives the (single-shot) dawn narration and is
            // consumed by narratePhaseChange; lastNightVictims
            // drives the (persistent) day-discussion banner chip
            // and is only cleared when the next night begins
            // (see the to === "night" branch in phaseChanged).
            // Both are lists: a night can land multiple kills (mafia
            // + vigilante on different targets), so we APPEND rather
            // than overwrite to keep every victim.
            dayDiscussionPendingDeaths.push(env.data.playerId);
            lastNightVictims.push(env.data.playerId);
            break;

          case "playerLynched":
            upsertPlayer(env.data.playerId, {
              alive: false,
              deathCause: "lynched",
            });
            // A lynch only fires after the host's FinalizeVotes, so we
            // flip the resolved flag here. The PhaseChanged →
            // day_discussion that follows in the same batch will read
            // this when rendering the host buttons (Begin night
            // instead of Open voting).
            dayLynchResolved = true;
            if (!replaying) {
              narrate(`${nameOf(env.data.playerId)} has been voted out.`);
            }
            break;

          case "noLynch":
            // The host finalized a day vote that didn't reach a
            // majority (split, abstentions, or no votes): nobody is
            // lynched, but the day still resolves. Mirror the same
            // dayLynchResolved flag a real lynch sets so the host UI
            // offers "Begin night" (not "Open voting") on the
            // day_discussion that follows in this batch.
            dayLynchResolved = true;
            if (!replaying) {
              narrate("No one was voted out.");
            }
            break;

          case "phaseChanged": {
            const from = env.data.from;
            const to = env.data.to;
            phase = to;
            if (typeof env.data.day === "number") day = env.data.day;
            myAction = null;
            mafiaKillTarget = null;
            votes = new Map();
            // Each fresh phase (notably a new day_vote opened by the
            // host) starts with the tally hidden again and no votes cast.
            votesRevealed = false;
            votesCastCount = 0;
            iAbstained = false;
            // Going INTO night clears any prior turn state; the
            // accompanying nightOpeningStarted / nightNarrationStarted
            // events in the same batch will set the new sub-phase.
            if (to === "night") {
              currentNightRole = "";
              nightTurnDeadlineMs = 0;
              currentNightSubPhase = "";
              currentNightTurnPhantom = false;
              iAmBlocked = false;
              iAmRecruited = false;
              mafiaRecruitTarget = null;
              heldFireThisTurn = false;
              stopNightCountdown();
              dayLynchResolved = false;
              // A new night invalidates the previous night's
              // victims — the banner chip ("Last night: X was
              // killed") should not survive into the next day's
              // discussion. If this night produces no death,
              // lastNightVictims stays empty and the chip stays
              // hidden, which is correct.
              lastNightVictims = [];
              // Clear the pending-dawn-announcement list here too, not only
              // when narratePhaseChange consumes it — that consumption is
              // gated behind !replaying, so on a refresh-replay across several
              // nights the list would otherwise accumulate EVERY night's kills
              // and the next LIVE dawn would announce the whole game's dead.
              // Scoping it to the current night (like lastNightVictims) keeps
              // the announcement to last night's victims only.
              dayDiscussionPendingDeaths = [];
              // A fresh night starts the spectator feed over: the dead
              // watch THIS night's actions, not last night's.
              spectatorNightActions = [];
            }
            renderAll();

            if (!replaying) {
              narratePhaseChange(from, to, env.data.day);
            }
            break;
          }

          case "nightOpeningStarted": {
            // One-shot global "City, go to sleep" sub-phase that fires
            // exactly once per night, BEFORE any role's narrate. No
            // role is associated with it (the event carries only day
            // and deadline). The room arms a timer for the sub-phase
            // duration; when it elapses the engine transitions to
            // mafia's narrate.
            currentNightRole = "";
            currentNightSubPhase = "opening";
            currentNightTurnPhantom = false;
            nightTurnDeadlineMs = env.data.deadline || 0;
            if (!replaying) {
              startNightCountdown(nightTurnDeadlineMs);
              narrateNightOpening();
            }
            renderActionPanel();
            renderPlayers();
            break;
          }

          case "nightNarrationStarted": {
            // Role's wake-up narration. No action is accepted yet —
            // the host's phone speaks the cue while the room listens.
            // The engine will transition us to "act" (or directly to
            // "ponder" for phantom turns) when this sub-phase's
            // server-side timer elapses.
            currentNightRole = env.data.role || "";
            currentNightSubPhase = "narrate";
            currentNightTurnPhantom = !!env.data.phantom;
            nightTurnDeadlineMs = env.data.deadline || 0;
            // A fresh role turn begins: clear the per-turn "held fire"
            // flag so a pass on an earlier turn doesn't suppress the
            // vigilante's controls when his next live turn opens.
            heldFireThisTurn = false;
            if (!replaying) {
              startNightCountdown(nightTurnDeadlineMs);
              narrateRoleWake(currentNightRole, day);
            }
            renderActionPanel();
            renderPlayers();
            break;
          }

          case "nightActionStarted": {
            // The actor's submission window. Target buttons render
            // for the matching role (gated in renderPlayerRow on
            // currentNightSubPhase === "act"). Phantom turns never
            // enter this sub-phase server-side, so we don't need to
            // re-check the phantom flag here.
            currentNightRole = env.data.role || "";
            currentNightSubPhase = "act";
            nightTurnDeadlineMs = env.data.deadline || 0;
            // The act window is the only sub-phase the server sizes with a
            // duration (for the countdown bar's proportion); 0 if absent.
            nightTurnTotalMs = env.data.duration || 0;
            if (!replaying) {
              startNightCountdown(nightTurnDeadlineMs);
            }
            renderActionPanel();
            renderPlayers();
            break;
          }

          case "nightPonderStarted": {
            // Brief silent pause after a submission (or a timeout, or
            // the phantom random delay). The Target buttons are
            // gone — the actor either submitted or the window
            // expired — and the room sits in silence waiting for
            // the sleep cue. Detective's result modal also has time
            // to be read during this sub-phase.
            currentNightRole = env.data.role || "";
            currentNightSubPhase = "ponder";
            currentNightTurnPhantom = !!env.data.phantom;
            nightTurnDeadlineMs = env.data.deadline || 0;
            if (!replaying) {
              startNightCountdown(nightTurnDeadlineMs);
            }
            renderActionPanel();
            renderPlayers();
            break;
          }

          case "nightSleepStarted": {
            // Role's "go to sleep" narration. Speak the role-specific
            // cue; the engine will transition to settle on this
            // sub-phase's timer.
            currentNightRole = env.data.role || "";
            currentNightSubPhase = "sleep";
            nightTurnDeadlineMs = env.data.deadline || 0;
            if (!replaying) {
              // Sleep means our ponder just elapsed. If the auto-dismiss
              // "Distracted" notice is still up, clear it now so the player
              // isn't stuck behind it even if they never tapped "Got it".
              if (modalAutoDismisses) hideModalCard();
              startNightCountdown(nightTurnDeadlineMs);
              narrateRoleSleep(currentNightRole);
            }
            renderActionPanel();
            renderPlayers();
            break;
          }

          case "nightSettleStarted": {
            // Brief silent pause between roles. No audio cue. The
            // engine transitions to the next role's narrate (or to
            // night resolution) when the timer elapses.
            currentNightRole = env.data.role || "";
            currentNightSubPhase = "settle";
            nightTurnDeadlineMs = env.data.deadline || 0;
            if (!replaying) {
              startNightCountdown(nightTurnDeadlineMs);
            }
            renderActionPanel();
            renderPlayers();
            break;
          }

          case "roleAssigned":
            // Each player only ever sees their own RoleAssigned, so
            // receiving any RoleAssigned is enough to know roles
            // have been dealt (server-side projection guarantees the
            // event only fires for us). The host UI uses this to
            // swap "Start game" → "Begin night", and the invite
            // banner hides because the room is now closed to joins.
            rolesDealt = true;
            if (env.data.playerId === myId) {
              // This is either the initial deal or a mid-game Yakuza recruit
              // flipping US to mafia. Either way, just adopt the new role —
              // the "you've been recruited" TOAST rides on the separate,
              // engine-emitted `recruited` event (exactly one per convert,
              // timed correctly), so we deliberately don't toast here. That
              // keeps villagers from getting a double toast and lets a
              // blocked-then-recruited player see "distracted" at their turn
              // and "recruited" at resolution as two distinct notices.
              myRole = env.data.role;
              $("my-role").textContent = env.data.role;
            }
            // Re-render everything: rolesDealt flips multiple
            // surfaces (action panel + invite banner + player
            // count framing on next phase). One renderAll() is
            // cheaper to reason about than tracking which
            // surfaces care about which fields.
            renderAll();
            break;

          case "mafiaRoster":
            // Faction-scoped: the server only sends this to mafia, so
            // simply receiving it means I'm mafia and these are my
            // teammates. Faction knowledge only ever WIDENS, so we MERGE the
            // revealed members into mafiaPeers rather than replacing it: a
            // re-issued roster (a consort promotion, or a Yakuza recruit)
            // lists only the LIVING members, so replacing would drop a
            // teammate who has since died — e.g. a Yakuza that sacrificed
            // itself on a recruit — and strip its "Mafia" badge. Merging
            // keeps every known mafioso badged for the rest of the game.
            for (const m of (env.data.members || [])) mafiaPeers.add(m);
            // Remember which member is the Yakuza so its row badges "Yakuza"
            // instead of the generic "Mafia". Only set when the field is
            // present (the StartGame reveal); re-issued rosters omit it, so we
            // don't clear a known yakuzaId — the Yakuza stays badged after it
            // dies on a recruit.
            if (env.data.yakuza) yakuzaId = env.data.yakuza;
            renderPlayers();
            break;

          case "nightActionRecorded":
            // The mafia turn is a faction-collective: the server sends
            // this ack to EVERY living mafioso, so a teammate's locked
            // kill must update our view even though we didn't click.
            // Town acks (doctor/detective) are PrivateTo the actor, so
            // for those env.data.actor === myId always holds.
            if (env.data.faction === "mafia") {
              mafiaKillTarget = env.data.target;
            }
            if (env.data.actor === myId) {
              myAction = env.data.target;
              // The vigilante's bullet is one-shot for the whole game.
              // Recording our own action spends it; latch the flag so the
              // picker stays hidden on later nights even after myAction
              // resets at the start of the next night.
              if (myRole === "vigilante") {
                vigilanteFired = true;
              }
            }
            if (env.data.actor === myId || env.data.faction === "mafia") {
              renderActionPanel();
              renderPlayers();
            }
            break;

          case "spectatorNightAction":
            // Graveyard-only: the server projects this exclusively to DEAD
            // players, so simply receiving it means we're spectating. Append
            // to the live feed (in turn order) and repaint the night banner.
            // Applied on EVERY path including replay, so a dead player who
            // refreshes mid-night rebuilds the whole feed; the feed is
            // cleared at the start of each night (see phaseChanged → night).
            spectatorNightActions.push({
              actor: env.data.actor,
              actorRole: env.data.actorRole,
              target: env.data.target,
              targetRole: env.data.targetRole,
              recruit: !!env.data.recruit,
            });
            renderActionPanel();
            break;

          case "detectiveResult": {
            // Server-side projection (PrivateTo) guarantees only the
            // detective ever receives this event. The modal is a one-shot
            // pacing signal tied to the live moment of investigation
            // (read → "Got it" → night continues), so we don't want it
            // re-popping on every ordinary refresh. But a detective whose
            // phone was locked when the result fired never saw it at all,
            // so blanket replay-suppression would silently swallow their
            // finding. We split the difference on acknowledgement: show
            // live, and on replay only if this specific result hasn't been
            // dismissed yet. The id is per investigation (day + target),
            // since the detective investigates once per night.
            //
            // (day is correct at this point on both paths: the night's
            // PhaseChanged, which sets it, is replayed before this event.)
            const detAckId = `det:${day}:${env.data.target}`;
            if (!replaying || !hasAckedNotice(detAckId)) {
              showDetectiveToast(nameOf(env.data.target), !!env.data.isMafia, detAckId);
            }
            break;
          }

          case "trackerResult": {
            // PrivateTo the tracker — projection guarantees only the
            // tracker receives this. Same one-shot, dismiss-to-remember
            // pacing as the detective result (show live; on replay only if
            // not yet acknowledged). The id is per track (day + target),
            // since the tracker tracks once per night. `visited` is "" when
            // the target took no action — render that as "stayed home". When
            // the target visited US (a player who acted on the tracker, e.g.
            // the mafia or the doctor), render "you" rather than our own name.
            const trkAckId = `track:${day}:${env.data.target}`;
            if (!replaying || !hasAckedNotice(trkAckId)) {
              const visited = env.data.visited;
              showTrackerToast(
                nameOf(env.data.target),
                visited ? (visited === myId ? "you" : nameOf(visited)) : null,
                trkAckId
              );
            }
            break;
          }

          case "blocked":
            // PrivateTo us, delivered at the START of our act window:
            // the Consort blocked us, so our action can't land. Set the
            // flag on EVERY path (incl. replay) so the target picker
            // stays hidden for the turn even after a refresh; pop the
            // modal only live so a reconnect doesn't re-pop a stale one.
            iAmBlocked = true;
            if (!replaying) showBlockedToast();
            renderActionPanel();
            renderPlayers();
            break;

          case "recruited":
            // PrivateTo us: the Yakuza recruited us into the mafia. The engine
            // sends exactly ONE of these per convert, timed by role:
            //   - active role (not blocked): at the start of our (now phantom)
            //     turn — our original power is suppressed tonight;
            //   - active role ALSO blocked by the Consort: at resolution (the
            //     Blocked notice took our turn slot), so we see "distracted"
            //     at our turn and "recruited" at resolution;
            //   - villager (no turn): at resolution.
            // Set the flag on EVERY path so the picker stays hidden after a
            // refresh; our role flips via roleAssigned. Show the toast live,
            // and on replay only if we haven't acknowledged it yet — so a
            // convert whose phone was locked when the recruit fired during
            // the night still learns about it when they reconnect, while an
            // ordinary refresh after they've tapped "Got it" stays quiet.
            // There's exactly one recruit per convert per game, so a single
            // "recruited" id suffices.
            iAmRecruited = true;
            if (!replaying || !hasAckedNotice("recruited")) showRecruitedToast("recruited");
            renderActionPanel();
            renderPlayers();
            break;

          case "recruitRecorded":
            // Faction-scoped: the server sends this to every living mafioso
            // (and the Yakuza) so the WHOLE faction sees the night's action is
            // a recruit — no kill is coming. Clear any stale kill target and
            // record the recruit target for everyone; the UI then shows the
            // recruiting Yakuza its "Recruited: X" confirmation and co-mafia a
            // "the Yakuza is recruiting X" notice, plus a "Recruit" row badge.
            mafiaKillTarget = null;
            mafiaRecruitTarget = env.data.target;
            renderActionPanel();
            renderPlayers();
            break;


          case "consortPromoted":
            // PrivateTo us: we (the Consort) have been elevated to full
            // mafia because the cabal was wiped out. Apply the role
            // change on EVERY path (including replay) so a reconnecting
            // promoted consort knows their new faction and gets the
            // mafia night turn. Pop the announcement modal live, and on
            // replay only if we haven't acknowledged it — so a consort
            // whose phone was locked when the promotion fired still gets
            // the notice on reconnect, while an ordinary refresh after
            // dismissal stays quiet. One promotion per game → one id.
            // The mafiaRoster event that follows badges us as Mafia.
            myRole = "mafia";
            $("my-role").textContent = "mafia";
            // The full-cabal mafiaRoster event that follows in this same batch
            // (and, on rejoin, the StartGame roster too) populates mafiaPeers
            // with her predecessors — the dead original mafia and the dead
            // Yakuza — so a promoted consort sees them badged. We do NOT reset
            // mafiaPeers here: faction knowledge only widens, and the merge in
            // the mafiaRoster handler keeps live and rejoin consistent.
            if (!replaying || !hasAckedNotice("promoted")) showPromotedToast("promoted");
            renderAll();
            break;

          case "rosterRevealed":
            // Graveyard-only: the server sends this exclusively to DEAD
            // players, so its mere arrival means I'm in the graveyard.
            // Fold the full player->role map into each row's revealedRole
            // — the same field gameEnded populates for everyone — so the
            // dead see every identity as an inline role tag, identical to
            // the game-end reveal. Re-emitted on a consort promotion, so
            // upsertPlayer refreshes her role to "mafia" even for players
            // who died before her takeover.
            //
            // Deliberately SILENT — no toast or modal — so the promotion
            // never pops the "You've been promoted" announce for the
            // dead; that stays private to the consort via the separate,
            // untouched consortPromoted event.
            for (const [pid, role] of Object.entries(env.data.roles || {})) {
              upsertPlayer(pid, { revealedRole: role });
            }
            break;

          // Pre-reveal, the server only delivers a voter THEIR OWN
          // cast/change/retract (the events are private), so these keep
          // the local player's "your vote" chip + row highlight in sync
          // without exposing anyone else's choice. The full board
          // arrives later via votesRevealed.
          case "voteCast":
            votes.set(env.data.voter, env.data.target);
            // Casting a real vote supersedes any abstention of ours.
            if (env.data.voter === myId) iAbstained = false;
            renderActionPanel();
            renderPlayers();
            break;

          case "voteChanged":
            votes.set(env.data.voter, env.data.to);
            if (env.data.voter === myId) iAbstained = false;
            renderActionPanel();
            renderPlayers();
            break;

          case "voteRetracted":
            votes.delete(env.data.voter);
            // A retract clears whatever decision we had — vote OR abstention.
            if (env.data.voter === myId) iAbstained = false;
            renderActionPanel();
            renderPlayers();
            break;

          case "voteAbstained":
            // PRIVATE to the abstainer: only ever us. Reflect our own
            // abstention and drop any real vote we had so the two states
            // stay mutually exclusive (mirrors the engine). Other players'
            // abstentions never arrive here — the room learns only the
            // aggregate count via voteProgress.
            if (env.data.voter === myId) {
              iAbstained = true;
              votes.delete(myId);
            }
            renderActionPanel();
            renderPlayers();
            break;

          case "voteProgress":
            // PUBLIC running count of how many living players have voted,
            // emitted alongside every (private) cast/change/retract. This is
            // how non-voters and the dead — who never receive the individual
            // votes — learn voting progress while the tally stays hidden.
            // Only the count moves; the banner shows "N of M voted" (M from
            // the local roster) or "Voting completed" once everyone's in.
            votesCastCount = env.data.cast || 0;
            renderActionPanel();
            break;

          case "votesRevealed": {
            // Host opened the box. Replace our (self-only) view with the
            // authoritative full tally so everyone — including dead
            // players, who receive this Public event too — sees who
            // voted for whom. Voting is now locked client-side; the
            // per-row Vote buttons drop out (renderPlayers keys off
            // votesRevealed) and the host swaps Reveal → Finalize/Clear.
            votesRevealed = true;
            // The full board takes over now; the hidden-window progress
            // count and our own abstention indicator are no longer shown.
            votesCastCount = 0;
            iAbstained = false;
            votes = new Map();
            const tally = env.data.tally || {};
            for (const [voter, target] of Object.entries(tally)) {
              votes.set(voter, target);
            }
            renderActionPanel();
            renderPlayers();
            if (!replaying) {
              narrate("Votes revealed.");
            }
            break;
          }

          case "voteCleared":
            votes = new Map();
            votesRevealed = false;
            votesCastCount = 0;
            iAbstained = false;
            renderActionPanel();
            renderPlayers();
            if (!replaying) {
              narrate("Votes cleared. Cast again.");
            }
            break;

          case "gameEnded":
            winner = env.data.winner || null;
            if (env.data.finalRoles) {
              for (const [pid, role] of Object.entries(env.data.finalRoles)) {
                upsertPlayer(pid, { revealedRole: role });
              }
            }
            renderActionPanel();
            if (!replaying) {
              narrate(
                winner === "town"
                  ? "Town wins. Roles will now be revealed."
                  : winner === "mafia"
                    ? "Mafia wins. Roles will now be revealed."
                    : "Game over.",
                { pauseBefore: 300 },
              );
            }
            break;

          case "gameReset": {
            // The host started a new game in the same room. GameReset is a
            // self-contained lobby snapshot (the server has dropped the
            // previous game's events), so we wipe ALL derived state and
            // rebuild the lobby from this one event — exactly the join
            // path's reset, minus the full replay. resetGameState() nulls
            // hostId; the HostChanged that the server appends right after
            // this event re-establishes it (and myIsHost). It also clears
            // myRole and the "you are mafia" display.
            resetGameState();
            // Drop one-shot notice acks from the previous game: the new
            // game reuses this room code and player id, and its notice ids
            // (recruited / promoted / det:<day>:<target>) can collide with
            // the old game's, so a stale ack would wrongly suppress a fresh
            // notice on replay.
            clearAckedNotices();
            lobbyMinPlayers = env.data.minPlayers;
            lobbyMaxPlayers = env.data.maxPlayers;
            mafiaCount = env.data.mafiaCount;
            for (const p of (env.data.players || [])) {
              upsertPlayer(p.playerId, { name: p.name, alive: true });
            }
            if (!replaying) {
              narrate("A new game is starting. Everyone back to the lobby.", {
                pauseBefore: 300,
              });
            }
            break;
          }
          default:
            // Forward-compatible by design: an unknown event tag (e.g. a
            // newer server emitting an event type this client predates) is
            // ignored rather than throwing, so a rolling deploy can't desync
            // or crash an older client. We note it for dev visibility.
            if (typeof console !== "undefined" && console.warn) {
              console.warn(`ignoring unknown event type: ${env.type}`);
            }
        }
      }

      // --- Narrator scripts -------------------------------------------

      function narratePhaseChange(from, to, dayNum) {
        if (to === "night") {
          // "City, go to sleep." used to fire here. It now rides on
          // the engine's nightOpeningStarted sub-phase event so that
          // the audio cadence is server-driven and tracked by the
          // same timer that gates the rest of the night. See the
          // nightOpeningStarted handler in handleEvent.
          // (Nothing to speak on the phase boundary itself.)
        } else if (to === "day_discussion") {
          // day_discussion is entered from TWO places:
          //   1. night → day_discussion: morning narration after the
          //      night resolves ("Everybody, wake up. Last night, X
          //      was killed." / "...nobody died.").
          //   2. day_vote → day_discussion: a lynch was finalized.
          //      The room already knows what happened (PlayerLynched
          //      event), the host announces it verbally, and a
          //      narrator line here would be redundant or — worse —
          //      wrong (e.g. announcing "nobody died last night"
          //      after a lynch). So we narrate ONLY when arriving
          //      from night.
          if (from === "night") {
            const dead = dayDiscussionPendingDeaths;
            dayDiscussionPendingDeaths = [];
            // Slight pause before the wake-up cue so any "doctor,
            // go to sleep" queued before this one finishes first.
            narrate("Everybody, wake up.", { pauseBefore: 200 });
            if (dead.length > 0) {
              const names = formatVictimList(dead);
              const verb = dead.length === 1 ? "was" : "were";
              narrate(`Last night, ${names} ${verb} killed.`, { pauseBefore: 1800 });
            } else {
              narrate("Last night, nobody died.", { pauseBefore: 1800 });
            }
          }
        } else if (to === "day_vote") {
          narrate("Time to vote.");
        }
      }

      // --- Role narration lookup -----------------------------------------
      //
      // Adding a new role? Add a row to each of ROLE_NARRATION (wake
      // cue) and ROLE_SLEEP (sleep cue). The matching engine-side
      // duration lives in internal/game/rolespec.go's roleSpec.Narrate
      // / Sleep function fields. Keep these two surfaces in sync: the
      // narration TEXT is owned by the client (so we can edit copy
      // without touching the server), the narration DURATION is owned
      // by the engine (so the server's timer pacing matches the
      // spoken cue's length).
      //
      // Entries support per-day variants. The lookup tries `day${N}`
      // first, then falls back to `default`. Mafia gets a longer Day 0
      // line ("Look around and recognize each other") because that
      // first night they need to identify each other; subsequent
      // nights skip straight to the target prompt.
      //
      // If a role arrives that isn't in the table — e.g. a new role
      // shipped in a server-only update before the client was
      // redeployed — we fall back to a generic "${role}, wake up."
      // line and log a warning. That keeps the in-person game
      // playable while the missing entry gets added.
      //
      // COUPLING WARNING: each text entry below has a matching
      // duration in internal/room/config.go — universal narrate/
      // sleep timing lives at DefaultNarrateDuration /
      // DefaultSleepDuration, and per-role variants (mafia Day 0,
      // future role overrides) live at DefaultNarrate /
      // DefaultSleep. If you make a cue longer (more words,
      // slower cadence), bump the matching duration there or the
      // engine will advance to the next sub-phase mid-sentence.
      // Test on the slowest TTS voice you support (iOS Safari
      // "Samantha" is a good worst-case anchor) and add ~500ms
      // slop.
      const ROLE_NARRATION = {
        mafia: {
          day0:    "Mafia, wake up. Look around and recognize each other. Then choose someone to kill.",
          default: "Mafia, wake up. Choose someone to kill.",
        },
        consort: {
          default: "Consort, wake up. Choose someone to distract.",
        },
        detective: {
          default: "Detective, wake up. Choose someone to investigate.",
        },
        doctor: {
          default: "Doctor, wake up. Choose someone to save.",
        },
        vigilante: {
          default: "Vigilante, wake up. Choose someone to eliminate.",
        },
        tracker: {
          default: "Tracker, wake up. Choose someone to track.",
        },
      };

      const ROLE_SLEEP = {
        mafia:     "Mafia, go to sleep.",
        consort:   "Consort, go to sleep.",
        detective: "Detective, go to sleep.",
        doctor:    "Doctor, go to sleep.",
        vigilante: "Vigilante, go to sleep.",
        tracker:   "Tracker, go to sleep.",
      };

      function lookupRoleNarration(role, dayNum) {
        const entry = ROLE_NARRATION[role];
        if (!entry) {
          console.warn(`narration: no wake-up cue for role "${role}" — using generic fallback`);
          return `${capitalize(role)}, wake up.`;
        }
        const dayKey = `day${dayNum}`;
        return entry[dayKey] || entry.default || `${capitalize(role)}, wake up.`;
      }

      function lookupRoleSleep(role) {
        const text = ROLE_SLEEP[role];
        if (!text) {
          console.warn(`narration: no sleep cue for role "${role}" — using generic fallback`);
          return `${capitalize(role)}, go to sleep.`;
        }
        return text;
      }

      // Cues are fired the moment the matching sub-phase event lands;
      // the engine's sub-phase timer (set per role in rolespec.go) is
      // long enough for the utterance to finish before the next
      // transition. No client-side pauseBefore scheduling — the
      // server owns the cadence.
      // The opening cue is short (~1.5s) but the sub-phase intentionally
      // runs longer (DefaultOpeningDuration in internal/room/config.go,
      // currently 7s) so the room has time to actually close their
      // eyes and settle before mafia narrate begins. If you change
      // this string to something substantially longer, bump
      // DefaultOpeningDuration too.
      function narrateNightOpening() {
        narrate("City, go to sleep.");
      }

      function narrateRoleWake(role, dayNum) {
        if (!role) return;
        narrate(lookupRoleNarration(role, dayNum));
      }

      function narrateRoleSleep(role) {
        if (!role) return;
        narrate(lookupRoleSleep(role));
      }

      function connect(code, name, creds) {
        currentRoomCode = code;
        // Neutralize any prior socket so its late onclose/onerror can't
        // race this fresh attempt (e.g. fire a redundant reconnect).
        if (ws) {
          ws.onopen = ws.onmessage = ws.onclose = ws.onerror = null;
          try { ws.close(); } catch {}
        }
        // On a reconnect we carry our resume cursor so the server replies
        // with only the events we missed. lastSeq is 0 on a page-load
        // auto-rejoin (fresh JS state), which correctly requests a full
        // snapshot.
        const params = creds
          ? `?playerId=${encodeURIComponent(creds.playerId)}&secret=${encodeURIComponent(creds.secret)}&since=${lastSeq}`
          : "";
        const url = `${location.origin.replace("http", "ws")}/ws/${code}${params}`;
        ws = new WebSocket(url);

        ws.onopen = () => {
          setStatus(`connected to ${code}`, "text-emerald-400");
          // Only the fresh-join path needs to send a join frame; rejoin
          // is handled server-side via the URL params.
          if (!creds) {
            pendingJoinCode = code;
            send("join", { name });
          }
        };
        ws.onmessage = (ev) => {
          try { handleServerMessage(JSON.parse(ev.data)); }
          catch (e) { showError(`Failed to parse server message: ${e}`); }
        };
        ws.onclose = () => {
          // If we were mid-PAGE-LOAD-auto-rejoin and the socket died
          // before any frame (server down, room gone), surface the
          // lobby instead of leaving the user looking at an empty
          // in-game view they can't escape. The close is opaque — a
          // reaped room 404s the handshake (no auth_failed frame ever
          // arrives) and looks identical to a transient outage — so
          // recoverFromFailedRejoin probes the room to disambiguate and
          // either clears the dead creds + offers Create, or keeps the
          // "Join room CODE" view for a retry. (The in-game reconnect
          // loop uses the `reconnecting` flag, NOT pendingRejoinCode, so
          // it falls through to the auto-reconnect branch below.)
          if (pendingRejoinCode) {
            const stale = pendingRejoinCode;
            pendingRejoinCode = null;
            recoverFromFailedRejoin(stale);
            return;
          }
          // Symmetric guard for first-time joins: socket died before
          // the server acked. Most likely the server is unreachable
          // (the alternative — an "error" frame — is handled above and
          // detaches this handler before close fires).
          if (pendingJoinCode) {
            const stale = pendingJoinCode;
            pendingJoinCode = null;
            recoverToLobby(stale, "Could not connect — the server may be unreachable.");
            return;
          }
          // An established in-game connection dropped (network blip,
          // idle proxy/NAT cull, or a mobile tab the OS suspended).
          // We have an identity and stored creds, so heal it: retry
          // with backoff until we're back. The server replays our
          // full state on rejoin, so the player just sees a brief
          // "Reconnecting…" and then continues.
          if (myId && currentRoomCode) {
            scheduleReconnect();
            return;
          }
          setStatus("disconnected", "text-rose-400");
        };
        ws.onerror = () => setStatus("connection error", "text-rose-400");
      }

