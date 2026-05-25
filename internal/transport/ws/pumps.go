package ws

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/coder/websocket"

	"github.com/malhar/mafia-the-game/internal/room"
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
) {
	defer cancel() // signal writePump to exit on any read failure

	conn.SetReadLimit(readLimit)

	for {
		// Read one full message. coder/websocket returns the type
		// (text/binary) and the payload as a byte slice.
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
			sendErrorViaRoom(ctx, r, sub, "bad_frame", "expected text frame")
			continue
		}

		tag, payload, err := decodeClientMessage(data)
		if err != nil {
			sendErrorViaRoom(ctx, r, sub, "bad_message", err.Error())
			continue
		}

		if err := dispatchClientMessage(ctx, r, sub, tag, payload); err != nil {
			// dispatchClientMessage returns errors only for transport-
			// level failures (room closed, ctx cancelled). Stop the
			// pump in that case.
			logger.Info("ws dispatch ended", "err", err)
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

	out := sub.Outbound()
	for {
		select {
		case <-ctx.Done():
			return
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
					logger.Warn("dropping unknown outbound", "err", err)
					continue
				}
				logger.Warn("encode outbound failed", "err", err)
				continue
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
	switch tag {
	case clientMsgJoin:
		d := payload.(clientJoinData)
		return r.SubmitJoin(ctx, sub, d.Name)

	case clientMsgNightAction, clientMsgVote, clientMsgStartGame, clientMsgAdvancePhase:
		cmd, ok := commandFromClient(tag, payload)
		if !ok {
			// Should be unreachable — commandFromClient handles every
			// non-join client message. We send an error frame just in
			// case a future tag is added without wiring.
			sendErrorViaRoom(ctx, r, sub, "internal", "unhandled command tag")
			return nil
		}
		return r.SubmitCommand(ctx, sub, cmd)

	default:
		// decodeClientMessage already validates tags, so this branch is
		// a belt-and-braces guard.
		sendErrorViaRoom(ctx, r, sub, "bad_message", "unknown type")
		return nil
	}
}

// sendErrorViaRoom delivers an OutError to the subscriber. Transport-
// level errors (bad JSON, unknown frame type) bypass the room's run
// loop — they're not engine state — but still ride the subscriber's
// outbound channel so they arrive in order with concurrent broadcasts.
//
// If the channel is full or already closed, the error is dropped: the
// room would also disconnect a slow subscriber in this case, so a lost
// error is the lesser problem.
func sendErrorViaRoom(_ context.Context, _ *room.Room, sub *room.Subscriber, code, msg string) {
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
