package game

import "maps"

// applyAdvancePhase elapses the current night SUB-phase (PhaseNight
// only). It is an INTERNAL command, invoked by the room's wall-clock
// timer when the active sub-phase's deadline is reached. Daytime
// pacing is NOT driven by AdvancePhase: hosts use BeginNight /
// OpenVoting / ClearVotes / FinalizeVotes for those transitions.
//
// Each AdvancePhase advances exactly one sub-phase boundary. The five-
// step state machine per role turn is:
//
//	narrate ─▶ act ─[submit]─▶ ponder(short) ──▶ sleep ─▶ settle ─▶ next role
//	narrate ─▶ act ─[timer]──▶ ponder(short) ──▶ sleep ─▶ settle ─▶ next role
//	narrate ────────────────▶ ponder(random)──▶ sleep ─▶ settle ─▶ next role   (phantom)
//
// Submission (NightAction) drives the act→ponder edge directly; every
// other edge is driven by AdvancePhase. After the last role's settle,
// the engine runs resolveNight and transitions to PhaseDayDiscussion
// (or PhaseEnded if a faction won).
//
// Transition summary by current sub-phase:
//
// Every Night sub-phase transition emits a NightSubPhaseStarted whose
// Sub field names the sub-phase below (e.g. Sub=act, Sub=sleep):
//
//	Lobby / DayDiscussion / DayVote -> ErrWrongPhase
//	Ended                           -> ErrGameEnded
//	Night, narrate                  -> Sub=act (real) OR Sub=ponder (phantom)
//	Night, act    (timeout)         -> Sub=ponder
//	Night, ponder                   -> Sub=sleep
//	Night, sleep                    -> Sub=settle
//	Night, settle (midQueue)        -> Sub=narrate (next role)
//	Night, settle (lastRole)        -> resolveNight + PhaseChanged
//
// AdvancePhase received during NightSubAct is the timeout branch: no
// submission was recorded for this turn, but it STILL passes through
// ponder (exactly like a submit) so the submit/timeout cadence stays
// uniform — observers can't tell them apart.
func (g *Game) applyAdvancePhase(_ AdvancePhase) ([]Event, error) {
	if err := g.requireActiveGame(); err != nil {
		return nil, err
	}
	if g.state.phase != PhaseNight {
		// Day phases are host-driven; AdvancePhase is invalid here.
		return nil, ErrWrongPhase
	}
	return g.advanceNightSubPhase(), nil
}

