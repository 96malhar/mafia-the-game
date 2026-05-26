package room

import (
	"time"

	"github.com/malhar/mafia-the-game/internal/game"
)

// This file collects everything that interacts with r.phaseTimer.
//
// There is exactly ONE timer field on a Room (phaseTimer), but it is
// armed in three semantically distinct modes:
//
//  1. Per-turn deadline during PhaseNight (resetNightTurnTimer).
//     Armed whenever a NightTurnStarted is broadcast; fires
//     handlePhaseTimer → AdvancePhase, which ends the role's turn.
//
//  2. Detective read-modal pause (armDetectivePauseTimer).
//     Armed when the engine emits a NightTurnEnded after a detective
//     action *without* starting the next turn (see rules_night.go).
//     The fixed short window gives the detective a beat to read
//     their result; firing it advances to the next queued role.
//
//  3. Day phases: never armed. PhaseLobby, PhaseDayDiscussion,
//     PhaseDayVote, and PhaseEnded are all host-driven via explicit
//     commands; resetPhaseTimer just clears any inherited timer on
//     transition so a stale Night/detective timer can't leak in.
//
// All three modes share the same fire path (handlePhaseTimer →
// AdvancePhase), which means each "what does AdvancePhase do here?"
// decision lives in the engine, not here.

// handlePhaseTimer fires when phaseTimer expires. Synthesizes an
// AdvancePhase to push the game forward:
//
//   - In a night turn, it ends the current role's turn.
//   - In the detective pause window, it pops the next queued role.
//
// appendAndBroadcast handles arming the next timer in either case
// (per-turn during Night, or the detective pause), so we don't
// re-arm here.
func (r *Room) handlePhaseTimer() {
	r.phaseTimer = nil
	events, err := r.g.Apply(game.AdvancePhase{})
	if err != nil {
		// AdvancePhase fails in PhaseEnded; not a timer-level error.
		r.log.Debug("phase timer advance rejected", "err", err)
		return
	}
	r.appendAndBroadcast(events)
}

// resetPhaseTimer clears any active phase-level timer on a phase
// transition.
//
// Day phases (PhaseDayDiscussion, PhaseDayVote) are entirely host-
// driven (BeginNight / OpenVoting / ClearVotes / FinalizeVotes), so
// they never carry an auto-advance timer. PhaseNight uses per-turn
// timers (resetNightTurnTimer), keyed off NightTurnStarted events
// rather than the phase entry. PhaseLobby and PhaseEnded are
// untimed by design. That leaves nothing to set up here — but we
// still stop any inherited timer so e.g. a lingering detective-pause
// from the previous night doesn't fire into a new phase.
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

// armDetectivePauseTimer schedules the next-turn kickoff that the
// engine intentionally didn't issue inside applyNightAction (see
// rules_night.go's detective branch). The timer fires AdvancePhase
// via handlePhaseTimer, which pops the next queued night role.
func (r *Room) armDetectivePauseTimer() {
	r.stopPhaseTimer()
	dur := r.cfg.DetectivePauseDuration
	if dur <= 0 {
		// Misconfigured to zero — fall back to immediate advance
		// rather than hanging the night.
		dur = time.Millisecond
	}
	r.phaseTimer = time.NewTimer(dur)
}

// resetNightTurnTimer arms the per-turn timer for the role that has
// just become active. The duration matches the deadline we stamped
// onto the outbound NightTurnStarted event a moment ago, so the
// server and clients agree on when this turn ends.
//
// We read the role and day from engine state rather than the event
// because by this point in appendAndBroadcast the engine has already
// applied them and CurrentNightRole() is the freshly-started role.
// The phantom flag is passed in by appendAndBroadcast (sourced from
// the NightTurnStarted event) — phantom turns use the shorter
// PhantomTurnDuration instead of grace + action.
func (r *Room) resetNightTurnTimer(phantom bool) {
	r.stopPhaseTimer()
	role := r.g.State().CurrentNightRole()
	day := r.g.State().Day()
	dur := r.cfg.nightTurnDuration(role, day, phantom)
	if dur <= 0 {
		return
	}
	r.phaseTimer = time.NewTimer(dur)
}
