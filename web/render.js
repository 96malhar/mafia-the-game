      // ----- roster reducer + render --------------------------------------

      // resetGameState clears everything derived from the event stream
      // so a (re)join can rebuild the model from scratch via replay.
      function resetGameState() {
        players = new Map();
        phase = "lobby";
        day = 0;
        // The model is being rebuilt from scratch, so the resume cursor
        // restarts too; enterRoomFromServer re-adopts the server's
        // high-water mark right after the replay.
        lastSeq = 0;
        myAction = null;
        mafiaKillTarget = null;
        votes = new Map();
        votesRevealed = false;
        votesCastCount = 0;
        iAbstained = false;
        winner = null;
        myRole = null;
        mafiaPeers = new Set();
        yakuzaId = null;
        dayLynchResolved = false;
        rolesDealt = false;
        // hostId is rebuilt from the HostChanged event in the
        // join/rejoin replay; null it so renderPlayers hides the
        // "Host: <name>" label until the replay re-populates it
        // (otherwise we'd briefly show stale identity from a
        // previous session).
        hostId = null;
        // Lobby config is event-sourced (gameCreated / mafiaCountChanged);
        // null out so the replay rebuilds it from the server's
        // events and we don't briefly render stale numbers from a
        // previous session.
        lobbyMinPlayers = null;
        lobbyMaxPlayers = null;
        mafiaCount = null;
        consortEnabled = false;
        vigilanteEnabled = false;
        yakuzaEnabled = false;
        trackerEnabled = false;
        vigilanteFired = false;
        heldFireThisTurn = false;
        // Night turn state — engine-authoritative; replayed on join.
        currentNightRole = "";
        nightTurnDeadlineMs = 0;
        currentNightSubPhase = "";
        currentNightTurnPhantom = false;
        iAmBlocked = false;
        iAmRecruited = false;
        mafiaRecruitTarget = null;
        dayDiscussionPendingDeaths = [];
        lastNightVictims = [];
        spectatorNightActions = [];
        narrationsSeen.clear();
        stopNightCountdown();
        // Clear any open notice modal so a refresh doesn't carry
        // a stale "X IS a mafia member" card into the new session.
        hideModalCard();
        $("my-role").textContent = "—";
        renderAll();
      }

      // renderAll re-renders every dynamic panel. Called whenever phase
      // changes (cheap: at most every few seconds) so that any UI piece
      // depending on phase + roster + votes stays consistent.
      function renderAll() {
        applyPhaseAtmosphere();
        renderInviteBanner();
        renderHostAudioBar();
        renderRoleGuide();
        renderPlayers();
        renderActionPanel();
      }

      // applyPhaseAtmosphere drives the full-screen "Midnight Noir" mood:
      // it tags <body data-phase> so styles.css can paint the per-phase
      // glow + vignette, and re-tints the browser-chrome theme-color so
      // the iOS toolbar / Android nav blend into the same field instead
      // of sandwiching the dark UI between two mismatched bars. The base
      // colours MUST stay in sync with the body[data-phase] rules in
      // styles.css. Before a game starts (or in any unknown phase) we
      // fall back to the neutral lobby ink.
      const PHASE_CHROME = {
        lobby:          "#0b1020",
        night:          "#060914",
        day_discussion: "#15100a",
        day_vote:       "#140a0e",
        ended:          "#07140f",
      };
      function applyPhaseAtmosphere() {
        const body = document.body;
        if (body) body.dataset.phase = phase || "lobby";
        const meta = document.querySelector('meta[name="theme-color"]');
        if (meta) meta.setAttribute("content", PHASE_CHROME[phase] || PHASE_CHROME.lobby);
      }

      // renderRoleGuide hides the static game guide (how-to-play + roles,
      // one #role-guide <details>) during the night phase (eyes closed) and
      // shows it otherwise — the whole lobby and roles-dealt window (phase
      // stays "lobby" until BeginNight), every day phase, and after the game
      // ends. Its content is static markup in index.html; only its visibility
      // is phase-driven. The collapsed/expanded state is browser-managed
      // (<details>) and untouched here, so a player's choice survives
      // re-renders.
      function renderRoleGuide() {
        const guide = $("role-guide");
        if (guide) guide.classList.toggle("hidden", phase === "night");
      }

      // renderInviteBanner toggles the "Invite link" strip based on
      // whether the room is still accepting joins.
      //
      // Boundary: rolesDealt — set when the engine emits its first
      // RoleAssigned event (i.e. StartGame ran). That's the same
      // boundary applyAddPlayer uses to reject new joiners, so
      // hiding the banner here keeps the UI honest with the server.
      // We deliberately do NOT key on phase === "lobby" alone,
      // because the phase stays "lobby" between StartGame and
      // BeginNight — during that window the room is closed but the
      // phase string would still let the banner show.
      function renderInviteBanner() {
        const banner = $("invite-banner");
        if (!banner) return;
        banner.classList.toggle("hidden", rolesDealt);
      }

      function upsertPlayer(pid, patch) {
        const existing = players.get(pid) || {
          id: pid,
          name: pid,
          alive: true,
          deathCause: null,    // "killed" | "lynched" | null
          revealedRole: null,  // populated at gameEnded for everyone
        };
        players.set(pid, { ...existing, ...patch });
        // Roster changes can affect the action panel hint
        // (e.g. "Waiting for 2 more players" → "Ready to start").
        renderPlayers();
        renderActionPanel();
      }

      function renderPlayers() {
        const list = $("players");
        const count = $("player-count");
        const hostLabel = $("player-host");
        list.innerHTML = "";

        // "Host: <name>" lives in the panel header (one canonical
        // spot) instead of as a per-row badge. We hide the label
        // until HostChanged lands and the host is in our roster —
        // showing "Host: p1" before the PlayerJoined for p1 lands
        // would look like a bug. In practice these arrive in the
        // same WS batch, so the hidden state is sub-frame.
        if (hostLabel) {
          const hp = hostId ? players.get(hostId) : null;
          if (hp) {
            hostLabel.textContent = `Host: ${hp.name || hostId}`;
            hostLabel.classList.remove("hidden");
          } else {
            hostLabel.textContent = "";
            hostLabel.classList.add("hidden");
          }
        }

        if (players.size === 0) {
          const empty = document.createElement("li");
          empty.className = "text-sm text-slate-500";
          empty.textContent = "Waiting for players to join…";
          list.appendChild(empty);
          count.textContent = "0";
          return;
        }

        // Deterministic order: insertion order is the join order, which
        // is the most natural reading order. Map iteration preserves it.
        let alive = 0;
        for (const p of players.values()) {
          if (p.alive) alive++;
          list.appendChild(renderPlayerRow(p));
        }
        // In the lobby, frame the count as "joined / cap" so the host
        // knows when the room is full. After the game has started the
        // cap is irrelevant — switch to "alive / total".
        if (phase === "lobby") {
          // Use "?" for the cap/min until the server's gameCreated
          // event lands (lobbyMaxPlayers/MinPlayers are null then).
          // This is a sub-second window in practice — first
          // paint to first event — but rendering "null" would
          // look like a bug.
          const cap = lobbyMaxPlayers ?? "?";
          const min = lobbyMinPlayers ?? "?";
          count.textContent = `${players.size} / ${cap} joined (min ${min})`;
        } else {
          count.textContent = `${alive} alive / ${players.size} total`;
        }
      }

      function renderPlayerRow(p) {
        const li = document.createElement("li");
        li.className =
          "flex items-center justify-between gap-2 rounded border border-slate-800 bg-slate-900/60 px-3 py-2";

        const left = document.createElement("div");
        left.className = "min-w-0";

        const nameEl = document.createElement("div");
        nameEl.className = "min-w-0 truncate text-sm font-medium";
        if (!p.alive) nameEl.classList.add("text-slate-500", "line-through");
        nameEl.textContent = p.name;

        // Secondary line under the name. We used to lead with the
        // server-assigned PlayerID (p1, p2, ...) for moderator
        // disambiguation, but it cluttered every row even though
        // players think of each other by name; engine-level wire
        // identity doesn't belong in the player-facing UI. Now the
        // line only renders when there's something meaningful to
        // show (death cause, revealed role, current vote tally),
        // and is omitted entirely otherwise so living players in
        // ordinary phases get a clean single-line row.
        // The sub-line WRAPS rather than truncates: the post-reveal
        // "voted by A, B, C, …" list can name up to (roster-1) players,
        // which on a phone (single-column rows) would otherwise be
        // clipped to one line with an ellipsis, hiding most voters. The
        // other sub-line contents (death cause, revealed role) are short
        // and unaffected. The row is flex items-center, so a taller
        // wrapped left column stays aligned with the right-side badge.
        const subEl = document.createElement("div");
        subEl.className = "font-mono text-xs text-slate-500 break-words leading-snug";
        const bits = [];
        if (!p.alive) {
          bits.push(p.deathCause === "lynched" ? "lynched by vote" : "killed in the night");
        }
        // p.revealedRole is rendered as an inline role tag next to the
        // name (see the badge block below), not here, so the reveal looks
        // identical whether it arrives mid-game (graveyard) or at game end.
        // Vote tally is secret until the host reveals it. Pre-reveal we
        // show NO counts (not even your own contribution) — a voter's
        // selection is conveyed by their row's "Voted ✓" highlight and
        // the "Your vote: X" chip instead. Post-reveal each living
        // player's row shows how many votes they received and the names
        // of everyone who voted against them, so the whole room (incl.
        // the dead) can read exactly who's lining up on whom.
        if (phase === "day_vote" && votesRevealed && p.alive) {
          const voters = votersFor(p.id);
          if (voters.length > 0) {
            bits.push(`${voters.length} vote${voters.length === 1 ? "" : "s"}`);
            bits.push(`voted by ${voters.join(", ")}`);
          }
        }

        // Identity badges (You / Mafia) sit inline right after the name
        // rather than in the right-hand cluster. Keeping them on the left
        // means the action buttons (Target / Vote / Save self) stay
        // flush-right and vertically aligned across every row, regardless
        // of how many badges a given row carries. shrink-0 keeps the
        // badges intact while a long name truncates on narrow screens.
        const nameLine = document.createElement("div");
        nameLine.className = "flex min-w-0 items-center gap-2";
        nameLine.appendChild(nameEl);
        if (p.id === myId) {
          nameLine.appendChild(badge("You", "shrink-0 bg-emerald-700 text-emerald-100"));
        }
        // Single role/faction identity badge per row, in precedence order:
        //   - revealedRole (the player's actual role) wins once it's
        //     known to this viewer — that happens for EVERY player when
        //     the roster is revealed to the dead mid-game and for everyone
        //     at game end. Rendered as the same indigo role tag in both
        //     cases, so the reveal UI is identical. Covers a promoted
        //     consort (shown as mafia).
        //   - mafiaPeers (populated only from the faction-scoped
        //     mafiaRoster event, so empty for town) badges every known
        //     mafioso "Mafia" — the in-game faction affordance, shown
        //     before any reveal.
        //   - otherwise the LOCAL player sees their own dealt role next
        //     to "You" (town never learns others' roles, so this is
        //     self-only). myRole is null until roles are dealt.
        if (p.revealedRole) {
          nameLine.appendChild(badge(capitalize(p.revealedRole), "shrink-0 bg-indigo-700 text-indigo-100"));
        } else if (mafiaPeers.has(p.id)) {
          // The whole faction (and the Yakuza itself) badges the Yakuza
          // distinctly; every other faction member is the interchangeable
          // "Mafia". yakuzaId comes from the faction-only mafiaRoster event,
          // so this never leaks to town.
          nameLine.appendChild(
            p.id === yakuzaId
              ? badge("Yakuza", "shrink-0 bg-rose-600 text-rose-50")
              : badge("Mafia", "shrink-0 bg-rose-800 text-rose-100")
          );
        } else if (p.id === myId && myRole) {
          nameLine.appendChild(badge(capitalize(myRole), "shrink-0 bg-indigo-700 text-indigo-100"));
        }
        // Faction-collective kill: once any mafioso locks a target, badge
        // that player on every faction member's roster (mafia AND the yakuza,
        // including teammates who didn't submit) so the locked kill is
        // visible at a glance. Only the mafia faction ever has mafiaKillTarget
        // set (town acks are private), so this never leaks to the village.
        if (iAmMafiaFaction() && mafiaKillTarget === p.id) {
          nameLine.appendChild(badge("Target", "shrink-0 bg-rose-600 text-rose-50"));
        }
        // Faction recruit: when the Yakuza locks a recruit, badge the target
        // on every faction member's roster (the recruiting Yakuza AND co-mafia)
        // so the whole family sees who's being converted. Amber distinguishes
        // it from a rose kill "Target". Only the mafia faction ever has
        // mafiaRecruitTarget set (recruitRecorded is faction-scoped), so this
        // never leaks to town.
        if (iAmMafiaFaction() && mafiaRecruitTarget === p.id) {
          nameLine.appendChild(badge("Recruit", "shrink-0 bg-amber-600 text-amber-50"));
        }

        left.appendChild(nameLine);
        if (bits.length > 0) {
          subEl.textContent = bits.join(" · ");
          left.appendChild(subEl);
        }

        const right = document.createElement("div");
        right.className = "flex shrink-0 items-center gap-1";

        // Phase-aware action button on each row. Targeting lives here
        // (vs a separate panel) so action and identity stay together.
        const btn = phaseActionButton(p);
        if (btn) right.appendChild(btn);
        // No per-row "Host" badge: who-is-host lives in the
        // Players panel header (see renderPlayers). The previous
        // badge competed with the row's action button (Vote /
        // Target) for horizontal space on narrow screens.

        li.appendChild(left);
        li.appendChild(right);
        return li;
      }

      // tallyFor returns the current public vote count against pid.
      function tallyFor(pid) {
        let n = 0;
        for (const target of votes.values()) if (target === pid) n++;
        return n;
      }

      // votersFor returns the display names of everyone who voted for
      // pid, in roster (join) order so the list is stable across renders.
      // Only meaningful once votes are revealed (pre-reveal the `votes`
      // map holds at most the local player's own vote).
      function votersFor(pid) {
        const names = [];
        for (const p of players.values()) {
          if (votes.get(p.id) === pid) names.push(p.name);
        }
        return names;
      }

      // projectedLynch mirrors the server's strict-majority rule
      // (resolveDayVote in internal/game/rules_day.go): it returns the
      // player who WOULD be lynched if the host finalized right now — the
      // single target whose vote count is more than half the living
      // population (count*2 > living) — or null when no target clears
      // that bar (a tie, a plurality short of half, abstentions, or no
      // votes). Only meaningful once votes are revealed, since the full
      // tally isn't local before then. KEEP IN SYNC with the server rule.
      function projectedLynch() {
        let living = 0;
        for (const p of players.values()) if (p.alive) living++;
        // At most one target can clear >50%, so the first match is THE
        // majority target — same uniqueness argument as the server.
        for (const p of players.values()) {
          if (tallyFor(p.id) * 2 > living) return p;
        }
        return null;
      }

      // canActAtNight reports whether the LOCAL player has a night
      // action available given their role. Villager has none. The
      // engine also forbids the doctor from self-saving on Night 0,
      // but the wire never echoes the day cleanly enough for us to
      // pre-block that client-side; we let the server reject and the
      // error flow into the log. Conservative behavior.
      function canActAtNight() {
        if (!myRole) return false;
        return (
          myRole === "mafia" ||
          myRole === "yakuza" ||
          myRole === "doctor" ||
          myRole === "detective" ||
          myRole === "consort" ||
          myRole === "vigilante" ||
          myRole === "tracker"
        );
      }

      // phaseActionButton returns a per-row action button suitable for
      // the current phase, or null if no row-action applies.
      function phaseActionButton(p) {
        const me = players.get(myId);
        const iAmAlive = !!(me && me.alive);

        // Night Target buttons are gated by the strict turn-order
        // rule: only show them when it's THIS role's turn AND we're
        // in the "act" sub-phase. Outside the turn or in any other
        // sub-phase (narrate / ponder / sleep / settle / opening) the
        // engine would reject the action with ErrNotYourTurn or
        // ErrWrongPhase, so hiding the buttons makes the UX match
        // the rules. The act sub-phase is the engine's authoritative
        // "actor may submit" window; it begins only after the role's
        // narration has fully played out.
        //
        // Phantom turns (no living holder of the role) skip the act
        // sub-phase entirely on the server, so currentNightSubPhase
        // never reaches "act" for them — but we keep the explicit
        // phantom guard for defense-in-depth.
        // Row buttons (Target / Vote) are sized to clear the 44px
        // iOS HIG tap-target minimum. The previous px-2/py-1/text-xs
        // styling looked nice on desktop but produced ~24×16px hit
        // areas that were borderline unusable on a phone.
        const rowBtnBase =
          "inline-flex min-h-[44px] min-w-[64px] items-center justify-center rounded px-3 py-2 text-sm font-medium";

        // Self-targeting policy:
        //   - mafia: cannot target self OR a fellow mafia — the engine
        //     rejects a mafia kill on ANY mafia (rolespec.go Validate),
        //     so we hide the button on every teammate row (mafiaPeers
        //     includes self, covering the self case too).
        //   - detective: cannot investigate self (ErrSelfTarget).
        //   - tracker: cannot track self (ErrSelfTarget).
        //   - doctor: CAN save self on any night.
        // So we allow the self row only for the doctor.
        const isSelfRow = p.id === myId;
        // Fellow-mafia rows carry no action button for either the mafia OR
        // the yakuza: the engine rejects a faction kill on any mafia, and a
        // recruit on a mafioso, and mafiaPeers includes our own id (covering
        // the self case too). The consort is NOT in mafiaPeers, so she stays
        // a legal target — including a recruit.
        const isFellowMafia = iAmMafiaFaction() && mafiaPeers.has(p.id);
        const canTargetThisRow =
          (!isSelfRow && !isFellowMafia) || myRole === "doctor";

        if (
          phase === "night" &&
          iAmAlive &&
          canActAtNight() &&
          // The yakuza acts during the MAFIA turn (it has no turn of its
          // own), so myNightTurnActive() opens its picker when the mafia is
          // up even though myRole is "yakuza".
          myNightTurnActive() &&
          currentNightSubPhase === "act" &&
          !currentNightTurnPhantom &&
          !iAmBlocked &&
          // A recruited player's own power is suppressed for the night —
          // their turn is phantom server-side, but we also hide the picker
          // locally the moment the private "recruited" notice lands.
          !iAmRecruited &&
          // The vigilante has a single bullet for the whole game. Once
          // we've fired it (tracked locally from our own
          // nightActionRecorded), hide the picker — the engine rejects
          // any further shot with ErrAlreadyActed. Likewise once we've
          // held fire this turn (an optimistic local flag), hide the
          // picker immediately rather than waiting for the ponder event.
          !(myRole === "vigilante" && (vigilanteFired || heldFireThisTurn)) &&
          p.alive &&
          canTargetThisRow
        ) {
          const submitted = myAction === p.id;
          const mkBtn = (label, active, onClick) => {
            const b = document.createElement("button");
            b.className = active
              ? `${rowBtnBase} bg-amber-600 text-white`
              : `${rowBtnBase} bg-slate-700 text-white hover:bg-slate-600`;
            b.textContent = label;
            b.addEventListener("click", onClick);
            return b;
          };

          // The yakuza gets TWO buttons per row — a faction Kill and a
          // Recruit (the one-shot self-sacrifice conversion) — and always
          // sees both choices during its act window (no mode toggle). The
          // two fire distinct engine commands. A locked recruit is tracked
          // faction-wide via mafiaRecruitTarget (from recruitRecorded), the
          // recruit analogue of myAction for the kill.
          if (myRole === "yakuza") {
            const recruiting = mafiaRecruitTarget === p.id;
            const wrap = document.createElement("div");
            wrap.className = "flex items-center gap-1";
            wrap.appendChild(
              mkBtn(submitted ? "Killing" : "Kill", submitted, () =>
                send("nightAction", { target: p.id })
              )
            );
            wrap.appendChild(
              mkBtn(recruiting ? "Recruiting" : "Recruit", recruiting, () =>
                send("recruit", { target: p.id })
              )
            );
            return wrap;
          }

          // Button label per acting role, sourced from ROLE_VERBS (the
          // shared verb table in helpers.js) so it can't drift from the
          // confirmation chip. The doctor is special-cased for its
          // self-row "Save self" variant (it's the only role that can
          // target its own row); any unknown role falls back to the
          // generic "Target". For every other role isSelfRow is
          // guaranteed false here (canTargetThisRow gates it).
          const verbs = ROLE_VERBS[myRole];
          let label;
          if (myRole === "doctor") {
            label = submitted
              ? (isSelfRow ? "Saving self" : verbs.gerund)
              : (isSelfRow ? "Save self" : verbs.base);
          } else if (verbs) {
            label = submitted ? verbs.gerund : verbs.base;
          } else {
            // No self-row variant here: canTargetThisRow gates isSelfRow
            // false for every role that reaches this generic fallback (only
            // the doctor renders its own row, handled above).
            label = submitted ? "Targeted" : "Target";
          }
          return mkBtn(label, submitted, () =>
            send("nightAction", { target: p.id })
          );
        }

        if (phase === "day_vote" && !votesRevealed && iAmAlive && p.alive && p.id !== myId) {
          const mine = votes.get(myId);
          const isMyTarget = mine === p.id;
          const b = document.createElement("button");
          b.className = isMyTarget
            ? `${rowBtnBase} bg-rose-600 text-white`
            : `${rowBtnBase} bg-slate-700 text-white hover:bg-slate-600`;
          // Tap the active selection again to retract — same button
          // does both jobs. The engine treats target:"" as a retract
          // (rules_day.go applyDayVote) and emits VoteRetracted.
          // The action-panel hint ("Tap your current selection to
          // retract") teaches the gesture; the button label stays
          // clean.
          b.textContent = isMyTarget ? "Voted ✓" : "Vote";
          b.addEventListener("click", () =>
            send("vote", { target: isMyTarget ? "" : p.id })
          );
          return b;
        }

        return null;
      }

      function badge(text, klass) {
        const span = document.createElement("span");
        span.className = `rounded px-1.5 py-0.5 text-[10px] font-semibold uppercase tracking-wide ${klass}`;
        span.textContent = text;
        return span;
      }