// advanceNightSubPhase implements one tick of the per-role state
// machine described on applyAdvancePhase. It assumes:
//   - g.state.phase == PhaseNight, AND
//   - g.state.currentNightRole and currentNightSubPhase are non-zero
//     (i.e. we're mid-turn).
//
// If currentNightSubPhase is empty (shouldn't happen in normal flow)
// the function returns no events, leaving state untouched.
func (g *Game) advanceNightSubPhase() []Event {
	sub := g.state.currentNightSubPhase
	if sub == "" {
		// Defensive: shouldn't happen — beginNightTurns always
		// populates a sub-phase before returning. If it does, no-op
		// rather than panicking, since this is reachable from a
		// wall-clock timer that we don't want to be load-bearing on
		// engine invariants.
		return nil
	}
	// Note: currentNightRole IS empty during NightSubOpening (the
	// night-scoped beat that precedes any role's turn), so we do
	// NOT short-circuit on it.
	role := g.state.currentNightRole

	switch sub {
	case NightSubOpening:
		// Opening elapsed: pop the first role and enter its narrate.
		// nightTurnQueue is guaranteed non-empty here because
		// beginNightTurns populates it before entering opening.
		g.state.currentNightSubPhase = ""
		return g.beginNextNightTurn()

	case NightSubNarrate:
		// narrate → act (real) OR narrate → ponder (phantom).
		// roleTurnIsPhantom determines which branch; real turns wait
		// for either the actor's submission or this AdvancePhase
		// firing again at the end of NightSubAct. A turn is phantom
		// when the role has no living holder, its one-shot action is
		// spent (an out-of-bullets vigilante), or its holder was
		// roleblocked this night — in every case the actor can't do
		// anything, so the turn skips the act window rather than
		// stalling the night on it.
		if g.state.roleTurnIsPhantom(role) {
			return g.enterNightSubPhase(NightSubPonder)
		}
		return g.enterNightSubPhase(NightSubAct)

	case NightSubAct:
		// Reaching here means AdvancePhase fired during the act
		// window — i.e. the timer expired without a submission.
		// We still pass through ponder so the audio cadence and
		// sub-phase sequence are uniform across submit/timeout: the
		// ponder beat is sized the same either way, so observers can't
		// distinguish submit from timeout by listening alone.
		return g.enterNightSubPhase(NightSubPonder)

	case NightSubPonder:
		// ponder → sleep, both for real turns (post-submit) and
		// for phantom turns (post-narrate).
		return g.enterNightSubPhase(NightSubSleep)

	case NightSubSleep:
		// sleep → settle. Universal; runs after every role's sleep
		// including the last one (whose settle precedes the
		// night → day transition).
		return g.enterNightSubPhase(NightSubSettle)

	case NightSubSettle:
		// End of this role's turn. Pop the next role from the queue
		// and start it at narrate; or, if the queue is empty,
		// resolve the night and transition to DayDiscussion.
		g.state.currentNightRole = ""
		g.state.currentNightSubPhase = ""
		if len(g.state.nightTurnQueue) > 0 {
			return g.beginNextNightTurn()
		}
		return g.resolveAndExitNight()
	}
	return nil
}

// enterNightSubPhase mutates state to enter the given sub-phase for
// the current role and returns the matching event. Deadline is left
// at 0 — the room layer stamps a wall-clock value before broadcasting.
// Called from advanceNightSubPhase (timer-driven edges) and from
// applyNightAction (the act → ponder edge driven by submission).
func (g *Game) enterNightSubPhase(sub NightSubPhase) []Event {
	role := g.state.currentNightRole
	g.state.currentNightSubPhase = sub

	// One event shape covers every role-scoped sub-phase. Phantom is
	// only consumed for narrate/ponder, but computing it uniformly is
	// correct everywhere: enterNightSubPhase is never called for the
	// (role-less) opening, a living role's act is never phantom, and
	// sleep/settle carry the flag harmlessly (the wire encoder omits
	// it for those sub-phases). Deadline is left 0 — the room stamps a
	// wall-clock value before broadcasting.
	events := []Event{NightSubPhaseStarted{
		Sub:      sub,
		Role:     role,
		Day:      g.state.day,
		Deadline: 0,
		Phantom:  g.state.roleTurnIsPhantom(role),
	}}

	// A Consort-blocked actor's turn is phantom (no act window — see
	// roleTurnIsPhantom), so we deliver the private Blocked notice when
	// the cannot-act ponder begins, i.e. right AFTER the role's narrate
	// cue plays. This mirrors the old "told at the start of your action
	// beat" timing and lets the client show the notice + suppress the
	// picker. A real (unblocked) role reaches ponder only after acting
	// and is never blocked, so this never double-fires on a normal turn.
	// Mafia are immune (the block is a no-op against the faction kill).
	// resolveNight nullifies a blocked action as a further backstop.
	if sub == NightSubPonder && role != RoleMafia {
		if holder, ok := g.state.livingHolderOf(role); ok && g.state.isNightBlocked(holder) {
			events = append(events, Blocked{PlayerID: holder})
		}
	}
	return events
}

