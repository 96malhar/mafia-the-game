package game

// Player is the engine's per-player record. Fields are unexported so that
// only this package can mutate them; read access from outside is provided
// via accessors on GameState (added as needed).
type Player struct {
	id    PlayerID
	name  string
	role  Role // zero value (empty Role) until StartGame deals roles
	alive bool
}

// ID returns the player's identifier.
//
// The other field reads (Name, Role, Alive) are test-only and live in
// export_test.go so they don't ship in production builds.
func (p Player) ID() PlayerID { return p.id }

// GameState is the full, authoritative state of one game. It is mutated
// only through Game.Apply; outside callers may only read via accessors.
//
// We keep fields unexported so the compiler enforces this rule across the
// codebase — no one can write `state.phase = PhaseEnded` from another
// package even by accident.
//
// Roster model: instead of a fixed Roles slice configured at CreateGame,
// the engine carries (minPlayers, maxPlayers, mafiaCount). The actual
// per-player role list is composed at StartGame from
// (playerCount, mafiaCount): Mafia×N, Detective, Doctor, Villager×rest.
// This lets the host tune the game during the lobby without having to
// know up-front exactly how many friends will show up.
type GameState struct {
	id   GameID
	seed int64

	minPlayers int // minimum players required to call StartGame
	maxPlayers int // hard cap on AddPlayer
	mafiaCount int // number of Mafia roles to deal at StartGame

	// consortEnabled records whether the host has toggled the optional
	// Consort role on for the upcoming game. Set via SetConsort while in
	// PhaseLobby; consumed by composeRoster at StartGame to add one
	// RoleConsort (taking the slot of a villager).
	consortEnabled bool

	// vigilanteEnabled records whether the host has toggled the optional
	// Vigilante role on for the upcoming game. Set via SetVigilante while
	// in PhaseLobby; consumed by composeRoster at StartGame to add one
	// RoleVigilante (taking the slot of a villager, like the Consort).
	vigilanteEnabled bool

	// vigilanteShotUsed records whether the vigilante has already fired
	// his single bullet on a prior night. Set in resolveNight the night a
	// shot is recorded; the vigilante's NightAction.Validate rejects any
	// further shot once it's true.
	vigilanteShotUsed bool

	// yakuzaEnabled records whether the host has toggled the optional
	// Yakuza role on for the upcoming game. Set via SetYakuza while in
	// PhaseLobby; consumed by composeRoster at StartGame to add one
	// RoleYakuza (taking the slot of a villager, like the Consort). The
	// Yakuza is a full mafia member that acts during the Mafia turn.
	yakuzaEnabled bool

	// recruitPending / recruitYakuza / recruitTarget hold the in-flight
	// Yakuza recruit for the current night. recruitPending distinguishes
	// "a recruit was submitted" from the zero-value ids. The recruit is
	// deliberately NOT stored in pendingNight (which is keyed actor->kill
	// target and replayed by runNightPhase as a kill); it gets its own
	// fields so resolveRecruit can apply the conversion + self-sacrifice
	// separately. Set in applyRecruit during the Mafia turn; consulted by
	// the detective read, the recruit-target power suppression, and the
	// vigilante void guard; cleared in resolveRecruit (and whenever the
	// night ends without one).
	recruitPending bool
	recruitYakuza  PlayerID
	recruitTarget  PlayerID

	phase   Phase
	day     int // day number; 0 in Lobby/first Night, 1 after first day starts
	players []Player

	// pendingNight stores night-action targets keyed by actor. Cleared
	// each time Night ends. Per-actor commit-once: re-submission is
	// rejected with ErrAlreadyActed.
	pendingNight map[PlayerID]PlayerID

	// votes stores the current PhaseDayVote tally as voter -> target.
	// Unlike night actions, votes are mutable; entries are overwritten
	// or deleted as players change or retract their vote.
	votes map[PlayerID]PlayerID

	// votesRevealed records whether the host has revealed the current
	// PhaseDayVote tally (RevealVotes). While false the tally is hidden
	// — each voter sees only their own vote. Once true the full tally is
	// public (via the VotesRevealed event) and voting is locked until a
	// ClearVotes reopens it. Reset to false by OpenVoting and ClearVotes.
	votesRevealed bool

	// dayLynchResolved records whether this day has already had a vote
	// finalized. When true, the only legal host transition out of
	// PhaseDayDiscussion is BeginNight; OpenVoting is rejected. Reset
	// to false each time a fresh DayDiscussion begins (out of Night).
	dayLynchResolved bool

	// rolesDealt records whether StartGame has composed and assigned
	// the per-player roles. It gates the lobby-mutating commands
	// (AddPlayer, SetMafiaCount, and the lobby branch of BeginNight):
	// once roles exist, the lobby is closed even though the game stays
	// in PhaseLobby until BeginNight. This is an explicit flag rather
	// than the old "players[0].role != ''" probe, which coupled
	// correctness to player ordering and the assumption that dealing
	// always sets every role at once.
	rolesDealt bool

	// --- Night turn state ----------------------------------------------
	//
	// Nights are strictly turn-ordered: one role acts at a time, in
	// nightTurnQueue order, and each role's turn walks a small linear
	// sub-state machine (see NightSubPhase). The currently-active role
	// is at index 0 (currentNightRole); currentNightSubPhase says
	// where in that role's lifecycle we are. All are cleared (zero
	// values) any time the game is not in PhaseNight, or when the
	// queue is exhausted just before resolveNight runs.
	//
	// We keep these as ENGINE-OWNED state because the engine is the
	// authority on "whose turn is it AND what part of it". The engine
	// itself is timeless: wall-clock deadlines are NOT stored here.
	// The room layer owns timing entirely — it stamps an absolute
	// Deadline onto each Night*Started event before broadcasting and
	// arms its own timer against it (see internal/room/broadcast.go and
	// timers.go).
	currentNightRole     Role
	currentNightSubPhase NightSubPhase
	nightTurnQueue       []Role
}

