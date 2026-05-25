package main

import (
	"sort"
	"strings"
)

// decideAction returns the command and target a bot should send given
// its role, the current phase, its own ID, the set of living players,
// and any detective knowledge accumulated so far.
//
// Returns ("", "") when the bot should NOT act this phase. This is the
// common case for, e.g., a villager during night.
//
// The strategies are deliberately simple and deterministic — the goal
// is to produce a finite, reproducible game, not to play well.
// "Lowest" / "highest" below means by numeric player sequence (p1 < p2
// < ... < p10), not lexicographic.
func decideAction(
	role string,
	phase string,
	me string,
	alive map[string]struct{},
	detectiveKnown map[string]bool,
) (cmd, target string) {
	switch phase {
	case phaseNight:
		return nightActionFor(role, me, alive)
	case phaseDayVote:
		return voteFor(role, me, alive, detectiveKnown)
	default:
		return "", ""
	}
}

func nightActionFor(role, me string, alive map[string]struct{}) (string, string) {
	switch role {
	case roleMafia:
		// Mafia targets the lowest-numbered alive non-mafia player.
		// We can't tell who else is mafia here (the bot would have to
		// reason about NightActionRecorded events from other mafia,
		// but with only 1 mafia in the default roster we trivially
		// pick anyone that isn't us).
		if t := pickLowestExcluding(alive, me); t != "" {
			return "nightAction", t
		}
	case roleDoctor:
		// Doctor saves the lowest-numbered alive player OTHER than
		// themselves. Self-save is rejected by the engine
		// (ErrSelfTarget); excluding ourselves keeps the action valid.
		if t := pickLowestExcluding(alive, me); t != "" {
			return "nightAction", t
		}
	case roleDetective:
		// Investigate the lowest-numbered alive player we haven't
		// investigated yet... but since we don't track that in this
		// minimal sim, just investigate the lowest non-self. Repeats
		// across nights are accepted by the engine (returns same
		// result) but waste a turn; for a 5-player game this is fine.
		if t := pickLowestExcluding(alive, me); t != "" {
			return "nightAction", t
		}
	}
	return "", "" // villagers have no night action
}

func voteFor(
	role, me string,
	alive map[string]struct{},
	detectiveKnown map[string]bool,
) (string, string) {
	// Detective with a confirmed mafia hit: vote that target.
	if role == roleDetective {
		for target, isMafia := range detectiveKnown {
			if isMafia {
				if _, stillAlive := alive[target]; stillAlive {
					return "vote", target
				}
			}
		}
	}
	// Mafia: vote for a non-self target (anyone they could lynch to
	// reduce town count). In the default 1-mafia roster, this just
	// means "the lowest-numbered alive non-self".
	if role == roleMafia {
		if t := pickLowestExcluding(alive, me); t != "" {
			return "vote", t
		}
	}
	// Town (villager/doctor and fallback for detective without info):
	// vote the highest-numbered alive non-self.
	if t := pickHighestExcluding(alive, me); t != "" {
		return "vote", t
	}
	return "", ""
}

// --- helpers --------------------------------------------------------------

func sortedAlive(alive map[string]struct{}) []string {
	ids := make([]string, 0, len(alive))
	for id := range alive {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		return playerSeq(ids[i]) < playerSeq(ids[j])
	})
	return ids
}

func pickLowestExcluding(alive map[string]struct{}, exclude string) string {
	for _, id := range sortedAlive(alive) {
		if id != exclude {
			return id
		}
	}
	return ""
}

func pickHighestExcluding(alive map[string]struct{}, exclude string) string {
	ids := sortedAlive(alive)
	for i := len(ids) - 1; i >= 0; i-- {
		if ids[i] != exclude {
			return ids[i]
		}
	}
	return ""
}

// playerSeq parses a "pN" id into N. Falls back to 0 for unrecognised
// shapes so the sort still gives stable order.
func playerSeq(id string) int {
	if !strings.HasPrefix(id, "p") {
		return 0
	}
	n := 0
	for _, c := range id[1:] {
		if c < '0' || c > '9' {
			return 0
		}
		n = n*10 + int(c-'0')
	}
	return n
}
