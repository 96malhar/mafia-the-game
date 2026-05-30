package ws

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/coder/websocket"

	"github.com/96malhar/mafia-the-game/internal/room"
	"github.com/96malhar/mafia-the-game/internal/wire"
)

// readLimit caps the size of a single inbound JSON frame. 64 KiB is
// plenty for any client message — typical inbound is under 200 bytes.
// Beyond this size, the connection is closed (likely abuse or a broken
// client).
const readLimit = 64 << 10

// writeTimeout bounds how long a single message write may block. A
// healthy client drains frames within milliseconds; anything longer
// indicates a broken or malicious peer.
const writeTimeout = 10 * time.Second

// pingInterval is how often the write pump sends a WebSocket ping when
// the connection is otherwise idle. Two jobs:
//
//  1. Keepalive: a steady trickle of frames stops idle-timeout proxies
//     (nginx/ALB/Cloudflare, default 60–100s) and carrier NATs from
//     silently dropping a connection during long, app-quiet stretches
//     — notably the 15–20 min day-discussion phase where nobody touches
//     their phone.
//  2. Dead-peer detection: conn.Ping blocks for a pong, so a peer that
//     vanished (half-open TCP after a network drop) surfaces as a ping
//     error within pingTimeout instead of lingering for the OS TCP
//     keepalive (~2h).
//
// 20s comfortably beats the common 60s proxy idle floor with margin for
// a missed beat. It does NOT rescue a mobile tab the OS has suspended
// (a frozen tab can't ping) — that case is handled by client-side
// reconnect-on-resume; see web/index.html.
const pingInterval = 20 * time.Second

// pingTimeout bounds how long we wait for a pong before declaring the
// peer dead. Mirrors writeTimeout: a healthy client answers in
// milliseconds.
const pingTimeout = 10 * time.Second

// defaultJoinDeadline bounds how long an UNAUTHENTICATED fresh
// connection may stay open without completing a join. Without it, a
// client that upgrades and then sends nothing parks two goroutines + a
// socket indefinitely (the keepalive ping keeps a silent-but-alive peer
// up forever). Once a join succeeds the subscriber has a PlayerID and
// the deadline no longer applies, so legitimately-idle joined players
// (e.g. during discussion) are never bounced. Rejoins authenticate
// server-side before the pumps start, so this deadline doesn't apply to
// them.
//
// Overridable per-handler via HandlerConfig.JoinDeadline (tests use a
// tiny value); zero falls back to this default.
const defaultJoinDeadline = 30 * time.Second

// readPump runs in its own goroutine for the lifetime of a connection.
// It is the SOLE reader on the websocket. It exits when the read fails
// (client disconnected, bad frame, etc.), at which point it cancels
// the per-connection context so the writePump also exits.
//
// The pump translates client JSON into room.SubmitX calls. Per-command
// errors (bad JSON, unknown type) become outError frames sent through
// the SAME subscriber's outbound channel so they arrive in order with
// any concurrent broadcasts — we never write directly to the WS here.
func readPump(
	ctx context.Context,
	cancel context.CancelFunc,
	logger *slog.Logger,
	conn *websocket.Conn,
	r *room.Room,
	sub *room.Subscriber,
	joinDeadline time.Duration,
) {
	defer cancel() // signal writePump to exit on any read failure

	conn.SetReadLimit(readLimit)

	// Enforce the join deadline OUT OF BAND from the read loop. A
	// successful join sets the subscriber's PlayerID asynchronously:
	// SubmitJoin only enqueues the join for the room's actor, which
	// sets PlayerID when it later processes the message — after
	// SubmitJoin (and so dispatchClientMessage) has already returned.
	// Binding the deadline to an individual read therefore races: a
	// client that joins and then stays silent (e.g. a villager through
	// the night) can have its next read block on a soon-to-expire
	// deadline context a hair before the actor sets PlayerID, then get
	// reaped at the deadline despite being fully joined.
	//
	// Instead we arm a single timer for the whole connection. If the
	// subscriber still hasn't authenticated when it fires, we cancel
	// the connection (tearing down both pumps); otherwise the join
	// landed and the reaper is a harmless no-op. Reads always block on
	// the unbounded connection context, so a joined-but-silent player
	// is never bounced. joinDeadline <= 0 disables the reaper entirely
	// — used by the rejoin path, which authenticates server-side before
	// the pumps start.
	if joinDeadline > 0 {
		reaper := time.NewTimer(joinDeadline)
		go func() {
			defer reaper.Stop()
			select {
			case <-ctx.Done():
				// Connection already closing; nothing to reap.
			case <-reaper.C:
				if sub.PlayerID() == "" {
					logger.Info("ws join deadline expired; closing unauthenticated connection")
					cancel()
				}
			}
		}()
	}

	for {
		// Read one full message. coder/websocket returns the type
		// (text/binary) and the payload as a byte slice. A joined
		// player may sit silent indefinitely (e.g. the long verbal
		// day-discussion phase); the keepalive ping in writePump and
		// the join reaper above are what bound idle/never-join peers.
		mt, data, err := conn.Read(ctx)
		if err != nil {
			// Normal close codes and context cancellation are not
			// errors worth logging at warn level.
			if !isExpectedCloseError(err) && ctx.Err() == nil {
				logger.Info("ws read ended", "err", err)
			}
			return
		}
		if mt != websocket.MessageText {
			// We only speak JSON-over-text in v1. Reject anything else
			// loudly so a misconfigured client notices.
			sendErrorViaRoom(ctx, r, sub, wire.ErrCodeBadFrame, "expected text frame")
			continue
		}

		tag, payload, err := decodeClientMessage(data)
		if err != nil {
			sendErrorViaRoom(ctx, r, sub, wire.ErrCodeBadMessage, err.Error())
			continue
		}

		if err := dispatchClientMessage(ctx, r, sub, tag, payload); err != nil {
			// dispatchClientMessage returns errors only for transport-
			// level failures (room closed, ctx cancelled) — i.e. normal
			// teardown — so this is debug-level, not a problem to flag.
			logger.Debug("ws dispatch ended", "err", err)
			return
		}
	}
}

