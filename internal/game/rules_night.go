package game

// applyNightAction records one role-specific night action AND drives
// the act → ponder edge of the per-role state machine. All other
// edges are driven by AdvancePhase from the room's wall-clock timer
// (see advanceNightSubPhase).
//
// This handler is a thin wrapper over the role registry in rolespec.go:
// generic structural checks live here, and role-specific validation
// lives in each role's NightAction.Validate hook.
//
// Generic validation:
//   - PhaseNight only AND currentNightSubPhase == NightSubAct (i.e.
//     the actor's window is open). Submissions during narrate /
//     ponder / sleep / settle are rejected with ErrNotYourTurn so
//     the wire and UI can keep "wrong time" distinct from "wrong
//     role" (ErrNotYourAction).
//   - Actor and Target must be known and alive.
//   - Actor's role must equal currentNightRole — the strict turn-order
//     gate that makes the game playable in person.
//   - Actor must be a role with a NightAction in the registry
//     (villagers are rejected with ErrNotYourAction).
//   - For the mafia turn the "actor" may be any living mafia (faction-
//     collective); first valid submission locks the kill target and
//     ends the act window for the whole faction.
//   - Each actor commits once per night (ErrAlreadyActed).
//
// Role-specific validation is delegated to spec.NightAction.Validate.
//
// Emitted events (atomic batch on success):
//
//	NightActionRecorded{actor, target, faction}    — faction-only
//	DetectiveResult{...}                           — detective only, private
//	NightPonderStarted{role, day, phantom: false}  — public
//
// After ponder elapses (room's timer), AdvancePhase drives ponder →
// sleep → settle → next role. The detective's read-modal pause is
// folded into the ponder duration (sized by the room's per-role
// Ponder function) rather than a separate timer hook.
func (g *Game) applyNightAction(c NightAction) ([]Event, error) {
	if err := g.requirePhase(PhaseNight); err != nil {
		return nil, err
	}

	actor, err := g.state.requireLivingPlayer(c.Actor)
	if err != nil {
		return nil, err
	}

	spec, ok := roleSpecs[actor.role]
	if !ok || spec.NightAction == nil {
		// Roles with no night action (villager) are categorically
		// rejected regardless of whose turn it is. This keeps the
		// "you have no night action" error stable for UI gating.
		return nil, ErrNotYourAction
	}

	// Turn-order gate. The current role MUST match the actor's role; the
	// Mafia turn is the faction-collective case — any living FactionMafia
	// member (a mafioso OR the Yakuza, whose role tag is RoleYakuza) may
	// submit the kill when it's the mafia's turn.
	if !g.state.isActorsTurn(actor) {
		return nil, ErrNotYourTurn
	}
	// And we must be in the act sub-phase. Submissions during narrate
	// / ponder / sleep / settle (or between turns) collapse onto the
	// same "wrong time" error.
	if g.state.currentNightSubPhase != NightSubAct {
		return nil, ErrNotYourTurn
	}

	// Roleblock backstop: a neutralized NON-mafia actor cannot act at all.
	// A neutralized actor's turn is now phantom (no act window — see
	// roleTurnIsPhantom), so the sub-phase gate above already rejects any
	// submission with ErrNotYourTurn before we get here; this ErrBlocked
	// branch is the deeper backstop kept for defense-in-depth (and in case
	// the phantom routing ever changes). The whole mafia faction is immune
	// (the faction kill ignores a block; the Yakuza acts within that turn)
	// and falls through unaffected.
	if actor.role.Faction() != FactionMafia && g.state.isNightNeutralized(actor.id) {
		return nil, ErrBlocked
	}

	if c.Target == "" {
		return nil, ErrUnknownPlayer
	}
	target, err := g.state.requireLivingPlayer(c.Target)
	if err != nil {
		return nil, err
	}

	if spec.NightAction.Validate != nil {
		if err := spec.NightAction.Validate(g.state, actor, target); err != nil {
			return nil, err
		}
	}

	if g.state.pendingNight == nil {
		g.state.pendingNight = make(map[PlayerID]PlayerID)
	}
	if _, already := g.state.pendingNight[c.Actor]; already {
		return nil, ErrAlreadyActed
	}
	g.state.pendingNight[c.Actor] = c.Target

	// First valid submission ends the act window. For mafia (faction-
	// collective), this is intentional: only one kill per night,
	// decided by whichever mafia submits first.
	events := []Event{
		NightActionRecorded{
			Actor:   c.Actor,
			Target:  c.Target,
			Faction: actor.role.Faction(),
		},
		// Mirror the action to the graveyard so dead spectators can watch
		// the night unfold. Graveyard-scoped, so it never reaches a living
		// player; it carries both roles since the dead already know the
		// full roster anyway. See SpectatorNightAction.
		SpectatorNightAction{
			Actor:      actor.id,
			ActorRole:  actor.role,
			Target:     target.id,
			TargetRole: target.role,
		},
	}

	// Detective gets immediate private feedback ("X IS / is NOT a
	// mafia member"). We emit it BEFORE the ponder transition so the
	// modal pops the moment the action is recorded. The detective's
	// reveal-phase Apply is a no-op (see rolespec.go); the information
	// is purely role-based (target.role.Faction()), so it doesn't
	// depend on the resolve step at all.
	if actor.role == RoleDetective {
		// A blocked/neutralized detective never reaches this point — his
		// turn is phantom (no act window), so there's no submission to
		// reach here — so we always have a genuine result to deliver.
		//
		// IsMafia is faction-based: a strict mafioso or a Yakuza (both
		// FactionMafia) read as mafia, while an un-promoted Consort (role
		// RoleConsort, faction FactionConsort) reads as NOT mafia by design.
		//
		// Yakuza recruit overrides (evaluated from the in-flight recruit,
		// which is locked during the earlier Mafia turn so it's already set
		// by the time the detective acts):
		//   - Investigating the Yakuza AFTER it has recruited this night
		//     reads NOT mafia — having committed its sacrifice it "leaves"
		//     the mafia.
		//   - Investigating the Yakuza's recruit target reads mafia
		//     immediately, even though the role flip itself lands at
		//     resolution.
		isMafia := target.role.Faction() == FactionMafia
		if g.state.recruitPending {
			switch target.id {
			case g.state.recruitYakuza:
				isMafia = false
			case g.state.recruitTarget:
				isMafia = true
			}
		}
		events = append(events, DetectiveResult{
			Detective: actor.id,
			Target:    target.id,
			IsMafia:   isMafia,
		})
	}

	// Tracker gets immediate private feedback naming who its target
	// visited tonight ("X visited Y", or "X stayed home"). Like the
	// detective above, we emit it BEFORE the ponder transition so the
	// modal pops the moment the action is recorded. The tracker wakes
	// LAST (after the doctor), so every other role's target is already in
	// pendingNight and any Yakuza recruit is recorded — trackedVisit reads
	// that settled intent directly (it does NOT depend on the resolve step,
	// which is why the spec's reveal-phase Apply is a no-op). A
	// blocked/neutralized tracker never reaches this point — its turn is
	// phantom (no act window) — so the result is always genuine. The Visited
	// id is "" when the tracked player took no action OR acted on themselves
	// (e.g. a doctor self-save) — both read as "stayed home" (see trackedVisit).
	if actor.role == RoleTracker {
		events = append(events, TrackerResult{
			Tracker: actor.id,
			Target:  target.id,
			Visited: g.state.trackedVisit(target),
		})
	}

	// Drive act → ponder. Both submit (here) and timeout (AdvancePhase
	// during the act window) pass through ponder, so the audio cadence
	// and sub-phase sequence are uniform — observers can't tell a real
	// submission from a timed-out turn.
	events = append(events, g.enterNightSubPhase(NightSubPonder)...)
	return events, nil
}

