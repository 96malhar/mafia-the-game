package room

import (
	"fmt"
	"runtime/debug"
	"time"

	"github.com/96malhar/mafia-the-game/internal/game"
)

// This file implements in-memory panic recovery (let-it-crash supervision)
// for the room goroutine.
//
// Without it, an unhandled panic anywhere under the run loop — a nil-map
// deref in a future role handler, a bad type assertion in projection — would
// propagate to the top of the goroutine and terminate the WHOLE process,
// killing every other in-flight game. The one-goroutine-per-room design is
// meant to isolate rooms, but a panic defeats that isolation.
//
// Recovery exploits the existing strength: the engine is a pure,
// deterministic function of its command stream. We keep a per-room COMMAND
// journal (the engine can only replay from commands — there is no
// fold-from-events path) paired with the exact events each command committed
// to the log. On panic we:
//
//  1. log the panic + stack and bump a metric (a panic is always a real bug
//     worth surfacing loudly),
//  2. rebuild a fresh *game.Game AND a fresh event log by replaying the
//     journal — never reusing the live engine, whose state may be
//     half-mutated by a panic mid-Apply,
//  3. re-arm the night timer so the game keeps advancing, and
//  4. force every connected client to reconnect+rejoin for a clean snapshot,
//     repairing any half-delivered fan-out from the interrupted batch.
//
// The goroutine never exits, so room ownership stays exactly where it was —
// no Manager hand-off, no shared recovery-state map, and the lock-free
// single-owner model is untouched. A crash-loop guard closes the room if a
// deterministic re-panic would otherwise spin.

const (
	// maxRecoveriesPerWindow bounds how many panics a single room may
	// recover from within recoveryWindow before we give up and close it.
	// A deterministic re-panic (e.g. a timer that fires AdvancePhase into a
	// reproducible bug) would otherwise loop forever; closing the one room
	// is strictly better than spinning, and still isolates the blast radius
	// to that game.
	maxRecoveriesPerWindow = 5
	recoveryWindow         = 10 * time.Second
)

// journalEntry is one applied command paired with the exact events the room
// committed to its log for it. Recording both is what lets the room rebuild
// the live (engine, event-log) pair after a panic WITHOUT re-deriving any
// room-layer logic:
//
//   - cmd rebuilds the engine: the engine replays only from commands, and
//     Apply is deterministic (the shuffle seed rides inside CreateGame /
//     ResetGame), so replaying the commands reproduces identical state.
//   - logged rebuilds the event log: it carries the room-layer facts the
//     engine never emits — the injected HostChanged, the stamped wall-clock
//     deadlines — exactly as they were first committed.
//   - rebaseline marks a ResetGame, whose committed events REPLACE the log
//     (a fresh lobby baseline) rather than appending to it, mirroring
//     handleReset's r.events = events.
type journalEntry struct {
	cmd        game.Command
	logged     []game.Event
	rebaseline bool
}

// record appends a command and the events it committed to the recovery
// journal. Callers invoke it AFTER the command's events have been committed
// to r.events (and, for the broadcast path, after stampNightDeadlines has
// stamped them in place), so logged carries the final, faithful events. A
// command is journaled only if it was applied successfully — a rejected
// command never reaches here.
func (r *Room) record(cmd game.Command, logged []game.Event, rebaseline bool) {
	r.journal = append(r.journal, journalEntry{cmd: cmd, logged: logged, rebaseline: rebaseline})
}

// guard runs fn and returns the value of any panic it raises (nil if fn
// returned normally). It only CATCHES the panic; the caller decides what to
// do, so guard can wrap both the main loop's work and the rebuild itself
// without the two recovery paths becoming entangled.
func guard(fn func()) (recovered any) {
	defer func() { recovered = recover() }()
	fn()
	return nil
}

// recoverFromPanic handles a panic caught in the run loop: it logs loudly,
// counts toward the crash-loop budget, and rebuilds the room from its
// journal. It returns true if the room recovered and the loop should
// continue, or false if the room is unrecoverable (budget exhausted, or the
// rebuild itself panicked) and has been cancelled so the loop should exit.
func (r *Room) recoverFromPanic(where string, p any) bool {
	r.log.Error("recovered from panic in room goroutine",
		"where", where, "panic", fmt.Sprint(p), "stack", string(debug.Stack()))
	recordRoomPanic()

	if r.overRecoveryBudget() {
		r.log.Error("room exceeded its recovery budget; closing for safety",
			"window", recoveryWindow, "max", maxRecoveriesPerWindow)
		r.cancel()
		return false
	}

	// The rebuild reconstructs from a fixed, immutable journal, so it should
	// never panic — but a truly corrupt journal (or a latent engine bug that
	// re-panics on replay) must not crash the process. Guard it and close
	// the room cleanly if it does.
	if p2 := guard(r.rebuildAndResync); p2 != nil {
		r.log.Error("room rebuild from journal panicked; closing",
			"panic", fmt.Sprint(p2), "stack", string(debug.Stack()))
		r.cancel()
		return false
	}
	return true
}

