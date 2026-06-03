package game

// Project returns the subset of events that the given viewer is allowed
// to see, based on each event's Visibility and the current GameState.
//
// The engine emits events with the FULL TRUTH; this is the single place
// where privacy is enforced. Server-side code MUST pipe events through
// Project (or an equivalent filter) before sending them to a player —
// the engine itself never hides anything.
//
// Visibility semantics:
//
//   - Public       -> visible to everyone, including dead players and
//     (future) spectators.
//   - PrivateTo(p) -> visible only when viewer == p. Used for role
//     assignment, doctor saves, detective results.
//   - FactionOnly(f) -> visible only when the viewer is a CURRENTLY ALIVE
//     member of faction f. Dead faction members lose
//     access to ongoing secret coordination; they become
//     spectators of public information only.
//   - Graveyard()    -> visible only when the viewer is a CURRENTLY DEAD
//     player. The mirror of FactionOnly's aliveness gate:
//     it hands the eliminated the full roster (RosterRevealed)
//     while the living see nothing.
//
// state is consulted to determine the viewer's role and aliveness for the
// FactionOnly / Graveyard checks. If the viewer is not a known player
// (e.g. a not-yet-joined client, or a future spectator), only public
// events are returned.
func Project(viewer PlayerID, events []Event, state *GameState) []Event {
	out := make([]Event, 0, len(events))
	for _, e := range events {
		if canSee(viewer, e.Visibility(), state) {
			out = append(out, e)
		}
	}
	return out
}

// canSee is the single decision function for visibility. Kept separate
// from Project so tests can exercise the rule directly.
func canSee(viewer PlayerID, v Visibility, state *GameState) bool {
	switch v.Audience {
	case "public":
		return true

	case "player":
		return viewer != "" && viewer == v.Player

	case "faction":
		if viewer == "" || state == nil {
			return false
		}
		p, ok := state.findPlayer(viewer)
		if !ok {
			return false
		}
		if !p.alive {
			return false
		}
		return p.role.Faction() == v.Faction

	case "dead":
		// The graveyard: only players who have been eliminated. A
		// not-yet-joined client or future spectator (not a known
		// player) is not in the graveyard and sees nothing here.
		if viewer == "" || state == nil {
			return false
		}
		p, ok := state.findPlayer(viewer)
		if !ok {
			return false
		}
		return !p.alive

	default:
		// Unknown audience tag: default-deny. The engine only emits
		// known tags today, but a future code change that adds a new
		// audience and forgets to update this switch should err on the
		// side of hiding info rather than leaking it.
		return false
	}
}