// applyNightPass ends the current actor's act window early WITHOUT
// recording an action. It's the engine side of the Vigilante's "hold
// fire" button: declining to act advances straight to ponder (so the
// table isn't held for the full act window) while preserving any
// one-shot resource — nothing is written to pendingNight, so resolveNight
// never sees an intent and the vigilante's bullet stays unspent.
//
// Gating mirrors applyNightAction's generic checks, plus the per-role
// AllowPass opt-in:
//   - PhaseNight only.
//   - Actor must be known and alive.
//   - Actor's role must have a NightAction whose AllowPass is set;
//     otherwise ErrNotYourAction (villagers, and roles like the detective
//     /doctor/consort/mafia that don't expose a pass affordance).
//   - Actor's role must be the current night role AND we must be in the
//     act sub-phase; otherwise ErrNotYourTurn (a phantom turn — dead,
//     spent, or blocked — never reaches act, so it's rejected here too).
//
// Submit, timeout, and pass all collapse onto the same act → ponder edge,
// so an observer can't distinguish "fired", "let the timer run", and
// "held fire" — the secrecy property the act window already guarantees.
func (g *Game) applyNightPass(c NightPass) ([]Event, error) {
	if err := g.requirePhase(PhaseNight); err != nil {
		return nil, err
	}

	actor, err := g.state.requireLivingPlayer(c.Actor)
	if err != nil {
		return nil, err
	}

	spec, ok := roleSpecs[actor.role]
	if !ok || spec.NightAction == nil || !spec.NightAction.AllowPass {
		// The role can't decline-to-act as an explicit move (it just
		// lets its timer run). Same sentinel as "you have no night
		// action" so the wire/UI gate it identically.
		return nil, ErrNotYourAction
	}

	if actor.role != g.state.currentNightRole {
		return nil, ErrNotYourTurn
	}
	if g.state.currentNightSubPhase != NightSubAct {
		return nil, ErrNotYourTurn
	}

	// Advance act → ponder, leaving pendingNight untouched. Identical to
	// the timeout edge, so no NightActionRecorded and no resource spend.
	return g.enterNightSubPhase(NightSubPonder), nil
}

