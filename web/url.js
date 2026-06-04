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

      // showUnjoinableRoom pivots the lobby to "create a new room" when a
      // join attempt against a SPECIFIC room can't ever succeed — the room
      // doesn't exist, a game is already in progress, the lobby is full, or
      // the game has ended. Re-attempting the same room would just fail
      // again, so instead of stranding the visitor on a dead "Join room XYZ"
      // screen we surface the reason and make "Create new room" the obvious
      // next step, carrying over the name they already typed (createRoom
      // reads it from the same #name input). Distinct from recoverToLobby,
      // which is for transient/rejoin failures where retrying the same room
      // is the right move.
      function showUnjoinableRoom(code, reason) {
        // Same teardown as recoverToLobby: stop any reconnect loop, drop the
        // banner, and surface the lobby over the in-game view.
        cancelReconnect();
        reconnectAttempts = 0;
        showReconnectingBanner(false);
        $("game").classList.add("hidden");
        $("lobby").classList.remove("hidden");
        // Clear the URL: the room it points at is unjoinable, so a reload
        // should land on a clean lobby rather than silently re-running the
        // doomed join. (createRoom will repoint it at the new room's code.)
        history.replaceState(null, "", "/");
        // Override applyURLState's framing directly — we don't call it here
        // because it keys purely off the (now-cleared) URL and would show the
        // generic "Start a game" copy, dropping the reason. Hide Join (the
        // target room can't accept us) and surface Create in its place.
        $("lobby-title").textContent = code ? `Room ${code} unavailable` : "Room unavailable";
        // Render the reason in red so it reads unmistakably as an error,
        // not just muted helper copy. applyURLState restores the default
        // slate when the lobby returns to a normal join/create view.
        $("lobby-subtitle").textContent = reason;
        $("lobby-subtitle").className = "text-sm text-rose-400";
        $("join").classList.add("hidden");
        $("create").classList.remove("hidden");
        refreshLobbyButtons();
        setStatus(code ? `room ${code} unavailable` : "room unavailable", "text-rose-400");
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
        // Restore the muted-slate subtitle: showUnjoinableRoom may have
        // turned it red, and any normal join/create view that follows
        // should read as neutral helper copy again.
        $("lobby-subtitle").className = "text-sm text-slate-400";
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

