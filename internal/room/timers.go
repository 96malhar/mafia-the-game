package room

import (
	"errors"
	"time"

	"github.com/malhar/mafia-the-game/internal/game"
)

// This file collects everything that interacts with r.phaseTimer.
//
// There is exactly ONE timer field on a Room (phaseTimer). During
// PhaseNight it's armed for the active sub-phase (opening, narrate,
// act, ponder, sleep, or settle). When it fires, handlePhaseTimer
// sends an AdvancePhase command, which the engine uses to move to
// the next sub-phase — or, after the last role's settle, to resolve
// the night and transition to PhaseDayDiscussion.
//
// Day phases (PhaseLobby, PhaseDayDiscussion, PhaseDayVote, and
// PhaseEnded) are all host-driven via explicit commands; resetPhaseTimer
// just clears any inherited timer on transition so a stale night
// sub-phase timer can't leak in.

// handlePhaseTimer fires when phaseTimer expires. It synthesizes an
// AdvancePhase command to push the night sub-phase machine forward
// (see engine's advanceNightSubPhase for the transition graph).
// appendAndBroadcast handles arming the timer for whatever sub-phase
// the engine moves into, so we don't re-arm here.
func (r *Room) handlePhaseTimer() {
	r.phaseTimer = nil
	events, err := r.g.Apply(game.AdvancePhase{})
	if err != nil {
		// AdvancePhase legitimately fails only in PhaseEnded (the
		// game ended on the edge that armed this timer) — that's an
		// expected, benign race, logged at debug. Any OTHER rejection
		// means a timer fired in a phase the engine considers untimed,
		// which would silently stall the night; surface it at warn so
		// the inconsistency is visible.
		if errors.Is(err, game.ErrGameEnded) {
			r.log.Debug("phase timer advance rejected (game ended)", "err", err)
		} else {
			r.log.Warn("phase timer advance rejected unexpectedly", "err", err)
		}
		return
	}
	r.appendAndBroadcast(events)
}

// resetPhaseTimer clears any active phase-level timer on a phase
// transition. Called by scheduleTimers when a PhaseChanged appears
// in the batch.
//
// Day phases (PhaseDayDiscussion, PhaseDayVote) are entirely host-
// driven (BeginNight / OpenVoting / ClearVotes / FinalizeVotes), so
// they never carry an auto-advance timer. Within PhaseNight, the
// sub-phase timer gets armed afterward (in scheduleTimers) by
// armSubPhaseTimer once the new sub-phase event is identified.
// PhaseLobby and PhaseEnded are untimed by design.
func (r *Room) resetPhaseTimer() {
	r.stopPhaseTimer()
}

// stopPhaseTimer cleanly stops phaseTimer if it is running. Safe to
// call repeatedly. Necessary on phase changes (so the new timer
// doesn't double up) and on shutdown.
func (r *Room) stopPhaseTimer() {
	if r.phaseTimer == nil {
		return
	}
	// Stop returns false if the timer has already fired or been
	// stopped. In either case we don't need to drain — the run loop
	// only reads timer.C inside the same goroutine, so there's no
	// pending receive to worry about.
	r.phaseTimer.Stop()
	r.phaseTimer = nil
}

// armSubPhaseTimer arms phaseTimer for the duration of the sub-phase
// just started by `evt`. Duration is sourced from c.subPhaseDuration
// (the Default* constants, or the SubPhaseDurationOverride seam). The
// deadline stamped on the broadcast event uses the same source, so
// server and clients agree on when this sub-phase will end.
//
// The `submitted` flag is the only piece of context not carried on
// the event itself — it distinguishes a real-actor-submitted ponder
// from a real-actor-timed-out one, which the room may want to size
// differently. We read it from engine state, which (post-Apply)
// already reflects the just-applied command's nightSubmitted flag.
func (r *Room) armSubPhaseTimer(evt game.Event) {
	r.stopPhaseTimer()
	dur := r.cfg.subPhaseDuration(evt, r.g.State().NightTurnSubmitted())
	if dur <= 0 {
		return
	}
	r.phaseTimer = time.NewTimer(dur)
}
