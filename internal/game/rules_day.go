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
	if g.state.id == "" {
		return nil, ErrWrongPhase
	}
	if g.state.phase == PhaseEnded {
		return nil, ErrGameEnded
	}
	if g.state.phase != PhaseDayVote {
		return nil, ErrWrongPhase
	}
	// Once the host reveals the tally, voting is locked: the revealed
	// map is the record the room is acting on, so late edits (which
	// wouldn't be reflected in the already-broadcast VotesRevealed
	// snapshot) are rejected. ClearVotes reopens voting if needed.
	if g.state.votesRevealed {
		return nil, ErrWrongPhase
	}

	voter, ok := g.state.findPlayer(c.Voter)
	if !ok {
		return nil, ErrUnknownPlayer
	}
	if !voter.alive {
		return nil, ErrPlayerDead
	}

	prior, hadPrior := g.state.votes[c.Voter]

	// Retract path: empty target.
	if c.Target == "" {
		if !hadPrior {
			return nil, ErrNoChange
		}
		delete(g.state.votes, c.Voter)
		return []Event{VoteRetracted{Voter: c.Voter, Was: prior}}, nil
	}

	// Non-empty target: validate target.
	if c.Voter == c.Target {
		return nil, ErrSelfTarget
	}
	target, ok := g.state.findPlayer(c.Target)
	if !ok {
		return nil, ErrUnknownPlayer
	}
	if !target.alive {
		return nil, ErrPlayerDead
	}

	if hadPrior && prior == c.Target {
		return nil, ErrNoChange
	}

	if g.state.votes == nil {
		g.state.votes = make(map[PlayerID]PlayerID)
	}
	g.state.votes[c.Voter] = c.Target

	if hadPrior {
		return []Event{VoteChanged{Voter: c.Voter, From: prior, To: c.Target}}, nil
	}
	// Commands and events are conceptually distinct vocabularies even
	// when they happen to share field shapes; avoid a structural cast
	// here so the two stay independently evolvable.
	//nolint:gosimple // see comment above.
	return []Event{VoteCast{Voter: c.Voter, Target: c.Target}}, nil
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
