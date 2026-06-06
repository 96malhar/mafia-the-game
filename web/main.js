      // ----- wiring -------------------------------------------------------

      $("name").addEventListener("input", refreshLobbyButtons);
      $("name").addEventListener("keydown", (e) => {
        if (e.key !== "Enter") return;
        if (roomFromURL()) joinRoom(roomFromURL());
        else createRoom();
      });

      $("create").addEventListener("click", createRoom);
      $("join").addEventListener("click", () => joinRoom(roomFromURL()));

      // Reconnect-on-resume. A mobile tab suspended in a pocket can't
      // run the backoff timer, so the moment it returns to the
      // foreground (or the network comes back) we re-open the socket
      // immediately rather than waiting for a timer that never ticked.
      document.addEventListener("visibilitychange", () => {
        if (document.visibilityState === "visible") resumeConnectionIfNeeded();
      });
      window.addEventListener("pageshow", resumeConnectionIfNeeded);
      window.addEventListener("online", resumeConnectionIfNeeded);

      $("manual-join").addEventListener("click", () => {
        const code = $("manual-code").value.trim().toUpperCase();
        if (code) joinRoom(code);
      });

      // Notice modal dismissal: the button, clicking the dimmed
      // overlay (but not the card), and Escape all close it. These are all
      // USER-driven, so they go through dismissModalCard, which records the
      // acknowledgement for one-shot notices (recruit / promotion /
      // detective result) so they aren't re-popped on a later replay.
      $("notice-modal-dismiss").addEventListener("click", dismissModalCard);
      $("notice-modal").addEventListener("click", (e) => {
        if (e.target.id === "notice-modal") dismissModalCard();
      });
      document.addEventListener("keydown", (e) => {
        if (e.key === "Escape") {
          const m = $("notice-modal");
          if (m && !m.classList.contains("hidden")) dismissModalCard();
        }
      });

      $("copy-link").addEventListener("click", async () => {
        const url = $("share-url").textContent;
        const btn = $("copy-link");
        const original = btn.textContent;
        try {
          await navigator.clipboard.writeText(url);
          btn.textContent = "Copied!";
        } catch {
          // Clipboard API can be unavailable (http on a LAN IP, older
          // browsers). Fall back to selecting the text so the user can
          // Cmd/Ctrl-C it themselves.
          const range = document.createRange();
          range.selectNodeContents($("share-url"));
          const sel = window.getSelection();
          sel.removeAllRanges();
          sel.addRange(range);
          btn.textContent = "Select & copy";
        }
        setTimeout(() => { btn.textContent = original; }, 1500);
      });

      // On page load: if the URL has ?room= AND credStore has
      // rejoin credentials for that room, reconnect silently. Only
      // show the lobby if there's nothing to rejoin to. This is what
      // makes a tab refresh a no-op from the player's perspective.
      if (!tryAutoRejoin()) {
        applyURLState();
        // A ?room=&name= link (the `task lobby` demo) auto-joins; a
        // plain ?room= share link just shows the name-entry lobby.
        maybeAutoJoinFromURL();
      }

      // Quick health probe to confirm the server is up before anyone clicks.
      (async () => {
        try {
          const res = await fetch("/healthz");
          setStatus(res.ok ? "server ready" : `server status ${res.status}`,
            res.ok ? "text-slate-400" : "text-amber-400");
        } catch (err) {
          setStatus(`server unreachable (${err.message})`, "text-rose-400");
        }
      })();