// NightSubPhase is the sub-state during PhaseNight. Every night opens
// with a one-shot NightSubOpening ("City, go to sleep." + pre-wake
// silence), then each role's turn walks a five-step linear state
// machine:
//
//	opening ─▶ first role's narrate
//	narrate ─▶ act ─[submit]──────────────▶ ponder(2s) ──▶ sleep ─▶ settle(3s) ─▶ next role
//	narrate ─▶ act ─[NightPass]───────────▶ ponder(2s) ──▶ sleep ─▶ settle(3s) ─▶ next role
//	narrate ─▶ act ─[timeout 60s]─────────▶ ponder(2s) ──▶ sleep ─▶ settle(3s) ─▶ next role
//	narrate ──────────────────────────────▶ ponder(5–10s) ▶ sleep ─▶ settle(3s) ─▶ next role  (phantom)
//
// Every real turn passes through ponder: the act→ponder edge fires on a
// submission, an explicit NightPass (decline-to-act, opt-in roles), OR
// the act-window timeout, so all three are indistinguishable downstream.
// They differ ONLY in what triggers the edge — never in whether ponder
// happens.
//
// Each transition is driven by a single AdvancePhase command from the
// room layer (whose timer fires at the end of the sub-phase) OR, for
// the act→ponder edge, by the actor submitting a NightAction or a
// NightPass. The engine itself is timeless; the room translates
// wall-clock into AdvancePhase. Phantom turns substitute `ponder` for
// `act` so the audio cues (narrate + sleep) still play, but no action
// can be submitted. A phantom turn is taken when the role cannot act
// this night — no living holder, a spent one-shot, or a Consort block.
//
// A role that cannot take an effective action this turn (no living
// holder, a spent vigilante, or a Consort-blocked non-mafia holder) skips
// the act window entirely: its turn is phantom, substituting a randomized
// ponder for act (see roleTurnIsPhantom). Only a turn that reaches act
// gets the full DefaultActionDuration window (sized by the room; see
// internal/room/config.go subPhaseDuration).
//
// Note that NightSubOpening is a NIGHT-scoped sub-phase (no
// currentNightRole during it); all others are role-scoped.
//
// The zero value NightSubPhase("") means "not in any night sub-phase";
// it's the value returned outside PhaseNight or between role turns.
type NightSubPhase string