// applyRecruit records the Yakuza's one-shot recruit, submitted during the
// Mafia turn's act window as an alternative to the faction kill. It is the
// engine side of the client's "Recruit" button.
//
// Gating:
//   - PhaseNight only.
//   - Actor known, alive, and holding RoleYakuza (else ErrNotYourAction —
//     the same sentinel as "you have no such action").
//   - It must be the Mafia turn (currentNightRole == RoleMafia) AND the act
//     sub-phase. Submissions outside that window are ErrNotYourTurn. This is
//     also what makes kill and recruit mutually exclusive: the first
//     submission of EITHER drives act → ponder, so the second is rejected
//     here on the sub-phase check.
//   - Target known and alive, not the Yakuza itself (ErrSelfTarget), and not
//     a strict mafioso (ErrNotYourAction). The Consort and any town role are
//     legal targets.
//
// On success it records the recruit in the dedicated recruit fields (NOT
// pendingNight, so resolveNight's kill replay never sees it), emits the
// faction-scoped ack + the graveyard mirror, and closes the act window by
// advancing to ponder. The conversion, the self-sacrifice, and the target's
// power suppression all resolve later (resolveRecruit / roleTurnIsPhantom).
func (g *Game) applyRecruit(c Recruit) ([]Event, error) {
	if err := g.requirePhase(PhaseNight); err != nil {
		return nil, err
	}

	actor, err := g.state.requireLivingPlayer(c.Actor)
	if err != nil {
		return nil, err
	}
	if actor.role != RoleYakuza {
		return nil, ErrNotYourAction
	}

	// Recruit rides the Mafia turn: only legal while the mafia act window is
	// open. (A spent recruit can't recur — the Yakuza dies the night it
	// recruits — so no separate one-shot flag is needed beyond that.)
	if g.state.currentNightRole != RoleMafia || g.state.currentNightSubPhase != NightSubAct {
		return nil, ErrNotYourTurn
	}

	if c.Target == "" {
		return nil, ErrUnknownPlayer
	}
	target, err := g.state.requireLivingPlayer(c.Target)
	if err != nil {
		return nil, err
	}
	if err := rejectSelfTarget(actor, target); err != nil {
		return nil, err
	}
	if target.role == RoleMafia {
		// Can't recruit an existing mafioso — pointless and nonsensical.
		// The Consort (RoleConsort) is NOT a strict mafioso, so she remains
		// a legal target by design.
		return nil, ErrNotYourAction
	}

	g.state.recruitPending = true
	g.state.recruitYakuza = actor.id
	g.state.recruitTarget = target.id

	events := []Event{
		// Co-mafia see the locked recruit (faction-scoped) so they know the
		// faction is converting, not killing, tonight.
		RecruitRecorded{Yakuza: actor.id, Target: target.id},
		// Mirror to the graveyard so dead spectators watch the night unfold,
		// flagged as a recruit so the feed labels it correctly.
		SpectatorNightAction{
			Actor:      actor.id,
			ActorRole:  actor.role,
			Target:     target.id,
			TargetRole: target.role,
			Recruit:    true,
		},
	}
	// Close the act window (act → ponder), exactly like a kill submission,
	// so the faction gets no second action this night.
	events = append(events, g.enterNightSubPhase(NightSubPonder)...)
	return events, nil
}

