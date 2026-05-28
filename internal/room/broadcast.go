package room

import (
	"time"

	"github.com/malhar/mafia-the-game/internal/game"
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
	// All sizing context (role, day, phantom-vs-real) rides on the
	// event itself. `submitted` is the only extra signal — it tells
	// subPhaseDuration whether a Ponder beat is after a real
	// submission vs a timeout, which today's defaults treat
	// identically but an override may not. Engine state, post-Apply,
	// already reflects the just-applied command's nightSubmitted
	// flag, so reading it here is correct.
	submitted := r.g.State().NightTurnSubmitted()

	for i := range events {
		dur := r.cfg.subPhaseDuration(events[i], submitted)
		if dur <= 0 {
			continue
		}
		deadline := now.Add(dur).UnixMilli()
		events[i] = stampDeadline(events[i], deadline)
	}
}

// stampDeadline returns a copy of evt with its Deadline field set to
// the given unix-millis value. Events that don't have a Deadline are
// passed through unchanged. Splitting this out from
// stampNightDeadlines keeps the per-type field assignments readable
// and isolates the type switch from the timing math.
func stampDeadline(evt game.Event, deadline int64) game.Event {
	switch e := evt.(type) {
	case game.NightOpeningStarted:
		if e.Deadline == 0 {
			e.Deadline = deadline
		}
		return e
	case game.NightNarrationStarted:
		if e.Deadline == 0 {
			e.Deadline = deadline
		}
		return e
	case game.NightActionStarted:
		if e.Deadline == 0 {
			e.Deadline = deadline
		}
		return e
	case game.NightPonderStarted:
		if e.Deadline == 0 {
			e.Deadline = deadline
		}
		return e
	case game.NightSleepStarted:
		if e.Deadline == 0 {
			e.Deadline = deadline
		}
		return e
	case game.NightSettleStarted:
		if e.Deadline == 0 {
			e.Deadline = deadline
		}
		return e
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
		case game.NightOpeningStarted,
			game.NightNarrationStarted,
			game.NightActionStarted,
			game.NightPonderStarted,
			game.NightSleepStarted,
			game.NightSettleStarted:
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

// disconnectSlow drops a slow subscriber from the room and closes its
// outbound channel. The player slot is NOT removed — they can rejoin.
func (r *Room) disconnectSlow(sub *Subscriber) {
	r.log.Warn("disconnecting slow subscriber", "player", sub.PlayerID())
	pid := sub.PlayerID()
	if slot, ok := r.players[pid]; ok && slot.sub == sub {
		slot.sub = nil
	}
	r.detachSubscriber(sub)
}

// attachSubscriber adds a subscriber to r.subs. Helper exists for
// symmetry with detachSubscriber and as the obvious extension point
// if we ever bring back subscriber-based reap policies.
func (r *Room) attachSubscriber(sub *Subscriber) {
	r.subs[sub] = struct{}{}
}

// detachSubscriber removes a subscriber from r.subs and closes its
// outbound channel. The close() is safe to call once and only once
// per subscriber — all call sites (handleLeave, handleRejoin's
// eviction path, disconnectSlow) gate on r.subs membership to
// enforce that.
func (r *Room) detachSubscriber(sub *Subscriber) {
	if _, ok := r.subs[sub]; !ok {
		return
	}
	delete(r.subs, sub)
	close(sub.out)
}

// shutdownSubscribers closes every connected subscriber's channel on
// room exit. Called via defer in Run.
func (r *Room) shutdownSubscribers() {
	for sub := range r.subs {
		close(sub.out)
		delete(r.subs, sub)
	}
}