const (
	// NightSubOpening is the one-shot start-of-night beat that happens
	// AFTER PhaseChanged→night and BEFORE the first role's narrate.
	// It's the "City, go to sleep." cue plus a fixed pre-wake silence
	// so the room has time to settle before any role is named. No
	// action accepted, currentNightRole is empty.
	NightSubOpening NightSubPhase = "opening"

	// NightSubNarrate is the opening audio cue ("Mafia, wake up...").
	// No action accepted. Duration is sized to cover the spoken cue.
	NightSubNarrate NightSubPhase = "narrate"

	// NightSubAct is the actor's decision window. Real turns only;
	// phantom turns substitute NightSubPonder. NightAction submissions
	// are accepted only during this sub-phase (engine returns
	// ErrNotYourTurn otherwise).
	NightSubAct NightSubPhase = "act"

	// NightSubPonder is a short pause between the actor finishing and
	// the "go to sleep" outro. For real turns it gives the room a
	// breath to absorb that an action was just submitted; for phantom
	// turns it stands in for the missing act window so the cadence
	// can't be used to deduce that a role is dead. No action accepted.
	NightSubPonder NightSubPhase = "ponder"

	// NightSubSleep is the closing audio cue ("Mafia, go to sleep.").
	// No action accepted. Sized to cover the spoken cue.
	NightSubSleep NightSubPhase = "sleep"

	// NightSubSettle is a short fixed pause after sleep before the
	// next role's narrate (or, for the last role, before the
	// night→day transition). Lets the "go to sleep" cue land cleanly
	// before the next narrator line begins. No action accepted.
	NightSubSettle NightSubPhase = "settle"
)

// newState constructs an empty state in PhaseLobby. Unexported because
// callers should always go through Game.Apply(CreateGame{...}).
func newState() *GameState {
	return &GameState{
		phase: PhaseLobby,
	}
}

// --- read-only accessors ---------------------------------------------------
//
// Only accessors with a production caller (or a cross-package test that
// can't reach an in-package export_test.go) live here. The remaining
// inspection getters are test-only and defined in export_test.go, so they
// stay out of production builds.

// Players returns a copy of the player list in join order. The copy keeps
// callers from accidentally mutating the engine's slice. Used by the room
// layer (oldestConnectedPlayer) to promote a new host in join order.
func (s *GameState) Players() []Player {
	out := make([]Player, len(s.players))
	copy(out, s.players)
	return out
}

// HasLivingRole reports whether at least one living player holds the
// given role. Used internally by roleTurnIsPhantom to decide whether a
// night turn has an actionable holder.
func (s *GameState) HasLivingRole(r Role) bool {
	return s.anyPlayer(func(p *Player) bool { return p.alive && p.role == r })
}

// roleTurnIsPhantom reports whether a night turn for role r should run
// as a phantom turn — narrate straight into a ponder, skipping the act
// window. "Phantom" here means "no holder who can take an effective
// action this turn", which covers three cases:
//
//   - r has no living holder (a dead role): the classic phantom turn,
//     narrated for cadence/secrecy but with no one to act.
//   - r is the Vigilante and his single bullet is already spent: he's
//     alive but has nothing left to do.
//   - r's living holder was roleblocked by the Consort this night (mafia
//     are immune): the holder is present but neutralized, so there is
//     nothing for him to submit either.
//
// All three collapse onto the same narrate->ponder cadence (no act
// window). Because a phantom ponder is the same randomized 5-10s beat
// regardless of the reason, a dead / spent / blocked role is
// timing-indistinguishable to an observer, so the Consort's block stops
// being a tell and a spent vigilante can't be told from a dead one.
//
// This is the single source of truth for the narrate->act/ponder branch
// (advanceNightSubPhase) and the Phantom flag stamped on
// NightSubPhaseStarted (enterNightSubPhase).
func (s *GameState) roleTurnIsPhantom(r Role) bool {
	if r == "" {
		return false
	}
	// The Mafia turn is faction-collective: it opens whenever ANY living
	// FactionMafia member exists, not just a strict RoleMafia holder. A
	// living Yakuza (FactionMafia) keeps the turn real so it can take the
	// kill or recruit even after every strict mafioso is dead. (HasLivingRole
	// would wrongly phantom the turn in that case.)
	if r == RoleMafia {
		return s.factionLivingCount(FactionMafia) == 0
	}
	if !s.HasLivingRole(r) {
		return true
	}
	if r == RoleVigilante && s.vigilanteShotUsed {
		return true
	}
	// A non-mafia holder that was neutralized this night (Consort-blocked OR
	// the Yakuza's recruit target) is present but can't act. Both the Consort
	// and the Yakuza act earlier in the night (the Yakuza during the Mafia
	// turn, which leads the queue), so the neutralization is already recorded
	// by the time a town role's turn begins and this is consulted.
	if holder, ok := s.livingHolderOf(r); ok && s.isNightNeutralized(holder) {
		return true
	}
	return false
}