// resolveNight runs each scheduled night action through its role's
// Apply hook, in nightPhase order, with an implicit resolve step
// (kill vs save reconciliation) between Schedule and Reveal.
//
// This replaces the old hand-rolled switch on role; new roles plug in
// purely via the registry in rolespec.go.
//
// The iteration order within a phase is the players' join order, which
// is stable and deterministic.
func (g *Game) resolveNight() []Event {
	ctx := &nightContext{state: g.state}

	// Block: roleblockers nullify other actions. (No role in v1; the
	// iteration is here so a future spec slots in without touching
	// this function.)
	g.runNightPhase(ctx, nightPhaseBlock)

	// Schedule: declare intent without mutating state (mafia kill,
	// doctor save).
	g.runNightPhase(ctx, nightPhaseSchedule)

	// Resolve: reconcile kill vs save into at most one mutation.
	resolvePhase(ctx)

	// Recruit: apply a Yakuza recruit (convert the target, sacrifice the
	// Yakuza) AFTER the kill/save resolution above, so a vigilante shot on
	// the convert is honored (the convert dies as town, conversion wasted)
	// and the doctor never gets a chance to save the self-sacrificing Yakuza.
	g.resolveRecruit(ctx)

	// Reveal: info roles read the resolved state (Detective, future
	// Lookout).
	g.runNightPhase(ctx, nightPhaseReveal)

	// Spend the vigilante's one-shot bullet if he fired this night. We
	// flush it here (rather than in the spec's Apply) so the persistent
	// flag is set exactly once per night and only when a shot was
	// actually recorded — a blocked vigilante never reaches Apply, so his
	// bullet is preserved for a later night.
	if ctx.vigilanteFired {
		g.state.vigilanteShotUsed = true
	}

	g.state.pendingNight = nil
	g.state.clearRecruit()
	return ctx.events
}

