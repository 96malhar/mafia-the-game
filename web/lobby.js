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

      async function joinRoom(code) {
        const name = currentName();
        if (!name || !code) return;
        const stored = credStore.getItem(storageKey(code));
        const creds = stored ? JSON.parse(stored) : null;
        // Probe the room BEFORE opening the WebSocket. A browser can't
        // read the HTTP status of a failed WS handshake (a 404 surfaces
        // only as an opaque close → a bare "disconnected"), so we ask
        // the server up front whether the room exists and give a precise
        // message. If we hold stored creds we skip the probe and let the
        // rejoin path (and its own recovery messaging) run. If the probe
        // itself errors (network/CORS), we fall through to connect rather
        // than block a legitimate join on a flaky preflight.
        if (!creds) {
          setStatus(`checking room ${code}…`, "text-slate-300");
          try {
            const res = await fetch(`/api/rooms/${encodeURIComponent(code)}`);
            if (res.status === 404) {
              // The room in the link doesn't exist (typo'd code, or the
              // host's room was reaped). Offer to create a fresh one with
              // the name they already typed rather than leaving them stuck
              // on a join screen for a room that isn't there.
              showUnjoinableRoom(code, `Room ${code} doesn't exist. Create a new room with your name, or join another by code.`);
              return;
            }
          } catch {
            // Probe unreachable — don't block the join; the WS attempt
            // below will surface a connection error if the server is
            // genuinely down.
          }
        }
        connect(code, name, creds);
      }

