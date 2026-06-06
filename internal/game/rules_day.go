package game

// applyDayVote handles vote cast / change / retract.
//
// See command.go's DayVote doc for the full state table. In short:
//
//   - "" target with no prior vote   -> ErrNoChange
//   - "" target with prior vote      -> VoteRetracted (delete entry)
//   - target == prior                -> ErrNoChange
//   - target, no prior               -> VoteCast (write entry)
//   - target, had prior              -> VoteChanged{From, To} (overwrite)
//
// Validation common to all paths:
//   - PhaseDayVote only.
//   - Voter must exist and be alive.
//   - When target != "": target must exist and be alive, and must not
//     equal the voter (self-vote forbidden, ErrSelfTarget).
func (g *Game) applyDayVote(c DayVote) ([]Event, error) {
	if err := g.requirePhase(PhaseDayVote); err != nil {
		return nil, err
	}
	// Once the host reveals the tally, voting is locked: the revealed
	// map is the record the room is acting on, so late edits (which
	// wouldn't be reflected in the already-broadcast VotesRevealed
	// snapshot) are rejected. ClearVotes reopens voting if needed.
	if g.state.votesRevealed {
		return nil, ErrWrongPhase
	}

	if _, err := g.state.requireLivingPlayer(c.Voter); err != nil {
		return nil, err
	}

	prior, hadPrior := g.state.votes[c.Voter]
	abstained := g.state.abstentions[c.Voter]

	// Retract path: empty target clears any active DECISION — a real vote
	// or an abstention — returning the voter to undecided.
	if c.Target == "" {
		if !hadPrior && !abstained {
			return nil, ErrNoChange
		}
		delete(g.state.votes, c.Voter)
		delete(g.state.abstentions, c.Voter)
		// Was carries the prior vote target (empty when the voter was
		// abstaining); either way the client clears its decision state.
		return []Event{VoteRetracted{Voter: c.Voter, Was: prior}, g.voteProgress()}, nil
	}

	// Non-empty target: validate target.
	if c.Voter == c.Target {
		return nil, ErrSelfTarget
	}
	if _, err := g.state.requireLivingPlayer(c.Target); err != nil {
		return nil, err
	}

	if hadPrior && prior == c.Target {
		return nil, ErrNoChange
	}

	if g.state.votes == nil {
		g.state.votes = make(map[PlayerID]PlayerID)
	}
	g.state.votes[c.Voter] = c.Target
	// Casting a real vote supersedes any prior abstention, keeping the
	// {votes, abstentions} states mutually exclusive.
	delete(g.state.abstentions, c.Voter)

	if hadPrior {
		return []Event{VoteChanged{Voter: c.Voter, From: prior, To: c.Target}, g.voteProgress()}, nil
	}
	// Commands and events are conceptually distinct vocabularies even
	// when they happen to share field shapes; avoid a structural cast
	// here so the two stay independently evolvable.
	//nolint:gosimple // see comment above.
	return []Event{VoteCast{Voter: c.Voter, Target: c.Target}, g.voteProgress()}, nil
}

// applyDayAbstain records an explicit abstention — the voter declines to
// vote anyone, but their decision still counts toward the reveal gate and
// the public progress count (it just adds to no target's tally). See the
// DayAbstain command doc for the full table. Abstaining supersedes any
// active vote so a voter is in exactly one of {voted, abstained, undecided}.
func (g *Game) applyDayAbstain(c DayAbstain) ([]Event, error) {
	if err := g.requirePhase(PhaseDayVote); err != nil {
		return nil, err
	}
	// Same lock as DayVote: once the host reveals, decisions are frozen.
	if g.state.votesRevealed {
		return nil, ErrWrongPhase
	}
	if _, err := g.state.requireLivingPlayer(c.Voter); err != nil {
		return nil, err
	}

	if g.state.abstentions[c.Voter] {
		return nil, ErrNoChange
	}
	delete(g.state.votes, c.Voter)
	if g.state.abstentions == nil {
		g.state.abstentions = make(map[PlayerID]bool)
	}
	g.state.abstentions[c.Voter] = true
	// Commands and events are distinct vocabularies even when their fields
	// line up; keep the explicit literal rather than a struct conversion so
	// they stay independently evolvable (same rationale as VoteCast above).
	//nolint:gosimple // see comment above.
	return []Event{VoteAbstained{Voter: c.Voter}, g.voteProgress()}, nil
}

// voteProgress builds the PUBLIC running-count event that accompanies every
// private vote cast/change/retract. It snapshots the current living-voter
// count so the whole room can see voting progress without the tally being
// revealed. A change leaves the count unchanged but still re-emits the
// (same) number, so a late joiner always finds the current count on the
// trailing VoteProgress regardless of which edit came last.
func (g *Game) voteProgress() Event {
	return VoteProgress{Day: g.state.day, Cast: g.state.votesCastCount()}
}

// resolveDayVote tallies the current vote map and returns either:
//   - (lynchTarget, true)  -> a single target has a STRICT MAJORITY of
//     the living population's votes; lynch them.
//   - ("", false)          -> no strict majority (split vote, a plurality
//     short of half, abstentions, or no votes).
//
// "Strict majority" means a single target's vote count is greater than
// half the number of living players: count*2 > living. This is a higher
// bar than a plurality — a town can lead the tally yet still fail to
// reach the threshold, in which case nobody is lynched. A target with a
// strict majority is necessarily unique (two targets can't each hold
// >50%), so no tie-break is needed. Abstaining (a living player not
// voting) effectively counts against the threshold, so a day can end
// with no lynch even when everyone who voted agreed.
//
// The vote map is NOT cleared here. The caller is FinalizeVotes, which
// is host-driven (no auto-advance from PhaseDayVote): on a strict
// majority the targeted player is lynched; otherwise the day ends with
// no lynch. Either way the phase returns to DayDiscussion.
func (g *Game) resolveDayVote() (PlayerID, bool) {
	counts := make(map[PlayerID]int, len(g.state.votes))
	for _, target := range g.state.votes {
		counts[target]++
	}
	if len(counts) == 0 {
		return "", false
	}

	living := g.state.livingCount()
	// Iterate via the player list for deterministic order. At most one
	// target can clear the >50% bar, so the first one we find is THE
	// majority target; no tie-break is possible or needed.
	for _, p := range g.state.players {
		if counts[p.id]*2 > living {
			return p.id, true
		}
	}
	return "", false
}