// clearRecruit resets the in-flight Yakuza recruit fields. Called at the end
// of resolveNight (the night-end cleanup, alongside clearing pendingNight) so
// a recruit never leaks across nights.
func (s *GameState) clearRecruit() {
	s.recruitPending = false
	s.recruitYakuza = ""
	s.recruitTarget = ""
}

// isActorsTurn reports whether it is currently actor's turn to submit a
// night action. The base rule is strict role equality with currentNightRole;
// the Mafia turn is the faction-collective exception — any living FactionMafia
// member (a strict mafioso or the Yakuza) may submit during the RoleMafia turn.
func (s *GameState) isActorsTurn(actor *Player) bool {
	if actor.role == s.currentNightRole {
		return true
	}
	return s.currentNightRole == RoleMafia && actor.role.Faction() == FactionMafia
}

// isNightNeutralized reports whether pid's night action is nullified this
// night — either a living Consort blocked them (isNightBlocked) OR the
// Yakuza recruited them (recruitPending and recruitTarget == pid; the
// recruit suppresses the target's own power the night they're converted).
// Used by roleTurnIsPhantom and the act-time/resolve-time backstops. The
// Blocked-vs-Recruited NOTICE distinction is made at the call sites that
// emit it (enterNightSubPhase), which still consult isNightBlocked directly.
func (s *GameState) isNightNeutralized(pid PlayerID) bool {
	return s.isNightBlocked(pid) || (s.recruitPending && s.recruitTarget == pid)
}

// allMafiaFactionIDs returns the ids of every player whose role is in the
// mafia faction (strict RoleMafia or RoleYakuza), ALIVE OR DEAD, in join
// order. Because roles never revert out of the faction (the only role change
// is Consort→Mafia, which moves INTO it), this is the full historical cabal:
// the original mafia, the Yakuza, any recruited convert, and a promoted
// consort.
//
// It is used to re-issue the roster to a NEW faction member (a promoted
// consort, or a fresh recruit) so they see every predecessor — the dead
// mafia and the dead Yakuza — as well as any living teammates, matching what
// a rejoin reconstructs from the StartGame roster (faction visibility is
// evaluated at the viewer's CURRENT faction, so a rejoining member replays
// the whole faction history). Living town never receives any of these events,
// so the dead-role information stays inside the faction.
func (s *GameState) allMafiaFactionIDs() []PlayerID {
	var out []PlayerID
	for i := range s.players {
		if s.players[i].role.Faction() == FactionMafia {
			out = append(out, s.players[i].id)
		}
	}
	return out
}

// yakuzaPlayerID returns the id of the player dealt RoleYakuza (alive or
// dead), or "" if the role was not in this game. Used to set the Yakuza
// field on a re-issued roster so a new faction member can badge the
// (possibly dead) Yakuza distinctly, consistent with the StartGame reveal.
func (s *GameState) yakuzaPlayerID() PlayerID {
	for i := range s.players {
		if s.players[i].role == RoleYakuza {
			return s.players[i].id
		}
	}
	return ""
}

// currentMafiaRoster snapshots the full historical cabal from current state —
// every faction member (alive or dead, via allMafiaFactionIDs) plus the
// Yakuza badge (via yakuzaPlayerID) — as a re-issued roster event. The
// Members/Yakuza pairing is the canonical "current full cabal" view, so the
// two re-issue sites (a fresh recruit, a promoted consort) build it through
// here rather than restating the pair. NOT used by the StartGame reveal:
// that builds its roster from ids accumulated in the deal loop, not a
// post-deal state scan.
func (s *GameState) currentMafiaRoster() MafiaRosterRevealed {
	return MafiaRosterRevealed{
		Members: s.allMafiaFactionIDs(),
		Yakuza:  s.yakuzaPlayerID(),
	}
}