// resolveAndExitNight is the common Night-end path: it's called from
// advanceNightSubPhase when the last role's settle completes (queue
// empty). It runs resolveNight, checks for a win, and transitions to
// DayDiscussion (or PhaseEnded). The day counter is incremented when
// entering DayDiscussion. Sub-phase state must already be cleared by
// the caller before this runs (advanceNightSubPhase does this when
// it leaves NightSubSettle with an empty queue).
//
// DayDiscussion is entered with dayLynchResolved=false so the host's
// OpenVoting command is enabled; the only way out of DayDiscussion
// after that is the host pressing the appropriate button.
func (g *Game) resolveAndExitNight() []Event {
	events := g.resolveNight()

	// If the night's kills wiped out the last mafia but a consort still
	// lives, promote her to mafia BEFORE the win check — same "sleeper
	// takes over" rule as the lynch path (applyFinalizeVotes). This path
	// matters because the Vigilante can kill a mafioso at night, so the
	// cabal can now reach zero during night resolution, not only via a
	// lynch. Without this the consort would never inherit the kill and
	// the takeover would silently fail. No-op unless the cabal is wiped.
	events = append(events, g.promoteConsortIfNeeded()...)

	if events, ended := g.endGameIfWon(events); ended {
		return events
	}

	from := g.state.phase
	g.state.day++
	g.state.phase = PhaseDayDiscussion
	g.state.dayLynchResolved = false
	events = append(events, PhaseChanged{From: from, To: PhaseDayDiscussion, Day: g.state.day})
	return events
}

// applyBeginNight transitions into PhaseNight, kicking off the night
// turn sequence atomically. Valid from two places:
//
//  1. PhaseLobby AFTER StartGame has dealt roles. This starts Night 1
//     (day stays 0).
//  2. PhaseDayDiscussion AFTER a vote has been finalized for the day
//     (dayLynchResolved == true). This starts Night N+1 (day stays as
//     the just-resolved day number; resolveAndExitNight increments it
//     before transitioning to the next DayDiscussion).
//
// In both cases the engine emits PhaseChanged{To: PhaseNight} followed
// by NightOpeningStarted (the one-shot "City, go to sleep." beat).
// After the room's opening timer elapses, AdvancePhase drives the
// transition to the first role's NightSubNarrate; see NightSubPhase
// for the per-role state machine that follows.
func (g *Game) applyBeginNight(_ BeginNight) ([]Event, error) {
	if err := g.requireActiveGame(); err != nil {
		return nil, err
	}
	switch g.state.phase {
	case PhaseLobby:
		// Roles must have been dealt by StartGame first.
		if !g.state.rolesDealt {
			return nil, ErrWrongPhase
		}
	case PhaseDayDiscussion:
		// Only after a finalized vote — i.e. the room is between
		// "X was lynched" and the next night.
		if !g.state.dayLynchResolved {
			return nil, ErrWrongPhase
		}
	default:
		return nil, ErrWrongPhase
	}

	from := g.state.phase
	g.state.phase = PhaseNight
	g.state.votes = nil
	g.state.dayLynchResolved = false
	events := []Event{PhaseChanged{From: from, To: PhaseNight, Day: g.state.day}}
	events = append(events, g.beginNightTurns()...)
	return events, nil
}

// applyOpenVoting transitions PhaseDayDiscussion into PhaseDayVote.
// Valid only when no lynch has been resolved yet on this day (after a
// finalized vote, the day is effectively over and the only legal
// action is BeginNight).
func (g *Game) applyOpenVoting(_ OpenVoting) ([]Event, error) {
	if err := g.requirePhase(PhaseDayDiscussion); err != nil {
		return nil, err
	}
	if g.state.dayLynchResolved {
		return nil, ErrWrongPhase
	}
	from := g.state.phase
	g.state.phase = PhaseDayVote
	g.state.votes = make(map[PlayerID]PlayerID)
	g.state.votesRevealed = false
	return []Event{PhaseChanged{From: from, To: PhaseDayVote, Day: g.state.day}}, nil
}

