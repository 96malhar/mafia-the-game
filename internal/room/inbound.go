// Package room hosts the multi-player coordination layer that sits
// between the pure game engine (internal/game) and the network
// transport (internal/server's WebSocket handler).
//
// A Room owns one *game.Game, a stable event log, and a set of
// Subscribers (one per WebSocket connection). It runs in its own
// goroutine and is the SOLE mutator of its state; everything else
// communicates with it via channels.
//
// The Manager owns the room registry and rejoin credentials.
//
// This layer adds:
//   - identity / auth (rejoin codes, host designation)
//   - per-subscriber projection + broadcast
//   - phase timers (added in a later sub-step)
//
// while keeping the engine (game.Game) and the transport (server)
// independent of each other.
package room

import "github.com/96malhar/mafia-the-game/internal/game"

// inbound is the closed sum type of messages a subscriber can send to a
// room. The room's select loop dispatches on the concrete type.
//
// Like Command in the engine, this is a closed interface (unexported
// marker) so callers can't invent new shapes — every inbound kind must
// be added here.
type inbound interface {
	isInbound()
}

// inJoin attaches a brand-new subscriber to the room. The subscriber
// has no PlayerID yet; the room assigns one and replies with OutJoined
// (which includes a rejoin secret for future reconnects).
//
// Sent by: the WebSocket handler when a client connects without a
// rejoin token.
type inJoin struct {
	From *Subscriber
	Name string
}

func (inJoin) isInbound() {}

// inRejoin attempts to attach a subscriber to an existing player slot
// using the secret returned at original join time. If the secret
// doesn't match, the subscriber is sent an outError and disconnected.
type inRejoin struct {
	From     *Subscriber
	PlayerID game.PlayerID
	Secret   string
	// Since is the client's resume cursor: the highest event sequence it has
	// already applied. The room replies with only the projected tail after
	// this point (a delta); 0 — or a value past the current log, e.g. after a
	// reset — yields the full projected log instead. See OutRejoined.
	Since int
}

func (inRejoin) isInbound() {}

// inLeave is sent when a subscriber disconnects. The room marks the
// player as detached (in v1: same as "still in game; can rejoin").
// We do NOT remove the player from the game state on disconnect — that
// would let players evade losing positions.
type inLeave struct {
	From *Subscriber
}

func (inLeave) isInbound() {}

// inCommand wraps a game.Command for the room to apply. The PlayerID
// on the source subscriber is authoritative — the room rewrites any
// player-identity fields on the command (Actor, Voter) to match. This
// prevents a malicious client from acting as another player even if
// they know that player's ID.
type inCommand struct {
	From *Subscriber
	Cmd  game.Command
}

func (inCommand) isInbound() {}

// inLifetimeCheck is sent by the manager's sweeper goroutine to ask
// the room to self-evaluate its age against cfg.MaxLifetime. The
// room receives this on its inbox so the check happens inside the
// run loop (no concurrent reads of run-loop-only state). If the
// room decides it's past the cap, it cancels its own context and
// exits; the manager's reapWhenDone goroutine then removes it from
// the registry.
type inLifetimeCheck struct{}

func (inLifetimeCheck) isInbound() {}

// inJoinability asks the room, on its own goroutine, whether a fresh join
// would currently be accepted. The room replies on the (buffered) reply
// channel with the engine's join-block reason — nil when a join would
// succeed. It backs the CheckRoom HTTP probe so the lobby can flip a share
// link to "create a new room" the moment the target can't be joined, without
// opening a doomed WebSocket. Read-only: it never mutates room state.
type inJoinability struct {
	reply chan error
}

func (inJoinability) isInbound() {}

// inTestHook carries a closure for the room to run on its own goroutine.
// It is a TEST-ONLY seam — nothing in production constructs one. It lets
// white-box recovery tests read run-loop-only state race-free (the closure
// rides the inbox like any message, so the channel handoff synchronizes
// it), and trigger a panic ON the room goroutine to exercise the recover
// path without needing a real engine bug. Carrying the closure in the
// message (rather than a shared package var) is what keeps it race-free
// under -race.
type inTestHook struct {
	fn func(*Room)
}

func (inTestHook) isInbound() {}