// findPlayer returns a pointer to the player record and true, or nil and
// false if no such player exists. Unexported: rule helpers use it; the
// outside world only sees the value type via Players().
func (s *GameState) findPlayer(id PlayerID) (*Player, bool) {
	for i := range s.players {
		if s.players[i].id == id {
			return &s.players[i], true
		}
	}
	return nil, false
}

// requireLivingPlayer resolves id to a player record, returning
// ErrUnknownPlayer if no such player exists or ErrPlayerDead if they're
// dead. It centralizes the actor/target liveness checks shared by the
// night and day command handlers.
func (s *GameState) requireLivingPlayer(id PlayerID) (*Player, error) {
	p, ok := s.findPlayer(id)
	if !ok {
		return nil, ErrUnknownPlayer
	}
	if !p.alive {
		return nil, ErrPlayerDead
	}
	return p, nil
}

// countPlayers returns the number of players satisfying pred. It is the
// shared skeleton for the various living/faction counters below.
func (s *GameState) countPlayers(pred func(*Player) bool) int {
	n := 0
	for i := range s.players {
		if pred(&s.players[i]) {
			n++
		}
	}
	return n
}

// anyPlayer reports whether any player satisfies pred.
func (s *GameState) anyPlayer(pred func(*Player) bool) bool {
	_, ok := s.firstPlayer(pred)
	return ok
}

// firstPlayer returns a pointer to the first player (in join order)
// satisfying pred, or nil and false if none do.
func (s *GameState) firstPlayer(pred func(*Player) bool) (*Player, bool) {
	for i := range s.players {
		if pred(&s.players[i]) {
			return &s.players[i], true
		}
	}
	return nil, false
}

// livingCount returns the total number of currently alive players,
// regardless of faction. Used by the day-vote resolver to compute the
// strict-majority threshold (a lynch requires more than half the
// living players to agree on a single target).
func (s *GameState) livingCount() int {
	return s.countPlayers(func(p *Player) bool { return p.alive })
}

// isNightBlocked reports whether a living Consort has targeted pid with
// her block this night. It scans the in-flight pendingNight map for a
// consort->pid entry. Used in three places: by roleTurnIsPhantom to route
// a blocked holder's turn through a phantom ponder (no act window), by
// enterNightSubPhase to emit the private Blocked notice when that ponder
// begins, and at submit time (applyNightAction) as a backstop that rejects
// a blocked non-mafia actor with ErrBlocked. Mafia are immune - the caller
// guards on the role before consulting this.
func (s *GameState) isNightBlocked(pid PlayerID) bool {
	for actor, target := range s.pendingNight {
		if target != pid {
			continue
		}
		if ap, ok := s.findPlayer(actor); ok && ap.role == RoleConsort {
			return true
		}
	}
	return false
}

// livingHolderOf returns the first living player holding role r, in join
// order. Used to notify a roleblocked town actor at the start of their
// own turn (detective/doctor are unique, so "first" is "the" holder).
func (s *GameState) livingHolderOf(r Role) (PlayerID, bool) {
	if p, ok := s.firstPlayer(func(p *Player) bool { return p.alive && p.role == r }); ok {
		return p.id, true
	}
	return "", false
}

// mafiaAlignedLivingCount returns the number of living players on the
// mafia side — the strict mafia plus a not-yet-promoted consort. Used by
// checkWin's TOWN-win branch: the town wins only once EVERY mafia-aligned
// player is dead, the consort included (she must be lynched). The MAFIA
// parity win, by contrast, counts only the strict mafia faction — a
// living consort does not pad it (see checkWin).
func (s *GameState) mafiaAlignedLivingCount() int {
	return s.countPlayers(func(p *Player) bool {
		return p.alive && p.role.Faction().MafiaAligned()
	})
}

// factionLivingCount returns the number of currently alive members of f.
func (s *GameState) factionLivingCount(f Faction) int {
	return s.countPlayers(func(p *Player) bool {
		return p.alive && p.role.Faction() == f
	})
}

// finalRolesSnapshot copies player -> role for the GameEnded event. Only
// safe to call at game end since it exposes every role publicly.
func (s *GameState) finalRolesSnapshot() map[PlayerID]Role {
	out := make(map[PlayerID]Role, len(s.players))
	for _, p := range s.players {
		out[p.id] = p.role
	}
	return out
}