// applyRevealVotes flips the current PhaseDayVote tally from hidden to
// public. Valid only in PhaseDayVote and only once per tally — a
// re-reveal is rejected with ErrNoChange (idempotent against the host
// double-clicking). On success it locks voting (further DayVote is
// rejected until a ClearVotes) and emits a single Public VotesRevealed
// event carrying a snapshot of the full voter→target map, so every
// viewer (alive or dead) can render who voted for whom.
func (g *Game) applyRevealVotes(_ RevealVotes) ([]Event, error) {
	if err := g.requirePhase(PhaseDayVote); err != nil {
		return nil, err
	}
	if g.state.votesRevealed {
		return nil, ErrNoChange
	}
	g.state.votesRevealed = true
	return []Event{VotesRevealed{Day: g.state.day, Tally: g.snapshotVotes()}}, nil
}

// snapshotVotes returns a copy of the current vote tally so the
// VotesRevealed event carries an immutable map that can't be mutated by
// later vote edits (there shouldn't be any post-reveal, but a copy keeps
// the event self-contained for replay). Always non-nil.
func (g *Game) snapshotVotes() map[PlayerID]PlayerID {
	out := make(map[PlayerID]PlayerID, len(g.state.votes))
	maps.Copy(out, g.state.votes)
	return out
}

// applyClearVotes wipes the in-flight vote tally (and undoes any
// reveal) so the room can re-vote from a clean, hidden slate. Stays in
// PhaseDayVote.
//
// Returns ErrNoChange only when there is genuinely nothing to do: no
// votes cast AND the tally hasn't been revealed. After a reveal, clear
// is always meaningful (it reopens hidden voting), even if the revealed
// tally was empty.
func (g *Game) applyClearVotes(_ ClearVotes) ([]Event, error) {
	if err := g.requirePhase(PhaseDayVote); err != nil {
		return nil, err
	}
	// Nothing to clear AND nothing revealed → no-op, reject to avoid
	// spamming the log with redundant VoteCleared events. But once the
	// host has revealed (even an empty tally), ClearVotes is meaningful:
	// it un-reveals and reopens hidden voting, which IS a state change.
	if len(g.state.votes) == 0 && !g.state.votesRevealed {
		return nil, ErrNoChange
	}
	g.state.votes = make(map[PlayerID]PlayerID)
	g.state.votesRevealed = false
	return []Event{VoteCleared{Day: g.state.day}}, nil
}

// applyFinalizeVotes resolves the current vote tally and ALWAYS ends
// the day. A lynch happens only if a single target reached a strict
// majority of the living population (see resolveDayVote); otherwise the
// day closes with NOBODY lynched. In both cases the phase returns to
// PhaseDayDiscussion with dayLynchResolved=true (so the only legal host
// command is BeginNight), or PhaseEnded if a lynch ends the game.
//
// Finalize is no longer rejected for an indecisive tally — pressing it
// is the host's explicit "close the day" action, and a town that can't
// muster a majority simply gets no lynch this round.
func (g *Game) applyFinalizeVotes(_ FinalizeVotes) ([]Event, error) {
	if err := g.requirePhase(PhaseDayVote); err != nil {
		return nil, err
	}

	target, decisive := g.resolveDayVote()

	// The vote is settled; clear the reveal flag so a future round
	// (after BeginNight → … → OpenVoting) starts hidden again. Harmless
	// on the game-ending path below.
	g.state.votesRevealed = false

	var events []Event

	// No strict majority: the day ends with nobody lynched. We still
	// advance out of PhaseDayVote so the host can proceed to the next
	// night. No win check is needed — the living roster is unchanged.
	if !decisive {
		events = append(events, NoLynch{Day: g.state.day}, g.endDayToDiscussion())
		return events, nil
	}

	if tp, ok := g.state.findPlayer(target); ok {
		tp.alive = false
	}
	events = append(events, PlayerLynched{PlayerID: target})

	// If that lynch wiped out the last mafia but a consort still lives,
	// she's promoted to mafia BEFORE the win check — otherwise the town
	// would be handed a premature victory.
	events = append(events, g.promoteConsortIfNeeded()...)

	if events, ended := g.endGameIfWon(events); ended {
		return events, nil
	}

	// Lynch but no win: return to DayDiscussion with the resolved flag
	// set so the only legal host command is BeginNight.
	events = append(events, g.endDayToDiscussion())
	return events, nil
}

