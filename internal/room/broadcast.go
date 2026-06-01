package room

import (
	"time"

	"github.com/96malhar/mafia-the-game/internal/game"
)

// This file owns the room → subscribers fan-out path and the
// per-connection lifecycle helpers (attach/detach/shutdown).
//
// appendAndBroadcast is the central choke point: every engine event
// the room emits flows through here in batches. It does four things
// in order:
//
//  1. stampNightDeadlines: rewrites Night*Started.Deadline from 0
//     (the engine is timeless) to a wall-clock millis value so all
//     viewers — current and future, via the replayed log — see the
//     same timing.
//  2. r.events = append(...): commits the batch to the canonical log.
//  3. broadcastToSubs: projects the batch per viewer and writes to
//     each subscriber's outbound channel; slow subs are dropped.
//  4. scheduleTimers: scans the batch for Night sub-phase events and
//     (re)arms the room's single phaseTimer to fire at the active
//     sub-phase's deadline.

// appendAndBroadcast records events to the canonical log, fans them
// out to all subscribers (projected per viewer), and (re)arms the
// night sub-phase timer implied by the batch.
//
// If a subscriber's outbound buffer is full, we consider them too
// slow and disconnect them; the room continues. This is a hard
// "fail closed" stance — better to drop a flaky connection than to
// back-pressure the whole room.
func (r *Room) appendAndBroadcast(events []game.Event) {
	if len(events) == 0 {
		return
	}

	r.stampNightDeadlines(events)
	r.events = append(r.events, events...)
	r.broadcastToSubs(events)
	r.scheduleTimers(events)
}

// stampNightDeadlines rewrites the Deadline field on every Night
// sub-phase-started event in the batch to carry a wall-clock millis
// value. The engine emits them with Deadline=0 because it is
// intentionally timeless; the room is the authoritative clock.
//
// We do this BEFORE appending to r.events so the canonical log
// stores the real deadlines — late joiners reconstructing state from
// the projected event stream see the same timing original viewers
// saw.
//
// The function mutates each event in place by replacing the slice
// element (events are values, not pointers). All six sub-phase
// events share the same Deadline+Day shape; we type-switch and
// rebuild each one with the stamped deadline. Day comes from the
// event itself (it's always set by the engine), so this function
// never reads engine state.
func (r *Room) stampNightDeadlines(events []game.Event) {
	now := time.Now()
	// All sizing context (role, day, phantom-vs-real) rides on the event
	// itself — including the blocked case, which the engine routes through
	// a phantom ponder (no act window), so no extra engine-state signal is
	// needed here.
	for i := range events {
		dur := r.cfg.subPhaseDuration(events[i])
		if dur <= 0 {
			continue
		}
		deadline := now.Add(dur).UnixMilli()
		events[i] = stampDeadline(events[i], deadline)
	}
}

// stampDeadline returns a copy of evt with its Deadline field set to
// the given unix-millis value. Non-night-sub-phase events (which carry
// no Deadline) pass through unchanged. The copy-with-deadline lives on
// the event itself (game.NightSubPhaseStarted.WithDeadline) so this
// stays a single type assertion.
func stampDeadline(evt game.Event, deadline int64) game.Event {
	if e, ok := evt.(game.NightSubPhaseStarted); ok {
		return e.WithDeadline(deadline)
	}
	return evt
}

// broadcastToSubs projects the batch per viewer and writes it to each
// subscriber's outbound channel. A subscriber whose channel is full
// is treated as too slow and disconnected — better to drop one flaky
// connection than block the whole room.
func (r *Room) broadcastToSubs(events []game.Event) {
	for sub := range r.subs {
		viewer := sub.PlayerID()
		filtered := game.Project(viewer, events, r.g.State())
		for _, e := range filtered {
			if !r.sendOne(sub, OutEvent{Event: e}) {
				r.disconnectSlow(sub)
				break
			}
		}
	}
}

