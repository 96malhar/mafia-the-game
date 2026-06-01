      // ----- URL-driven lobby setup ---------------------------------------

      // recoverToLobby restores the pre-game lobby view after an
      // auto-rejoin attempt failed (typically: server restart cleared
      // the room, or stored credentials are stale). It assumes the
      // game view was never shown (we caught the error before
      // "joined" / "rejoined" arrived).
      function recoverToLobby(code, note) {
        // Bouncing to the lobby ends any in-flight reconnect loop and
        // clears the banner — we're giving up on this socket on
        // purpose, so no further auto-retries.
        cancelReconnect();
        reconnectAttempts = 0;
        showReconnectingBanner(false);
        $("game").classList.add("hidden");
        $("lobby").classList.remove("hidden");
        // If a code is given, keep the URL pointed at that room so
        // applyURLState() formats the lobby as "Join room ZMPP"
        // (used by the rejoin-failed paths — the user still wants to
        // try this specific room). Passing null clears the URL so
        // the lobby reverts to the generic "Start a game" view with
        // the Create button visible — used when the target room is
        // unjoinable and re-attempting would just fail again.
        history.replaceState(null, "", code ? shareLinkFor(code) : "/");
        applyURLState();
        setStatus(note, "text-amber-400");
      }

      // tryAutoRejoin runs at page load. If the URL carries a room
      // code AND credStore has matching rejoin credentials, we
      // open a WebSocket immediately and let the server replay the
      // event log — the user never sees the lobby. Returns true if a
      // rejoin attempt was launched.
      function tryAutoRejoin() {
        const code = roomFromURL();
        if (!code) return false;
        const stored = credStore.getItem(storageKey(code));
        if (!stored) return false;
        let creds;
        try { creds = JSON.parse(stored); }
        catch { credStore.removeItem(storageKey(code)); return false; }
        if (!creds || !creds.playerId || !creds.secret) {
          credStore.removeItem(storageKey(code));
          return false;
        }
        pendingRejoinCode = code;
        setStatus(`reconnecting to ${code}…`, "text-slate-300");
        // The server takes name from its own roster on rejoin, so
        // we pass null for the name argument.
        connect(code, null, creds);
        return true;
      }

      function applyURLState() {
        const fromLink = roomFromURL();
        if (fromLink) {
          // Visitor came from a share link. Make joining the obvious
          // action; hide Create so they don't accidentally spawn a
          // second room.
          $("lobby-title").textContent = `Join room ${fromLink}`;
          $("lobby-subtitle").textContent = "Pick a name to join.";
          $("join-code").textContent = fromLink;
          $("join").classList.remove("hidden");
          $("create").classList.add("hidden");
        } else {
          $("lobby-title").textContent = "Start a game";
          $("lobby-subtitle").textContent = "Pick a name. Create a room or join one via a share link.";
          $("join").classList.add("hidden");
          $("create").classList.remove("hidden");
        }
        refreshLobbyButtons();
      }

      // maybeAutoJoinFromURL is a dev/demo convenience: a link carrying
      // BOTH ?room= and ?name= joins immediately, skipping the manual
      // name-entry + click. The `task lobby` helper uses this to open a
      // tab per bot player straight into the lobby. Regular share links
      // (room only, no name) are unaffected — they still land on the
      // name-entry lobby. Returns true if an auto-join was launched.
      function maybeAutoJoinFromURL() {
        const code = roomFromURL();
        const name = nameFromURL();
        if (!code || !name) return false;
        $("name").value = name;
        refreshLobbyButtons();
        joinRoom(code);
        return true;
      }

