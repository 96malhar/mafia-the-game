package room

import (
	"time"

	"github.com/malhar/mafia-the-game/internal/game"
)

// This file owns the room → subscribers fan-out path and the
// per-connection lifecycle helpers (attach/detach/shutdown).
//
// appendAndBroadcast is the central choke point: every engine
// event the room emits flows through here in batches. It does four
// things in order:
//
//  1. stampNightDeadlines: rewrites NightTurnStarted.Deadline from 0
//     (the engine is timeless) to a wall-clock millis value so all
//     viewers — current and future, via the replayed log — see the
//     same timing.
//  2. r.events = append(...): commits the batch to the canonical log.
//  3. broadcastToSubs: projects the batch per viewer and writes to
//     each subscriber's outbound channel; slow subs are dropped.
//  4. scheduleTimers: scans the batch's structural events
//     (PhaseChanged, NightTurnStarted, etc.) and (re)arms timers.

// appendAndBroadcast records events to the canonical log, fans them
// out to all subscribers (projected per viewer), and (re)arms any
// phase/turn timers implied by the batch.
//
// If a subscriber's outbound buffer is full, we consider them too slow
// and disconnect them; the room continues. This is a hard "fail closed"
// stance — better to drop a flaky connection than to back-pressure the
// whole room.
func (r *Room) appendAndBroadcast(events []game.Event) {
	if len(events) == 0 {
		return
	}

	lastNightTurnPhantom := r.stampNightDeadlines(events)
	r.events = append(r.events, events...)
	r.broadcastToSubs(events)
	r.scheduleTimers(events, lastNightTurnPhantom)
}

// stampNightDeadlines rewrites any NightTurnStarted events in the
// batch to carry a wall-clock deadline. The engine emits them with
// Deadline=0 because it is intentionally timeless; the room is the
// authoritative clock.
//
// We do this BEFORE appending to r.events so the canonical log
// stores the real deadlines — late joiners reconstructing state from
// the projected event stream see the same timing original viewers
// saw.
//
// Returns the Phantom flag from the LAST NightTurnStarted in the
// batch, so the timer-arming pass can pick the right timer mode
// (real grace+action vs. shorter phantom window). If no
// NightTurnStarted is in the batch the return value is unused.
func (r *Room) stampNightDeadlines(events []game.Event) (lastPhantom bool) {
	// State.Day() at this point reflects the night currently in
	// progress: Day 0 for the first night, Day 1 for the second, etc.
	// We pass it to nightTurnDuration so the grace can scale (Night 1
	// mafia gets a longer audio grace for the "look around" beat).
	day := r.g.State().Day()
	for i := range events {
		ts, ok := events[i].(game.NightTurnStarted)
		if !ok {
			continue
		}
		if ts.Deadline == 0 {
			dur := r.cfg.nightTurnDuration(ts.Role, day, ts.Phantom)
			ts.Deadline = time.Now().Add(dur).UnixMilli()
			events[i] = ts
		}
		lastPhantom = ts.Phantom
	}
	return lastPhantom
}

// broadcastToSubs projects the batch per viewer and writes it to
// each subscriber's outbound channel. A subscriber whose channel is
// full is treated as too slow and disconnected — better to drop one
// flaky connection than block the whole room.
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

// scheduleTimers inspects the structural events in the batch
// (PhaseChanged, NightTurnStarted, NightTurnEnded, DetectiveResult)
// and (re)arms the room's single phaseTimer in the appropriate mode.
// See timers.go for the three modes.
func (r *Room) scheduleTimers(events []game.Event, lastNightTurnPhantom bool) {
	var phaseChanged, nightTurnStarted, nightTurnEnded, detectiveResult bool
	for _, e := range events {
		switch e.(type) {
		case game.PhaseChanged:
			phaseChanged = true
		case game.NightTurnStarted:
			nightTurnStarted = true
		case game.NightTurnEnded:
			nightTurnEnded = true
		case game.DetectiveResult:
			detectiveResult = true
		}
	}

	if phaseChanged {
		r.resetPhaseTimer()
	}
	if nightTurnStarted {
		r.resetNightTurnTimer(lastNightTurnPhantom)
	}
	if nightTurnEnded && !nightTurnStarted && !phaseChanged {
		// NightTurnEnded with no immediate NightTurnStarted is the
		// engine signalling a deliberate pause — currently this only
		// happens after a detective action. We arm a short timer
		// here; when it fires, handlePhaseTimer sends AdvancePhase
		// which pops the next queued role (engine's advanceFromNight
		// handles the "currentNightRole=='' but queue non-empty"
		// case as "start next turn"). Without this timer the night
		// would silently hang.
		//
		// If something else ever produces NightTurnEnded without
		// NightTurnStarted, double-check this branch — for now,
		// detectiveResult is the only known producer.
		if detectiveResult {
			r.armDetectivePauseTimer()
		} else {
			r.stopPhaseTimer()
		}
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