// resolveRecruit applies a pending Yakuza recruit during night resolution.
// It runs AFTER resolvePhase (so the kill/save is already settled) and
// before the reveal phase. Three things happen, in order:
//
//  1. Conversion — if the target survived the night's kills and isn't
//     already mafia, flip their role to RoleMafia and privately tell them
//     (RoleAssigned). If a vigilante shot killed the target this same night,
//     the conversion is silently wasted (the guard skips a dead/absent
//     target) — but the Yakuza still pays the price below.
//  2. Recruit notice — delivered here only for a villager (no night turn,
//     so no earlier beat to be told at). An active role was already told at
//     its (phantom) turn, where the Recruited notice takes precedence over a
//     Consort block, so a recruited-and-blocked player sees only Recruited
//     (at its turn) and is not re-notified here. Exactly one Recruited per
//     convert.
//  3. Self-sacrifice — the Yakuza dies, unconditionally and unpreventably
//     (the doctor cannot save it; this is not a "hit" that resolveHit
//     reconciles). The alive guard keeps it to a single PlayerKilled even if
//     a vigilante already shot the Yakuza tonight.
//
// Finally it re-issues the mafia roster (now including the convert, minus the
// dead Yakuza) to the faction so the new mafioso and the team see the update.
func (g *Game) resolveRecruit(ctx *nightContext) {
	if !g.state.recruitPending {
		return
	}
	yakuza := g.state.recruitYakuza
	targetID := g.state.recruitTarget

	if tp, ok := g.state.findPlayer(targetID); ok && tp.alive && tp.role != RoleMafia {
		// Deliver exactly ONE Recruited notice per convert, at the right
		// time. An active role is told at its (phantom) turn
		// (enterNightSubPhase), where the recruit notice takes precedence
		// over a Consort block — so even a recruited-AND-blocked player sees
		// only the Recruited notice, at their turn. A villager (no
		// NightAction, hence no turn) has no earlier beat, so it is told here.
		spec, hadSpec := roleSpecs[tp.role]
		hadNightTurn := hadSpec && spec.NightAction != nil

		tp.role = RoleMafia
		ctx.events = append(ctx.events, RoleAssigned{PlayerID: targetID, Role: RoleMafia})
		if !hadNightTurn {
			ctx.events = append(ctx.events, Recruited{PlayerID: targetID})
		}
	}

	if yp, ok := g.state.findPlayer(yakuza); ok && yp.alive {
		yp.alive = false
		ctx.died = yakuza
		ctx.events = append(ctx.events, PlayerKilled{PlayerID: yakuza})
	}

	// Re-issue the FULL faction roster (faction-scoped) so the convert sees
	// every teammate — living mafia AND the dead predecessors (the original
	// cabal and the just-sacrificed Yakuza, badged distinctly via the Yakuza
	// field) — while the existing mafia merge in the new convert. Sending the
	// complete cabal here makes a live recruit match what a rejoin would
	// reconstruct from the StartGame roster. Captured after the role flip and
	// the Yakuza's death; recruit fields are cleared by resolveNight cleanup.
	ctx.events = append(ctx.events, g.state.currentMafiaRoster())
}

// runNightPhase invokes Apply on every player whose role's spec
// declares a NightAction in the given phase AND has a pending target
// for tonight. Iteration order is players' join order for determinism.
func (g *Game) runNightPhase(ctx *nightContext, phase nightPhase) {
	for i := range g.state.players {
		actor := &g.state.players[i]
		target, ok := g.state.pendingNight[actor.id]
		if !ok {
			continue
		}
		spec, ok := roleSpecs[actor.role]
		if !ok || spec.NightAction == nil {
			continue
		}
		if spec.NightAction.Phase != phase {
			continue
		}
		// Neutralization backstop (defense-in-depth). A neutralized
		// non-mafia actor's turn is phantom (no act window — see
		// roleTurnIsPhantom), so he's never in pendingNight and this branch
		// is unreachable in normal flow. It stays as a safety net: if the
		// phantom routing is ever bypassed, the action is still nullified
		// here (no save scheduled, no reveal run). The whole mafia faction
		// is immune by design (blocking a mafioso is a wasted night — the
		// kill is a faction action), and neither the consort nor the yakuza
		// targets a fellow mafia member.
		if actor.role.Faction() != FactionMafia && g.state.isNightNeutralized(actor.id) {
			continue
		}
		tp, ok := g.state.findPlayer(target)
		if !ok {
			continue
		}
		spec.NightAction.Apply(ctx, actor, tp)
	}
}