// scheduleTimers inspects the batch for night sub-phase events and
// (re)arms the room's single phaseTimer. There's exactly one active
// deadline at any time during PhaseNight; the latest sub-phase event
// in the batch wins (the engine emits at most one per batch, but
// applyNightAction can emit DetectiveResult + NightPonderStarted in
// one shot, so we still scan).
//
// On PhaseChanged we clear any active timer first so a stale night
// timer can't leak across a phase change. PhaseDayDiscussion /
// PhaseDayVote are host-driven and have no timer.
func (r *Room) scheduleTimers(events []game.Event) {
	var phaseChanged bool
	var lastSubPhaseEvent game.Event
	for _, e := range events {
		switch e.(type) {
		case game.PhaseChanged:
			phaseChanged = true
		case game.NightSubPhaseStarted:
			// Any night sub-phase start (opening/narrate/act/ponder/
			// sleep/settle) — they all share this one event type now,
			// distinguished by its Sub field.
			lastSubPhaseEvent = e
		}
	}

	if phaseChanged {
		r.resetPhaseTimer()
	}
	if lastSubPhaseEvent != nil {
		r.armSubPhaseTimer(lastSubPhaseEvent)
	}
}

// sendOne attempts a non-blocking send to a subscriber. Returns true on
// success, false if the channel is full (subscriber too slow).
func (r *Room) sendOne(sub *Subscriber, msg Outbound) bool {
	select {
	case sub.out <- msg:
		return true
	default:
		return false
	}
}

// rejectUnjoined sends a terminal error to a subscriber that never
// attached (a failed join or rejoin auth) and then closes its outbound
// channel so the transport's write pump unwinds instead of parking on
// an empty channel forever. The error is queued before the close, so
// the write pump delivers it to the client and then sees the channel
// close (a clean shutdown). detachSubscriber marks the subscriber
// closed, so any stray inbound the read pump delivers after this is
// ignored by the dispatch guards.
func (r *Room) rejectUnjoined(sub *Subscriber, out OutError) {
	r.sendOne(sub, out)
	r.detachSubscriber(sub)
}

// disconnectSlow drops a slow subscriber from the room and closes its
// outbound channel. The player slot is NOT removed — they can rejoin.
func (r *Room) disconnectSlow(sub *Subscriber) {
	r.log.Warn("disconnecting slow subscriber", "player", sub.PlayerID())
	pid := sub.PlayerID()
	if slot, ok := r.players[pid]; ok && slot.sub == sub {
		slot.sub = nil
	}
	r.detachSubscriber(sub)
	// A dropped host (slow connection) also triggers the migration
	// countdown, same as a clean leave.
	r.maybeArmHostGrace()
}

// attachSubscriber adds a subscriber to r.subs. Helper exists for
// symmetry with detachSubscriber and as the obvious extension point
// if we ever bring back subscriber-based reap policies.
func (r *Room) attachSubscriber(sub *Subscriber) {
	r.subs[sub] = struct{}{}
}

// detachSubscriber removes a subscriber from r.subs and closes its
// outbound channel exactly once. Idempotency is enforced by the
// per-subscriber `closed` flag rather than r.subs membership, so it's
// also safe to call on a subscriber that never attached — e.g. a
// rejected join/rejoin (see rejectUnjoined). All callers run on the
// room goroutine, so the flag swap + delete + close are race-free with
// each other; the flag's atomicity only matters for the transport-side
// close-vs-send race.
func (r *Room) detachSubscriber(sub *Subscriber) {
	if sub.closed.Swap(true) {
		return // already closed
	}
	delete(r.subs, sub) // no-op if never attached
	close(sub.out)
}

// shutdownSubscribers closes every connected subscriber's channel on
// room exit. Called via defer in Run. Routes through detachSubscriber
// so the close-once invariant (and the `closed` flag) is honored.
func (r *Room) shutdownSubscribers() {
	for sub := range r.subs {
		r.detachSubscriber(sub)
	}
}