// writePump runs in its own goroutine for the lifetime of a connection.
// It is the SOLE writer on the websocket. It exits when the subscriber's
// outbound channel closes (room dropped us) or the connection dies.
//
// On exit, it cancels the shared context so the readPump unwinds too.
func writePump(
	ctx context.Context,
	cancel context.CancelFunc,
	logger *slog.Logger,
	conn *websocket.Conn,
	sub *room.Subscriber,
) {
	defer cancel()

	// Idle keepalive / dead-peer probe. See pingInterval.
	ping := time.NewTicker(pingInterval)
	defer ping.Stop()

	out := sub.Outbound()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ping.C:
			// conn.Ping blocks until the peer pongs or the context
			// expires. A failure means the peer is gone (or wedged);
			// exit so the connection tears down.
			pctx, pcancel := context.WithTimeout(ctx, pingTimeout)
			err := conn.Ping(pctx)
			pcancel()
			if err != nil {
				if !isExpectedCloseError(err) && ctx.Err() == nil {
					logger.Info("ws ping failed", "err", err)
				}
				return
			}
		case msg, ok := <-out:
			if !ok {
				// Channel closed by the room. Close the connection
				// politely so the peer sees a clean shutdown.
				_ = conn.Close(websocket.StatusNormalClosure, "room closed connection")
				return
			}

			raw, known, err := encodeOutbound(msg)
			if err != nil {
				if !known {
					// A shape we deliberately don't encode — skip it
					// but keep the connection (defensive; shouldn't
					// happen for a version-matched pair).
					logger.Warn("dropping unknown outbound", "err", err)
					continue
				}
				// A KNOWN shape that failed to marshal is a server
				// bug. Limping on would silently gap the client's
				// event stream and desync its view, so we drop the
				// connection instead; the client's auto-reconnect
				// then triggers a clean full-state replay.
				logger.Warn("encode outbound failed; dropping connection", "err", err)
				return
			}

			// Per-message write timeout. derive a child ctx so a stuck
			// peer doesn't block the goroutine forever.
			wctx, wcancel := context.WithTimeout(ctx, writeTimeout)
			err = conn.Write(wctx, websocket.MessageText, raw)
			wcancel()
			if err != nil {
				if !isExpectedCloseError(err) && ctx.Err() == nil {
					logger.Info("ws write ended", "err", err)
				}
				return
			}
		}
	}
}

// dispatchClientMessage translates a decoded client message into a
// SubmitX call on the room. Returns an error only on transport-level
// failure (room closed, ctx done). Per-command errors flow back to the
// client through the subscriber's outbound channel via the room.
func dispatchClientMessage(
	ctx context.Context,
	r *room.Room,
	sub *room.Subscriber,
	tag clientMsgType,
	payload any,
) error {
	// Join is the one client message that isn't an engine command
	// (it carries connection identity, not game intent), so it's
	// handled explicitly. Everything else is routed through
	// commandFromClient, which is the SINGLE source of truth for which
	// tags map to engine commands — we deliberately don't re-list the
	// command tags here. (An earlier version did, and a new command
	// silently fell through to "unknown type" because the second list
	// wasn't updated.)
	if tag == clientMsgJoin {
		d := payload.(clientJoinData)
		return r.SubmitJoin(ctx, sub, d.Name)
	}

	cmd, ok := commandFromClient(tag, payload)
	if !ok {
		// decodeClientMessage already rejects unknown tags, so this
		// only fires if a tag decodes but has no command mapping —
		// belt-and-braces against a half-wired future message.
		sendErrorViaRoom(ctx, r, sub, wire.ErrCodeBadMessage, "unknown type")
		return nil
	}
	return r.SubmitCommand(ctx, sub, cmd)
}

// sendErrorViaRoom delivers an OutError to the subscriber. Transport-
// level errors (bad JSON, unknown frame type) bypass the room's run
// loop — they're not engine state — but still ride the subscriber's
// outbound channel so they arrive in order with concurrent broadcasts.
//
// If the channel is full or already closed, the error is dropped: the
// room would also disconnect a slow subscriber in this case, so a lost
// error is the lesser problem.
//
// `code` is typed (wire.ErrorCode) so a typo at the call site is a
// Go compile error rather than a silent wire mismatch.
func sendErrorViaRoom(ctx context.Context, _ *room.Room, sub *room.Subscriber, code wire.ErrorCode, msg string) {
	recordMessageRejected(ctx, code)
	_ = sub.TrySend(room.OutError{Code: code, Message: msg})
}

// isExpectedCloseError reports whether err is a normal close path that
// we should NOT log at warn/info level. This includes context
// cancellation, the StatusNormalClosure code, and the common abnormal-
// close that browsers emit on tab close.
func isExpectedCloseError(err error) bool {
	if err == nil {
		return true
	}
	if errors.Is(err, context.Canceled) {
		return true
	}
	status := websocket.CloseStatus(err)
	switch status {
	case websocket.StatusNormalClosure,
		websocket.StatusGoingAway,
		websocket.StatusNoStatusRcvd,
		websocket.StatusAbnormalClosure:
		return true
	}
	return false
}