// beginNightTurns is called whenever the game enters PhaseNight. It
// builds the night turn queue with ALL acting roles in the canonical
// order (Mafia → Detective → Doctor), regardless of whether any
// player of that role is still alive. It then enters NightSubOpening
// — the one-shot "City, go to sleep." beat that precedes any role's
// narration — without populating currentNightRole. The room's
// opening timer fires AdvancePhase, which pops the first role and
// enters its NightSubNarrate (see advanceNightSubPhase).
//
// Each role's turn walks the five-step NightSubPhase state machine;
// roles with no living holder substitute NightSubPonder for the act
// window so the audio cadence still narrates them. The night ends
// when the whole queue is exhausted.
//
// Returns the events to be appended after PhaseChanged.
func (g *Game) beginNightTurns() []Event {
	g.state.nightTurnQueue = g.state.nightTurnQueue[:0]
	// Canonical order: Mafia first, then the Consort (when present),
	// then the town info roles. The Consort sits right after the Mafia
	// and crucially BEFORE the detective and doctor, so her block is
	// already recorded when their act windows open — that's what lets us
	// notify a blocked town role at the start of their own turn and
	// suppress the detective's result. Mafia lead, and the Doctor wakes
	// LAST of all — after both night-killers (the mafia and, when
	// enabled, the vigilante) — so the save is the final beat of the
	// night, the town's last line of defense against either kill. The
	// consort turn is queued only when the role was actually dealt
	// (alive OR dead, to keep a dead consort's turn phantom and hide her
	// death); an optional role that was never dealt has no turn at all.
	//
	// We key off consortEnabled — the dealt-time fact that this game
	// HAS a consort — rather than the live roster role. A consort
	// promoted to RoleMafia (promoteConsortIfNeeded) no longer "holds"
	// RoleConsort, but her turn must keep running as a PHANTOM: dropping
	// it would shorten the night cadence the moment a promotion happens,
	// leaking the secret takeover to anyone counting the moderator's
	// beats. The phantom substitution (no act window) falls out of
	// roleTurnIsPhantom (no living RoleConsort) in enterNightSubPhase.
	g.state.nightTurnQueue = append(g.state.nightTurnQueue, RoleMafia)
	if g.state.consortEnabled {
		g.state.nightTurnQueue = append(g.state.nightTurnQueue, RoleConsort)
	}
	g.state.nightTurnQueue = append(g.state.nightTurnQueue, RoleDetective)
	// The optional Vigilante wakes after the detective but BEFORE the
	// doctor, so the doctor still closes the night (see above). Like the
	// consort we key off the dealt-time toggle (vigilanteEnabled) rather
	// than the live roster so a dead vigilante's turn keeps running as a
	// phantom — dropping it would shorten the night cadence and leak his
	// death. A vigilante who is alive but has spent his one bullet is
	// likewise treated as phantom (roleTurnIsPhantom): he still wakes for
	// cadence/secrecy but skips the act window instead of stalling the
	// night on a 60s window he can't use. Resolution order (mafia kill
	// before vigilante shot) is enforced in resolvePhase, independent of
	// this wake order.
	if g.state.vigilanteEnabled {
		g.state.nightTurnQueue = append(g.state.nightTurnQueue, RoleVigilante)
	}
	g.state.nightTurnQueue = append(g.state.nightTurnQueue, RoleDoctor)
	// Enter the night-scoped opening sub-phase. currentNightRole
	// stays empty until the opening elapses and advanceNightSubPhase
	// pops the first role.
	g.state.currentNightRole = ""
	g.state.currentNightSubPhase = NightSubOpening
	return []Event{NightSubPhaseStarted{
		Sub:      NightSubOpening,
		Day:      g.state.day,
		Deadline: 0,
	}}
}

