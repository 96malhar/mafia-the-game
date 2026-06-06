      // ----- action panel (per-phase) -------------------------------------

      // Phase theming. Each phase gets a distinct color so a glance is
      // enough to know what's happening. Lobby is neutral; Night is
      // deep indigo (sleeping); Day Discussion is amber (sunlight);
      // Day Vote is rose (tense); Ended depends on the winner.
      const PHASE_THEMES = {
        lobby:           "border-slate-700 bg-slate-800/40 text-slate-100",
        night:           "border-indigo-800 bg-indigo-950/40 text-indigo-100",
        day_discussion:  "border-amber-700/60 bg-amber-900/20 text-amber-100",
        day_vote:        "border-rose-700/60 bg-rose-900/20 text-rose-100",
        ended:           "border-emerald-700/60 bg-emerald-900/20 text-emerald-100",
      };

      function renderActionPanel() {
        const panel = $("action-panel");
        const label = $("phase-label");
        const headline = $("phase-headline");
        const dayEl = $("phase-day");
        const hint = $("phase-hint");
        const extras = $("action-extras");
        const banner = $("night-banner");
        const bannerText = $("night-banner-text");

        panel.className =
          "rounded border p-4 transition-colors duration-300 " +
          (PHASE_THEMES[phase] || PHASE_THEMES.lobby);

        // Hide the eyebrow in the lobby: its headline is just "Lobby" too,
        // so showing the phase name above it reads as a redundant repeat.
        label.textContent = phase.replace("_", " ");
        label.classList.toggle("hidden", phase === "lobby");
        dayEl.textContent = phase === "lobby" || phase === "ended" ? "" : `Day ${day}`;
        extras.innerHTML = "";

        const me = players.get(myId);
        const iAmAlive = !!(me && me.alive);
        const total = players.size;

        // Show the night banner only when we're in Night AND a turn is
        // active. Between turns or outside Night, hide it so the panel
        // doesn't display stale countdowns.
        //
        // The countdown number + draining bar are nested inside the
        // banner but are FURTHER gated by viewerOwnsCurrentTimer: only
        // the actor sees them, and only during the "act" sub-phase
        // when the seconds-remaining is actually actionable. During
        // narrate / ponder / sleep / settle the actor is just
        // listening or waiting — a draining bar would falsely
        // suggest urgency. Showing the timer to spectators in any
        // sub-phase would also leak deliberation timing.
        //
        // The banner HEADING (text) is broader: it reflects whose
        // role-turn is in flight across all of that role's
        // sub-phases, so "Your turn — Mafia" stays visible from
        // narrate through settle for the mafia, giving them context
        // even when they can't yet click.
        const countdownEl = $("night-banner-countdown");
        const barWrapEl = $("night-banner-bar-wrap");
        if (phase === "night" && currentNightRole) {
          banner.classList.remove("hidden");
          const display = capitalize(currentNightRole);
          // The yakuza acts within the Mafia turn (no turn of its own), so it
          // is "playing" when the mafia is up. myNightTurnActive() captures
          // that exception.
          const youArePlayingThisRole =
            myNightTurnActive() && !!(me && me.alive);
          if (youArePlayingThisRole) {
            const pickPhrase =
              myRole === "consort"
                ? "Pick someone to distract"
                : myRole === "vigilante"
                  ? "Pick someone to eliminate"
                  : myRole === "tracker"
                    ? "Pick someone to track"
                    : myRole === "yakuza"
                      ? "Pick someone to kill, or Recruit them"
                      : "Pick a target";
            if (mafiaRecruitTarget) {
              // A recruit is locked — the faction kills no one tonight. The
              // recruiting Yakuza sees it as its own action; co-mafia see who
              // the Yakuza is converting.
              bannerText.textContent =
                myRole === "yakuza"
                  ? `Your turn — ${display}. Recruit locked on ${nameOf(mafiaRecruitTarget)}.`
                  : `Your turn — ${display}. The Yakuza is recruiting ${nameOf(mafiaRecruitTarget)} — no kill tonight.`;
            } else if (iAmRecruited) {
              // The Yakuza recruited us: power suppressed this night.
              // Checked before iAmBlocked (a recruit overrides a block).
              bannerText.textContent =
                `Your turn — ${display}. The Yakuza recruited you — your power is gone tonight.`;
            } else if (iAmBlocked) {
              // Roleblocked tonight: the server runs this as a phantom
              // turn (no act window), so the notice stands for the whole
              // turn. Checked before the spent-vigilante branch so a
              // blocked vigilante (bullet preserved) isn't mislabeled
              // "spent".
              bannerText.textContent =
                `Your turn — ${display}. You were distracted tonight — your action won't land.`;
            } else if (myRole === "vigilante" && currentNightTurnPhantom) {
              // Bullet already spent on an earlier night. The server runs
              // a spent vigilante's turn as a phantom (no act window), so
              // tell him outright rather than the generic "Listen to the
              // moderator…". We key off the server's phantom flag (not the
              // local vigilanteFired) so the notice is correct even after a
              // mid-game refresh clears local state.
              bannerText.textContent =
                `Your turn — ${display}. Your bullet is spent — nothing to do tonight.`;
            } else if (myRole === "vigilante" && heldFireThisTurn) {
              // We chose to hold fire this turn (an explicit NightPass).
              // The bullet is preserved; the turn ends early.
              bannerText.textContent =
                `Your turn — ${display}. You held your fire — bullet saved.`;
            } else {
              bannerText.textContent =
                currentNightSubPhase === "act"
                  ? `Your turn — ${display}. ${pickPhrase} on the Players panel below.`
                  : `Your turn — ${display}. Listen to the moderator…`;
            }
          } else {
            bannerText.textContent = `${display} is choosing. Eyes closed.`;
          }
          if (viewerOwnsCurrentTimer()) {
            countdownEl.classList.remove("hidden");
            barWrapEl.classList.remove("hidden");
          } else {
            countdownEl.classList.add("hidden");
            barWrapEl.classList.add("hidden");
          }
        } else {
          banner.classList.add("hidden");
          countdownEl.classList.add("hidden");
          barWrapEl.classList.add("hidden");
        }

        switch (phase) {
          case "lobby": {
            headline.textContent = "Lobby";
            // A valid game needs: total in [min, max] AND mafia in
            // [1, total - 3]. The "3" mirrors the engine's
            // `reservedTownRoles + 1` (Det + Doc + ≥1 villager) in
            // internal/game/rules.go — keep them in sync. The
            // server is authoritative for both predicates; this
            // client-side check only drives the Start button's
            // enabled/disabled state and the hint text.
            const configReady =
              lobbyMinPlayers !== null && lobbyMaxPlayers !== null && mafiaCount !== null;

            const minMafia = 1;
            // Each enabled optional role (Consort, Vigilante) takes a
            // villager slot, so it tightens the mafia cap once more than
            // one is on. The engine enforces these bounds (see
            // applyStartGame in internal/game/rules.go — keep them in sync):
            //   mafia <= total - 3 - (#optional roles)   (Det + Doc +
            //       every optional + ≥1 plain villager; each optional takes
            //       a villager slot and the roster must keep one villager)
            //   2*mafia + (#mafia-aligned optionals) < total
            //       (don't OPEN at the mafia parity win — checkWin ends the
            //        game for the mafia at strictMafia >= town, where town ==
            //        total - mafia - #mafia-aligned optionals; a roster that
            //        opens at parity would hand the mafia an instant Night-1
            //        win. Only the Consort is a mafia-aligned optional.)
            const optionalRoles =
              (consortEnabled ? 1 : 0) +
              (vigilanteEnabled ? 1 : 0) +
              (yakuzaEnabled ? 1 : 0) +
              (trackerEnabled ? 1 : 0);
            // The Consort and the Yakuza are the mafia-aligned optionals (the
            // Vigilante and Tracker are town), so they shrink the town count in
            // the parity test. Keep in sync with applyStartGame's parity guard.
            const mafiaAlignedOptionals =
              (consortEnabled ? 1 : 0) + (yakuzaEnabled ? 1 : 0);
            // Det + Doc + every optional + at least one plain villager.
            const slotCap = total - 3 - optionalRoles;
            // 2*mafia + mafiaAlignedOptionals < total
            //   ⇒ mafia <= floor((total - mafiaAlignedOptionals - 1) / 2)
            const parityCap = Math.floor((total - mafiaAlignedOptionals - 1) / 2);
            const maxMafiaNow = Math.max(0, Math.min(slotCap, parityCap));
            const tooFew = configReady && total < lobbyMinPlayers;
            const tooMany = configReady && total > lobbyMaxPlayers;
            const mafiaOK = configReady && mafiaCount >= minMafia && mafiaCount <= maxMafiaNow;
            const canStart = configReady && !tooFew && !tooMany && mafiaOK;

            if (!configReady) {
              // Sub-second window between WS open and the first
              // gameCreated event. Don't pretend to know the bounds.
              hint.textContent = "Connecting…";
            } else if (tooFew) {
              const need = lobbyMinPlayers - total;
              hint.textContent =
                `Waiting for ${need} more player${need === 1 ? "" : "s"} ` +
                `(min ${lobbyMinPlayers}, currently ${total}).`;
            } else if (tooMany) {
              hint.textContent = `Too many players (${total} > ${lobbyMaxPlayers}).`;
            } else if (maxMafiaNow < minMafia) {
              // The valid mafia range is empty: the enabled roles already
              // claim every seat (no mafia count works). Don't blame the
              // mafia picker — point at the real lever.
              hint.textContent =
                `Too many special roles for ${total} players. ` +
                `Turn off an optional role or add more players.`;
            } else if (!mafiaOK) {
              hint.textContent =
                `Mafia count ${mafiaCount} is invalid for ${total} players ` +
                `— must be between ${minMafia} and ${maxMafiaNow}.`;
            } else {
              hint.textContent = "Ready to start.";
            }

            // Mafia-count picker: host-only, lobby-only, AND only
            // before roles are dealt. The engine rejects setMafia
            // once StartGame has run (see applySetMafiaCount), so
            // the picker would be a UI lie at that point — the
            // host clicks +/− and nothing happens because
            // composeRoster has already committed. Hide it the
            // moment that boundary is crossed.
            if (!rolesDealt) {
              const mafiaPicker = renderMafiaPicker();
              if (mafiaPicker) extras.appendChild(mafiaPicker);
              extras.appendChild(renderConsortToggle());
              extras.appendChild(renderVigilanteToggle());
              extras.appendChild(renderYakuzaToggle());
              extras.appendChild(renderTrackerToggle());
            }

            if (myIsHost) {
              // Audio enablement now lives in the persistent host
              // audio bar above the action panel (#host-audio-bar)
              // so it's reachable from every phase, including after
              // a mid-game refresh. We deliberately don't duplicate
              // it here.
              if (!rolesDealt) {
                if (canStart) {
                  extras.appendChild(actionButton("Start game", "bg-emerald-600 hover:bg-emerald-500",
                                                  () => send("startGame")));
                } else {
                  extras.appendChild(disabledNote("Start unlocks once the lobby + roles are valid."));
                }
              } else {
                // Roles dealt; show "Begin night" so everyone can
                // peek at their role before the first night begins.
                headline.textContent = "Roles dealt";
                hint.textContent = "Everyone, check your role privately. When ready, the host begins the night.";
                extras.appendChild(actionButton("Begin night", "bg-indigo-600 hover:bg-indigo-500",
                                                () => send("beginNight")));
              }
            } else {
              if (!rolesDealt) {
                extras.appendChild(disabledNote("Only the host can start the game."));
              } else {
                hint.textContent = "You've been dealt your role. Waiting for the host to begin the night.";
              }
            }
            break;
          }

          case "night": {
            headline.textContent = "Night falls";
            if (!iAmAlive) {
              hint.textContent = "You're out. Watch the night unfold.";
              // Spectator feed: the dead receive a graveyard-only
              // SpectatorNightAction for each submitted action, so render
              // them in turn order with the actor's ROLE-SPECIFIC verb —
              // "Bob (mafia) killed Diana (villager)", "Hannah (consort)
              // distracted Fiona (detective)", "George (doctor) saved …".
              // The past-tense verb comes from the shared ROLE_VERBS table
              // (lowercased mid-sentence); an unknown role falls back to
              // "targeted". Empty until the first role acts; cleared at the
              // start of each night (see spectatorNightActions). A self-target
              // (e.g. a doctor saving themselves) collapses to "… saved
              // themselves" instead of repeating the name on both sides.
              for (const a of spectatorNightActions) {
                // A Yakuza recruit reads "recruited" regardless of the actor's
                // verb table entry (its kill and recruit share the role tag).
                const verb = a.recruit
                  ? "recruited"
                  : (ROLE_VERBS[a.actorRole]?.past ?? "Targeted").toLowerCase();
                const actorLabel = `${nameOf(a.actor)} (${a.actorRole})`;
                const objectLabel =
                  a.actor === a.target
                    ? "themselves"
                    : `${nameOf(a.target)} (${a.targetRole})`;
                extras.appendChild(
                  noteChip(`${actorLabel} ${verb} ${objectLabel}`, "bg-indigo-800/60"),
                );
              }
              break;
            }
            // Hint text is driven by the strict turn order: only act
            // when the current turn matches your role; otherwise sit
            // still (eyes closed, in the in-person flow).
            if (!currentNightRole) {
              hint.textContent = "Eyes closed. Waiting for the moderator audio…";
            } else if (myNightTurnActive()) {
              if (mafiaRecruitTarget) {
                // A recruit is locked, so the faction kills no one tonight.
                // The recruiting Yakuza sees it as its own action (and will
                // sacrifice itself at daybreak); co-mafia see who the Yakuza
                // is converting. Either way a chip names the target.
                if (myRole === "yakuza") {
                  hint.textContent = "Recruit locked. The family kills no one tonight.";
                } else {
                  hint.textContent =
                    "The Yakuza is recruiting — the family kills no one tonight.";
                }
                extras.appendChild(noteChip(`Recruit: ${nameOf(mafiaRecruitTarget)}`, "bg-amber-700/60"));
              } else if (myAction) {
                hint.textContent = "Submitted. The next role will be summoned shortly.";
                // Past-tense verb from the shared ROLE_VERBS table (keeps
                // the chip in lockstep with the row button); unknown roles
                // fall back to the generic "Targeted".
                const verb = ROLE_VERBS[myRole]?.past ?? "Targeted";
                extras.appendChild(noteChip(`${verb}: ${nameOf(myAction)}`, "bg-amber-700/60"));
              } else if (iAmRecruited) {
                // The Yakuza recruited us: our turn is phantom and our power
                // is suppressed tonight (we flip to mafia at daybreak).
                // Checked before iAmBlocked since a recruit overrides a block.
                hint.textContent =
                  "The Yakuza recruited you — your power is gone tonight. Sit tight.";
              } else if (iAmBlocked) {
                // Roleblocked tonight: the turn is phantom (no act
                // window), so we show this regardless of sub-phase.
                // Checked before the spent-vigilante branch so a blocked
                // vigilante (bullet preserved) isn't mislabeled "spent".
                hint.textContent =
                  "You were distracted tonight — your action won't land. Sit tight.";
              } else if (
                myRole === "vigilante" &&
                (vigilanteFired || currentNightTurnPhantom)
              ) {
                // Bullet already spent on an earlier night — the server
                // runs this as a phantom turn (no act window), so explain
                // why there's nothing to do. We honor the server's phantom
                // flag in addition to the local vigilanteFired so the
                // notice survives a mid-game refresh (which clears the
                // local flag). The earlier myAction guard prevents this
                // from firing on the night the shot was actually taken.
                hint.textContent =
                  "Your bullet is spent. Stay still until the moderator moves on.";
              } else if (iAmMafiaFaction() && mafiaKillTarget) {
                // A co-mafioso locked the kill before us (the first
                // submission ends the act window for the whole faction).
                // The Yakuza shares this turn, so it sees the locked kill too.
                // Show the team's target so we're not left staring at a
                // dead picker wondering what happened.
                hint.textContent = "A teammate locked in the kill for the family.";
                extras.appendChild(noteChip(`Team target: ${nameOf(mafiaKillTarget)}`, "bg-rose-800/70"));
              } else if (myRole === "vigilante" && heldFireThisTurn) {
                // We pressed "Hold fire" — an explicit NightPass that ends
                // the turn early WITHOUT spending the bullet. Confirm it
                // and keep the picker hidden until the moderator moves on.
                // Set optimistically on click and held until the next
                // turn's narration, so it spans the brief act moment after
                // the click AND the ponder the server advances us into.
                hint.textContent =
                  "You held your fire — your bullet is saved for a later night.";
              } else if (currentNightSubPhase !== "act") {
                // We're in narrate / ponder / sleep / settle (or it's
                // the global opening). The action window hasn't opened
                // yet (or has already closed). Keep the actor listening.
                hint.textContent = "Listen to the moderator…";
              } else {
                hint.textContent =
                  myRole === "consort"
                    ? "Pick someone to distract on the Players panel below."
                    : myRole === "vigilante"
                      ? "Pick a target below — or hold your fire to save your bullet."
                      : myRole === "yakuza"
                        ? "Pick someone to kill — or Recruit them into the family (you'll sacrifice yourself)."
                        : "Pick your target on the Players panel below.";
                if (myRole === "vigilante") {
                  // Explicit "decline to act" affordance. Holding fire
                  // keeps the one bullet AND ends the 60s act window early
                  // (the server treats NightPass like a fast timeout).
                  extras.appendChild(
                    actionButton(
                      "Hold fire (save bullet)",
                      "bg-slate-700 hover:bg-slate-600",
                      () => {
                        // Optimistic local flag → the picker and this
                        // button vanish and the confirmation shows at
                        // once, before the server's ponder event lands.
                        heldFireThisTurn = true;
                        send("nightPass");
                        renderAll();
                      },
                    ),
                  );
                }
              }
            } else if (canActAtNight()) {
              hint.textContent =
                `${capitalize(currentNightRole)} is acting now. Your turn comes next (or later).`;
            } else {
              hint.textContent = "Eyes closed. The active role acts in secret.";
            }
            break;
          }

          case "day_discussion": {
            if (dayLynchResolved) {
              headline.textContent = "Day ends";
              if (iAmAlive) {
                hint.textContent = myIsHost
                  ? "When the room is ready, begin the next night."
                  : "The vote is in. Waiting for the host to begin the night.";
              } else {
                hint.textContent = "You're out. Waiting for the next night.";
              }
              if (myIsHost) {
                extras.appendChild(actionButton(
                  "Begin night",
                  "bg-indigo-600 hover:bg-indigo-500",
                  () => send("beginNight"),
                ));
              }
            } else {
              headline.textContent = "Daybreak — discuss";
              if (iAmAlive) {
                hint.textContent = myIsHost
                  ? "Let the room discuss, then open voting when they're ready."
                  : "Talk it out (offline / voice). Voting opens when the host says so.";
              } else {
                hint.textContent = "You're out. The living debate without you.";
              }
              // Surface last night's outcome as a chip alongside
              // the discussion controls. The chip persists across
              // renders + refreshes (lastNightVictims is rebuilt
              // from the event log), unlike the dawn narration
              // which fires once. This daybreak branch
              // (dayLynchResolved === false) is only ever reached
              // coming from night, so an empty list here means the
              // night was genuinely quiet — show a neutral
              // "nobody died" chip to mirror the dawn narration.
              // A doctor save also lands here as "nobody died",
              // which is intentional: the save is private to the
              // doctor, so the town must not be able to tell a save
              // apart from a night with no attack at all.
              if (lastNightVictims.length > 0) {
                const names = formatVictimList(lastNightVictims);
                const verb = lastNightVictims.length === 1 ? "was" : "were";
                extras.appendChild(noteChip(
                  `Last night: ${names} ${verb} killed`,
                  "bg-rose-700/60",
                ));
              } else {
                extras.appendChild(noteChip(
                  "Last night: nobody died",
                  "bg-slate-700/60",
                ));
              }
              if (myIsHost) {
                extras.appendChild(actionButton(
                  "Open voting",
                  "bg-rose-600 hover:bg-rose-500",
                  () => send("openVoting"),
                ));
              }
            }
            break;
          }

          case "day_vote": {
            headline.textContent = votesRevealed ? "Votes revealed" : "Vote";
            if (votesRevealed) {
              // Surface the finalize outcome to the WHOLE room (incl. the
              // dead) before the host commits: name who will be lynched,
              // or state that nobody will. projectedLynch mirrors the
              // server's strict-majority rule, so this preview matches
              // what finalize actually does.
              const target = projectedLynch();
              const outcome = target
                ? `${target.name} has a majority and will be lynched once the host finalizes.`
                : "No one has a majority, so nobody will be lynched once the host finalizes.";
              hint.textContent = "Votes are locked and shown below.";
              // Render the projected outcome on its own line so it reads
              // as the headline consequence rather than trailing prose. A
              // pending lynch is tinted rose; "no lynch" stays neutral.
              const outcomeLine = document.createElement("span");
              outcomeLine.className = `mt-1 block font-medium ${target ? "text-rose-300" : "text-slate-300"}`;
              outcomeLine.textContent = outcome;
              hint.appendChild(outcomeLine);
            } else if (!iAmAlive) {
              hint.textContent = "You're out. You may not vote.";
            } else {
              const myVote = votes.get(myId);
              hint.textContent = myVote
                ? "Vote locked in. Tap another player to change, or tap your current selection to retract. Counts stay hidden until the host reveals."
                : "Pick a player to lynch on the Players panel below. Counts stay hidden until the host reveals.";
              if (myVote) {
                // The "Your vote: X" chip stays as a one-glance
                // summary; the retract action moved onto the
                // selected player's row itself (tap-to-undo).
                extras.appendChild(noteChip(`Your vote: ${nameOf(myVote)}`, "bg-rose-700/60"));
              }
            }
            // Host-only vote-management buttons. The flow is two-step:
            // while hidden the host only gets "Reveal votes"; once
            // revealed they get Finalize (closes the day — lynches the
            // target only if it reached a >50% majority, otherwise no
            // one dies and play moves to the next night) and Clear
            // (wipes the board and reopens hidden voting).
            if (myIsHost) {
              if (!votesRevealed) {
                extras.appendChild(actionButton(
                  "Reveal votes",
                  "bg-rose-600 hover:bg-rose-500",
                  () => send("revealVotes"),
                ));
              } else {
                extras.appendChild(actionButton(
                  "Finalize votes",
                  "bg-emerald-600 hover:bg-emerald-500 disabled:opacity-50",
                  () => send("finalizeVotes"),
                ));
                extras.appendChild(actionButton(
                  "Clear & re-vote",
                  "bg-amber-600 hover:bg-amber-500 disabled:opacity-50",
                  () => send("clearVotes"),
                ));
              }
            }
            break;
          }

          case "ended": {
            headline.textContent = winner
              ? `${capitalize(winner)} wins`
              : "Game over";
            // The host can restart in-place: a new game in the SAME room,
            // keeping every player, returning to the lobby so optional
            // roles can be re-toggled and new players can join before the
            // next deal. Non-hosts just wait for the host to do so.
            if (myIsHost) {
              hint.textContent = "Roles below are revealed. Start a new game with the same players, or share the room link to add more.";
              extras.appendChild(actionButton(
                "Start new game",
                "bg-emerald-600 hover:bg-emerald-500",
                () => send("resetGame"),
              ));
            } else {
              hint.textContent = "Roles below are revealed. Waiting for the host to start a new game.";
            }
            break;
          }

          default:
            headline.textContent = phase;
            hint.textContent = "";
        }
      }

      // Buttons use min-h-[44px] to clear the iOS Human Interface
      // Guidelines minimum tap target. Tailwind doesn't ship a 44px
      // utility out of the box; the arbitrary value works fine and
      // is more honest about the constraint than rounding to 40px.
      function actionButton(label, klass, onClick) {
        const b = document.createElement("button");
        b.className = `inline-flex min-h-[44px] items-center justify-center rounded px-4 py-2 text-sm font-medium text-white ${klass}`;
        b.textContent = label;
        b.addEventListener("click", onClick);
        return b;
      }

      // renderMafiaPicker builds the host's lobby-only control for the
      // planned mafia count. Non-hosts see the same value as a read-only
      // chip so everyone has shared visibility into the configured
      // composition.
      //
      // Bounds:
      //   minimum:  1 (engine requires ≥1 mafia)
      //   maximum:  MaxPlayers - 3 (Det + Doc + ≥1 villager reserved).
      //             Mirrors `reservedTownRoles + 1` in
      //             internal/game/rules.go — keep them in sync.
      //             The "is this valid for the CURRENT player count?"
      //             check lives in the calling switch arm — here we
      //             only enforce the absolute lobby bound so the host
      //             can pre-tune before everyone has joined.
      //
      // Returns null if the lobby config (lobbyMaxPlayers / mafiaCount)
      // hasn't been delivered by the server yet — the caller then
      // simply omits the picker rather than rendering "Mafia null".
      function renderMafiaPicker() {
        if (mafiaCount === null || lobbyMaxPlayers === null) {
          return null;
        }

        const wrapper = document.createElement("div");
        wrapper.className =
          "flex items-center gap-2 rounded bg-slate-700/60 px-3 py-1.5 text-xs";

        const label = document.createElement("span");
        label.className = "uppercase tracking-wide opacity-70";
        label.textContent = "Mafia";
        wrapper.appendChild(label);

        if (!myIsHost) {
          const val = document.createElement("span");
          val.className = "font-mono text-sm";
          val.textContent = String(mafiaCount);
          wrapper.appendChild(val);
          return wrapper;
        }

        const minMafia = 1;
        const maxMafia = Math.max(1, lobbyMaxPlayers - 3);

        // Stepper buttons need real tap targets. Square 36px buttons
        // line up visually with the label and read clearly on phones.
        const stepBtnClass =
          "inline-flex h-9 w-9 items-center justify-center rounded bg-slate-600 text-base font-semibold hover:bg-slate-500 disabled:cursor-not-allowed disabled:opacity-40";

        const minus = document.createElement("button");
        minus.className = stepBtnClass;
        minus.textContent = "−";
        minus.disabled = mafiaCount <= minMafia;
        minus.addEventListener("click", () => send("setMafia", { count: mafiaCount - 1 }));

        const val = document.createElement("span");
        val.className = "min-w-[1.5rem] text-center font-mono text-base";
        val.textContent = String(mafiaCount);

        const plus = document.createElement("button");
        plus.className = stepBtnClass;
        plus.textContent = "+";
        plus.disabled = mafiaCount >= maxMafia;
        plus.addEventListener("click", () => send("setMafia", { count: mafiaCount + 1 }));

        wrapper.appendChild(minus);
        wrapper.appendChild(val);
        wrapper.appendChild(plus);
        return wrapper;
      }

      // renderOptionalRoleToggle builds a lobby control for an optional
      // role. Host sees an on/off toggle (sends `msgType` with
      // {enabled}); everyone else sees a read-only Yes/No so the whole
      // room knows whether the role is in play. Like the mafia picker,
      // it's only shown before roles are dealt (the engine rejects the
      // toggle afterward).
      function renderOptionalRoleToggle(labelText, enabled, msgType) {
        const wrapper = document.createElement("div");
        wrapper.className =
          "flex items-center gap-2 rounded bg-slate-700/60 px-3 py-1.5 text-xs";

        const label = document.createElement("span");
        label.className = "uppercase tracking-wide opacity-70";
        label.textContent = labelText;
        wrapper.appendChild(label);

        if (!myIsHost) {
          const val = document.createElement("span");
          val.className = "font-mono text-sm";
          val.textContent = enabled ? "Yes" : "No";
          wrapper.appendChild(val);
          return wrapper;
        }

        const btn = document.createElement("button");
        btn.className = enabled
          ? "inline-flex h-9 items-center justify-center rounded bg-rose-700 px-3 text-sm font-semibold text-white hover:bg-rose-600"
          : "inline-flex h-9 items-center justify-center rounded bg-slate-600 px-3 text-sm font-semibold hover:bg-slate-500";
        btn.textContent = enabled ? "On" : "Off";
        btn.addEventListener("click", () =>
          send(msgType, { enabled: !enabled })
        );
        wrapper.appendChild(btn);
        return wrapper;
      }

      function renderConsortToggle() {
        return renderOptionalRoleToggle("Consort", consortEnabled, "setConsort");
      }

      function renderVigilanteToggle() {
        return renderOptionalRoleToggle("Vigilante", vigilanteEnabled, "setVigilante");
      }

      function renderYakuzaToggle() {
        return renderOptionalRoleToggle("Yakuza", yakuzaEnabled, "setYakuza");
      }

      function renderTrackerToggle() {
        return renderOptionalRoleToggle("Tracker", trackerEnabled, "setTracker");
      }

      function disabledNote(text) {
        const span = document.createElement("span");
        span.className = "text-xs opacity-70";
        span.textContent = text;
        return span;
      }

      function noteChip(text, bgClass) {
        const span = document.createElement("span");
        span.className = `rounded px-2 py-1 text-xs ${bgClass}`;
        span.textContent = text;
        return span;
      }

      function capitalize(s) {
        return s ? s.charAt(0).toUpperCase() + s.slice(1) : s;
      }

      // showError surfaces a user-facing problem (server rejected a
      // command, wire frame failed to parse, etc) in the error
      // panel. Auto-clears after ~5s so a transient error doesn't
      // linger forever.
      //
      // For developer-style telemetry (every server event echoed),
      // open DevTools — there's no in-app log anymore.
      let errorPanelTimer = null;
      function showError(message) {
        if (!errorPanel) return;
        errorPanel.textContent = message;
        errorPanel.classList.remove("hidden");
        if (errorPanelTimer) clearTimeout(errorPanelTimer);
        errorPanelTimer = setTimeout(() => {
          errorPanel.classList.add("hidden");
          errorPanel.textContent = "";
          errorPanelTimer = null;
        }, 5000);
      }

