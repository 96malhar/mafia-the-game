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
	if g.state.phase != PhaseDayVote {
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
//   - (lynchTarget, true)  -> a unique plurality exists; lynch them.
//   - ("", false)          -> no unique plurality (tie, or no votes).
//
// "Unique plurality" means: there is exactly one target with the
// highest vote count, and that count is at least 1.
//
// The vote map is NOT cleared here. The caller is FinalizeVotes,
// which is host-driven (no auto-advance from PhaseDayVote): on a
// unique plurality the targeted player is lynched and the phase
// returns to DayDiscussion; on a tie the host calls ClearVotes for
// a re-vote or FinalizeVotes again to end the day with no lynch.
func (g *Game) resolveDayVote() (PlayerID, bool) {
	counts := make(map[PlayerID]int, len(g.state.votes))
	for _, target := range g.state.votes {
		counts[target]++
	}
	if len(counts) == 0 {
		return "", false
	}

	var top PlayerID
	var topCount int
	var tied bool
	// Iterate via the player list for deterministic order: if two
	// candidates have the same count, the iteration determines which
	// "appears first," but we then mark it tied and return false anyway.
	for _, p := range g.state.players {
		c := counts[p.id]
		if c == 0 {
			continue
		}
		switch {
		case c > topCount:
			top = p.id
			topCount = c
			tied = false
		case c == topCount:
			tied = true
		}
	}
	if topCount == 0 || tied {
		return "", false
	}
	return top, true
}