// beginNextNightTurn pops the front of the queue, sets it as the
// current role, and enters NightSubNarrate (the opening audio cue).
// Subsequent sub-phases are driven by AdvancePhase (from the room's
// wall-clock timer) or NightAction (the actor's submission). The
// room layer stamps a real Deadline before broadcasting; the engine
// is timeless and emits Deadline=0.
//
// Note on Phantom for RoleMafia: HasLivingRole(RoleMafia) is always
// true when this function runs. checkWin (called after every
// state-changing event) emits GameEnded the instant living mafia
// hits zero and the phase transitions to PhaseEnded, which prevents
// any further beginNightTurns/beginNextNightTurn calls. The uniform
// `Phantom: !HasLivingRole(next)` computation in enterNightSubPhase
// is kept for symmetry across roles — it's correct, it's just
// dead-on-arrival for the mafia case.
func (g *Game) beginNextNightTurn() []Event {
	if len(g.state.nightTurnQueue) == 0 {
		return nil
	}
	next := g.state.nightTurnQueue[0]
	g.state.nightTurnQueue = g.state.nightTurnQueue[1:]
	g.state.currentNightRole = next
	return g.enterNightSubPhase(NightSubNarrate)
}

// checkWin evaluates win conditions and, if a faction has won, returns
// the GameEnded event and true. Otherwise returns the zero event and
// false.
//
// Mafia win:  living STRICT mafia >= living town  (can't be out-voted).
// Town win:   no living mafia-aligned players remain.
//
// The parity comparison counts ONLY the strict mafia faction (RoleMafia),
// not a surviving Consort. She has no kill of her own — she only blocks —
// and the town doesn't even know she's mafia-aligned, so letting her pad
// the mafia's "can't be out-voted" count would hand a kill-less role the
// game. She still matters in two ways: the town must eliminate her to win
// (the town-win branch counts her via mafiaAlignedLivingCount), and if
// the cabal is wiped while she lives she is promoted to RoleMafia
// (promoteConsortIfNeeded, which callers run BEFORE this check) and from
// then on counts toward parity as a real mafioso.
//
// Because that promotion always runs first, a wiped-out cabal with a
// living consort never reaches the town-win branch: she's already a
// RoleMafia, so both mafiaAlignedLivingCount and the strict mafia count
// are non-zero.
func (g *Game) checkWin() (GameEnded, bool) {
	mafia := g.state.factionLivingCount(FactionMafia)
	town := g.state.factionLivingCount(FactionTown)

	switch {
	case g.state.mafiaAlignedLivingCount() == 0:
		return GameEnded{
			Winner:     FactionTown,
			FinalRoles: g.state.finalRolesSnapshot(),
		}, true
	case mafia >= town:
		return GameEnded{
			Winner:     FactionMafia,
			FinalRoles: g.state.finalRolesSnapshot(),
		}, true
	}
	return GameEnded{}, false
}

// promoteConsortIfNeeded elevates a surviving Consort to full RoleMafia
// when the original mafia cabal has been wiped out (no living RoleMafia
// remains). This is the "sleeper takes over" mechanic: with the mafia
// gone, the consort inherits the kill and joins the mafia turn from the
// next night on.
//
// Returns the events to append (private to the promoted player, so the
// town never learns a takeover happened), or nil if no promotion
// applies. Callers MUST invoke this AFTER applying a death and BEFORE
// checkWin, so a cabal-ending death promotes the consort rather than
// handing the town a win. Two callers wipe the cabal today:
// applyFinalizeVotes (a lynch) and resolveAndExitNight (the Vigilante's
// night kill — the only way a mafioso dies at night).
func (g *Game) promoteConsortIfNeeded() []Event {
	if g.state.factionLivingCount(FactionMafia) > 0 {
		return nil // cabal still alive; nothing to take over
	}
	for i := range g.state.players {
		p := &g.state.players[i]
		if p.alive && p.role == RoleConsort {
			p.role = RoleMafia
			return []Event{
				ConsortPromoted{PlayerID: p.id},
				// She is now the (sole) mafia; re-issue the roster so
				// her client recognizes its new faction. FactionOnly,
				// so it reaches only her.
				MafiaRosterRevealed{Members: []PlayerID{p.id}},
			}
		}
	}
	return nil
}