// overRecoveryBudget reports whether this room has recovered too many times
// too quickly, using a fixed window so transient, well-spaced panics never
// trip it but a tight re-panic loop does. It records the current recovery as
// a side effect.
func (r *Room) overRecoveryBudget() bool {
	now := time.Now()
	if r.recoveries == 0 || now.Sub(r.recoveryWindowStart) > recoveryWindow {
		r.recoveryWindowStart = now
		r.recoveries = 1
		return false
	}
	r.recoveries++
	return r.recoveries > maxRecoveriesPerWindow
}

// rebuildAndResync rebuilds the engine and event log from the journal, re-arms
// the night timer, and forces a client resync. It panics (to be caught by the
// guard in recoverFromPanic) if the journal cannot be replayed.
func (r *Room) rebuildAndResync() {
	if err := r.rebuildFromJournal(); err != nil {
		panic(fmt.Sprintf("journal replay failed: %v", err))
	}
	r.rearmTimerAfterRebuild()
	r.resyncSubscribers()
}

// rebuildFromJournal reconstructs r.g and r.events from the recovery journal,
// replacing the live (possibly corrupt) values. The engine is rebuilt by
// replaying every command into a fresh game.New(); the event log is rebuilt
// by folding each entry's committed events (respecting the ResetGame
// rebaseline). The two reconstructions are independent but consistent by
// construction, because both derive from the same recorded history.
//
// It does NOT touch subscribers or timers — those are handled separately by
// rebuildAndResync — so a failure here leaves the live state untouched for
// the caller to close the room on.
func (r *Room) rebuildFromJournal() error {
	g, events, err := r.replayJournal()
	if err != nil {
		return err
	}
	r.g = g
	r.events = events
	return nil
}

// replayJournal is the pure core of rebuildFromJournal: it replays the
// journal into a fresh engine and event log and RETURNS them without touching
// the live room. Kept separate so tests can assert that a replay reproduces
// the live (engine, log) pair without destroying the running room.
func (r *Room) replayJournal() (*game.Game, []game.Event, error) {
	g := game.New()
	var events []game.Event
	for i, e := range r.journal {
		if _, err := g.Apply(e.cmd); err != nil {
			return nil, nil, fmt.Errorf("journal entry %d (%T): %w", i, e.cmd, err)
		}
		if e.rebaseline {
			// A ResetGame rebaselines the log: its committed events become the
			// new beginning of time, exactly as handleReset does live.
			events = append([]game.Event(nil), e.logged...)
		} else {
			events = append(events, e.logged...)
		}
	}
	return g, events, nil
}

// rearmTimerAfterRebuild re-arms the night sub-phase timer after a rebuild so
// the night keeps advancing — without it, a room that panicked mid-night
// would freeze (no timer ever fires AdvancePhase again). It arms a fresh full
// duration for the current sub-phase; because in-memory recovery is
// effectively instantaneous, the new timer and the deadline already stamped
// on the last NightSubPhaseStarted agree closely enough for the client
// countdown.
//
// The room can't read the engine's phase directly (GameState's accessors are
// test-only), so it infers the active timer from the log the same way
// scheduleTimers does — walking backwards, the first event that defines the
// current context wins: a NightSubPhaseStarted means we're mid-night (arm
// it); a PhaseChanged that left night means there's no sub-phase timer.
func (r *Room) rearmTimerAfterRebuild() {
	r.stopPhaseTimer()
	for i := len(r.events) - 1; i >= 0; i-- {
		switch e := r.events[i].(type) {
		case game.NightSubPhaseStarted:
			r.armSubPhaseTimer(e)
			return
		case game.PhaseChanged:
			if e.To != game.PhaseNight {
				return // most recent phase boundary left night: no timer
			}
			// A PhaseChanged INTO night is immediately followed by its
			// opening NightSubPhaseStarted, which a backward scan hits
			// first; reaching this means keep scanning.
		}
	}
}

// resyncSubscribers forces every connected client to reconnect and rejoin so
// each pulls a fresh authoritative snapshot from the rebuilt log. This
// repairs any half-delivered fan-out from the batch the panic interrupted —
// some subscribers may have seen events that the rebuild rolled back, or
// missed events it kept. Player slots are preserved by the rebuild, so the
// clients' auto-reconnect re-authenticates against their existing secrets and
// re-attaches seamlessly.
func (r *Room) resyncSubscribers() {
	for _, slot := range r.players {
		slot.sub = nil
	}
	// shutdownSubscribers detaches every subscriber (closing its outbound
	// channel); the transport write pump then unwinds and the client's
	// auto-reconnect kicks in. r.subs is left empty.
	r.shutdownSubscribers()
}
