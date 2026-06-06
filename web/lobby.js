      // ----- lobby actions ------------------------------------------------

      // Both actions need a name. We gate the buttons rather than
      // popping an alert; the disabled state makes the requirement
      // visible at a glance.
      function currentName() {
        return $("name").value.trim();
      }

      function refreshLobbyButtons() {
        const hasName = currentName().length > 0;
        const fromLink = roomFromURL();
        $("create").disabled = !hasName;
        $("join").disabled = !hasName || !fromLink;
      }

      async function createRoom() {
        const name = currentName();
        if (!name) return;
        setStatus("creating room…", "text-slate-300");
        try {
          const res = await fetch("/api/rooms", { method: "POST" });
          if (!res.ok) throw new Error(`HTTP ${res.status}`);
          const { code } = await res.json();
          // Reflect the new room in the address bar so a reload of
          // THIS tab keeps the same target room, and a copy-paste of
          // the address bar URL is also a valid invite.
          history.replaceState(null, "", shareLinkFor(code));
          connect(code, name, null);
        } catch (e) {
          setStatus(`could not create: ${e.message}`, "text-rose-400");
        }
      }

      // probeRoom asks the server whether `code` can be joined BEFORE we open
      // a WebSocket. A browser can't read the HTTP status of a failed WS
      // handshake (a 404 surfaces only as an opaque close → a bare
      // "disconnected"), so this preflight is the only way to give a precise
      // reason. The server reports both existence (404) and joinability (a
      // {joinable:false,...} body for a room that's in progress, full, or
      // ended). Returns one of:
      //   {state:"joinable"}            — go ahead and connect
      //   {state:"unjoinable", reason}  — surface `reason` and offer Create
      //   {state:"unknown"}             — probe unreachable; caller decides
      // Centralizing it here keeps the page-load auto-check
      // (maybeProbeRoomFromURL) and the Join-button path in exact agreement on
      // what "can't join" means and what message to show.
      async function probeRoom(code) {
        try {
          const res = await fetch(`/api/rooms/${encodeURIComponent(code)}`);
          if (res.status === 404) {
            // The room in the link doesn't exist (typo'd code, or the host's
            // room was reaped/never created).
            return {
              state: "unjoinable",
              reason: `Room ${code} doesn't exist. Create a new room with your name, or join another by code.`,
            };
          }
          // The room exists. The body tells us whether it can still take a new
          // player; the server owns the player-facing wording.
          const info = await res.json().catch(() => null);
          if (info && info.joinable === false) {
            return {
              state: "unjoinable",
              reason: info.message || `Room ${code} can't be joined right now. Create a new room to play.`,
            };
          }
          return { state: "joinable" };
        } catch {
          // Probe unreachable (network/CORS) — don't block; let the caller
          // proceed and surface any real connection error via the WS attempt.
          return { state: "unknown" };
        }
      }

      async function joinRoom(code) {
        const name = currentName();
        if (!name || !code) return;
        const stored = credStore.getItem(storageKey(code));
        const creds = stored ? JSON.parse(stored) : null;
        // If we hold stored creds we skip the probe and let the rejoin path
        // (and its own recovery messaging) run. Otherwise preflight the room
        // and, if it can't accept us, pivot to "create a new room" with the
        // typed name rather than opening a socket that's doomed to be
        // rejected.
        if (!creds) {
          setStatus(`checking room ${code}…`, "text-slate-300");
          const probe = await probeRoom(code);
          if (probe.state === "unjoinable") {
            showUnjoinableRoom(code, probe.reason);
            return;
          }
        }
        connect(code, name, creds);
      }

