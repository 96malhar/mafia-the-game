package game_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/96malhar/mafia-the-game/internal/game"
)

// This suite is the FAIL-CLOSED net over the engine's single most
// security-critical contract: "full truth then redact" — every Event
// carries a Visibility and Project (projection.go) is the one place that
// redacts per viewer. A single mis-tagged Visibility() (e.g. a stray
// Public() on a role-bearing event) would leak the roster to the whole
// table in one line. The existing projection_test.go / spectator_test.go
// suites spot-check hand-picked (viewer, event) pairs; this suite instead
// quantifies over EVERY event type and forces a new one to be classified
// before it can compile-and-pass.
//
// Three layers, in increasing strength:
//
//  1. Exhaustiveness — an AST scan of event.go enumerates the closed Event
//     universe (every type with an isEvent() method) and asserts each one
//     is classified in eventRedactionCases below. Add an event → the scan
//     finds it → this test fails until you classify it. This is the
//     fail-closed forcing function (mirrors TestRegistry_RoleConstantsHaveSpecs).
//  2. Public whitelist — a literal set of the events that are legitimately
//     Public(). An accidental Public() on a secret-bearing event is caught
//     here because the new type won't be in the whitelist yet.
//  3. Redaction property — for every non-public event, no viewer outside its
//     audience receives anything; the intended audience does.
//
// Roster from fixedRoster(t): mafia1 (mafia), det (detective), doc (doctor),
// town1, town2 (villagers) — all alive. Mafia faction == {mafia1}.

// publicEventWhitelist is the explicit set of event types allowed to be
// Public(). Adding a new public event is a deliberate edit here; that edit
// is the review gate. Anything NOT listed must redact (non-public
// Visibility), or layer 2 fails.
var publicEventWhitelist = map[string]bool{
	"GameCreated":          true,
	"MafiaCountChanged":    true,
	"ConsortChanged":       true,
	"VigilanteChanged":     true,
	"YakuzaChanged":        true,
	"TrackerChanged":       true,
	"PlayerJoined":         true,
	"HostChanged":          true,
	"GameStarted":          true,
	"PhaseChanged":         true,
	"NightSubPhaseStarted": true,
	"PlayerKilled":         true,
	"VoteProgress":         true,
	"VotesRevealed":        true,
	"VoteCleared":          true,
	"PlayerLynched":        true,
	"NoLynch":              true,
	"GameEnded":            true,
	"GameReset":            true,
}

// eventRedactionCase is one classified event sample. name MUST equal the
// Go type name (cross-checked against the AST scan). audience is the
// EXPECTED Visibility().Audience for this constructed instance — asserted
// against the real Visibility() so a change to an event's visibility forces
// a deliberate update here. recipients lists the viewers who SHOULD see it
// in the relevant state (empty for graveyard events, which only the dead
// see — covered separately).
type eventRedactionCase struct {
	name       string
	ev         game.Event
	audience   string
	recipients []game.PlayerID
}

// eventRedactionCases constructs one instance of every event type, with any
// secret fields populated so a leak would be observable. The conditional
// NightActionRecorded appears twice (its visibility depends on the Faction
// field) — both entries share the type name, so the exhaustiveness set
// still dedupes to one.
func eventRedactionCases() []eventRedactionCase {
	roleMap := map[game.PlayerID]game.Role{
		"mafia1": game.RoleMafia, "det": game.RoleDetective,
		"doc": game.RoleDoctor, "town1": game.RoleVillager, "town2": game.RoleVillager,
	}
	return []eventRedactionCase{
		// --- Public events (no redaction; whitelist + audience checks only) ---
		{"GameCreated", game.GameCreated{GameID: "g1", MinPlayers: 5, MaxPlayers: 20, MafiaCount: 1}, "public", nil},
		{"MafiaCountChanged", game.MafiaCountChanged{From: 1, To: 2}, "public", nil},
		{"ConsortChanged", game.ConsortChanged{Enabled: true}, "public", nil},
		{"VigilanteChanged", game.VigilanteChanged{Enabled: true}, "public", nil},
		{"YakuzaChanged", game.YakuzaChanged{Enabled: true}, "public", nil},
		{"TrackerChanged", game.TrackerChanged{Enabled: true}, "public", nil},
		{"PlayerJoined", game.PlayerJoined{PlayerID: "town1", Name: "town1"}, "public", nil},
		{"HostChanged", game.HostChanged{PlayerID: "mafia1"}, "public", nil},
		{"GameStarted", game.GameStarted{}, "public", nil},
		{"PhaseChanged", game.PhaseChanged{From: game.PhaseNight, To: game.PhaseDayDiscussion, Day: 1}, "public", nil},
		{"NightSubPhaseStarted", game.NightSubPhaseStarted{Sub: game.NightSubAct, Role: game.RoleMafia, Day: 1}, "public", nil},
		{"PlayerKilled", game.PlayerKilled{PlayerID: "town2"}, "public", nil},
		{"VoteProgress", game.VoteProgress{Day: 1, Cast: 2}, "public", nil},
		{"VotesRevealed", game.VotesRevealed{Day: 1, Tally: map[game.PlayerID]game.PlayerID{"det": "mafia1"}}, "public", nil},
		{"VoteCleared", game.VoteCleared{Day: 1}, "public", nil},
		{"PlayerLynched", game.PlayerLynched{PlayerID: "mafia1"}, "public", nil},
		{"NoLynch", game.NoLynch{Day: 1}, "public", nil},
		{"GameEnded", game.GameEnded{Winner: game.FactionTown, FinalRoles: roleMap}, "public", nil},
		{"GameReset", game.GameReset{MinPlayers: 5, MaxPlayers: 20, MafiaCount: 1}, "public", nil},

		// --- Player-private events: only the named recipient may see them ---
		{"RoleAssigned", game.RoleAssigned{PlayerID: "det", Role: game.RoleDetective}, "player", ids("det")},
		{"Recruited", game.Recruited{PlayerID: "town1"}, "player", ids("town1")},
		{"Blocked", game.Blocked{PlayerID: "doc"}, "player", ids("doc")},
		{"ConsortPromoted", game.ConsortPromoted{PlayerID: "town1"}, "player", ids("town1")},
		{"DetectiveResult", game.DetectiveResult{Detective: "det", Target: "mafia1", IsMafia: true}, "player", ids("det")},
		{"TrackerResult", game.TrackerResult{Tracker: "town1", Target: "mafia1", Visited: "town2"}, "player", ids("town1")},
		{"VoteCast", game.VoteCast{Voter: "det", Target: "mafia1"}, "player", ids("det")},
		{"VoteChanged", game.VoteChanged{Voter: "det", From: "mafia1", To: "doc"}, "player", ids("det")},
		{"VoteRetracted", game.VoteRetracted{Voter: "det", Was: "mafia1"}, "player", ids("det")},
		{"VoteAbstained", game.VoteAbstained{Voter: "det"}, "player", ids("det")},
		// NightActionRecorded, town variant: a solo town role self-acks privately.
		{"NightActionRecorded", game.NightActionRecorded{Actor: "det", Target: "mafia1", Faction: game.FactionTown}, "player", ids("det")},

		// --- Faction (mafia) events: only LIVING mafia may see them ---
		{"MafiaRosterRevealed", game.MafiaRosterRevealed{Members: ids("mafia1")}, "faction", ids("mafia1")},
		{"RecruitRecorded", game.RecruitRecorded{Yakuza: "mafia1", Target: "town1"}, "faction", ids("mafia1")},
		// NightActionRecorded, mafia variant: faction-scoped kill ack.
		{"NightActionRecorded", game.NightActionRecorded{Actor: "mafia1", Target: "town1", Faction: game.FactionMafia}, "faction", ids("mafia1")},

		// --- Graveyard events: only the DEAD may see them (recipients empty;
		//     positive direction is asserted in the dead-viewer state) ---
		{"SpectatorNightAction", game.SpectatorNightAction{Actor: "mafia1", ActorRole: game.RoleMafia, Target: "town1", TargetRole: game.RoleVillager}, "dead", nil},
		{"RosterRevealed", game.RosterRevealed{Roles: roleMap}, "dead", nil},
	}
}

func ids(s ...game.PlayerID) []game.PlayerID { return s }

// Layer 1: every event type in the source is classified above.
func TestProjectionRedaction_EveryEventTypeIsClassified(t *testing.T) {
	fromSource := eventTypeNamesFromSource(t)

	classified := map[string]bool{}
	for _, c := range eventRedactionCases() {
		classified[c.name] = true
	}

	for name := range fromSource {
		require.Truef(t, classified[name],
			"event type %q has an isEvent() method but no entry in eventRedactionCases — "+
				"classify it (and add it to publicEventWhitelist iff it is genuinely Public)", name)
	}
	// And nothing stale: every classified name must be a real event type.
	for name := range classified {
		require.Truef(t, fromSource[name],
			"eventRedactionCases lists %q but event.go has no such Event type", name)
	}
}

// Layer 2: the public whitelist and each sample's actual Visibility() agree.
func TestProjectionRedaction_PublicEventsWhitelisted(t *testing.T) {
	for _, c := range eventRedactionCases() {
		got := c.ev.Visibility().Audience
		require.Equalf(t, c.audience, got,
			"%s: classified as %q but Visibility().Audience is %q — update the case", c.name, c.audience, got)

		if got == "public" {
			require.Truef(t, publicEventWhitelist[c.name],
				"%s is Public() but not in publicEventWhitelist — a new public event must be added "+
					"deliberately (this is the review gate against an accidental Public() on a secret event)", c.name)
		} else {
			require.Falsef(t, publicEventWhitelist[c.name],
				"%s is whitelisted as Public() but its Visibility().Audience is %q", c.name, got)
		}
	}
}

// Layer 3: no secret event reaches a viewer outside its audience.
func TestProjectionRedaction_NoSecretLeaks(t *testing.T) {
	aliveState := fixedRoster(t).State() // all five alive

	// A state with a dead player, for the graveyard direction: mafia kills
	// town2 with no doctor save, landing in PhaseDayDiscussion.
	gDead := fixedRoster(t)
	playNight(t, gDead, map[game.Role]game.PlayerID{game.RoleMafia: "town2"})
	require.False(t, livingByID(gDead, "town2"), "precondition: town2 is dead in the graveyard state")
	deadState := gDead.State()

	allViewers := []game.PlayerID{"mafia1", "det", "doc", "town1", "town2", "stranger", ""}

	// seeExactly asserts that — projecting just this one event against state —
	// every viewer in want sees it and every other viewer sees nothing.
	seeExactly := func(t *testing.T, st *game.GameState, ev game.Event, want []game.PlayerID) {
		t.Helper()
		recip := map[game.PlayerID]bool{}
		for _, r := range want {
			recip[r] = true
		}
		for _, v := range allViewers {
			out := game.Project(v, []game.Event{ev}, st)
			if recip[v] {
				require.NotEmptyf(t, out, "%T: recipient %q must see it", ev, v)
			} else {
				require.Emptyf(t, out, "%T: viewer %q must NOT see it (secret leak!)", ev, v)
			}
		}
	}

	for _, c := range eventRedactionCases() {
		t.Run(c.name+"/"+c.audience, func(t *testing.T) {
			switch c.audience {
			case "public":
				// Public is visible to everyone, including unknown/empty viewers.
				seeExactly(t, aliveState, c.ev, allViewers)
			case "player", "faction":
				// Negative for all non-recipients (incl. stranger/""), positive
				// for the living recipient(s). In the all-alive state.
				seeExactly(t, aliveState, c.ev, c.recipients)
			case "dead":
				// Living see nothing (all-alive state → nobody is in the
				// graveyard); the dead viewer sees it (graveyard state).
				seeExactly(t, aliveState, c.ev, nil)
				seeExactly(t, deadState, c.ev, ids("town2"))
			default:
				t.Fatalf("unexpected audience %q for %s", c.audience, c.name)
			}
		})
	}

	// A dead viewer must also lose access to a still-living faction's secrets
	// (the "dead mafia loses faction comms" rule, exercised here from the
	// dead side): town2 is dead, so it sees no mafia-faction event either.
	t.Run("dead_viewer_denied_faction_secret", func(t *testing.T) {
		roster := game.MafiaRosterRevealed{Members: ids("mafia1")}
		out := game.Project("town2", []game.Event{roster}, deadState)
		require.Empty(t, out, "a dead player must not see a living faction's secret")
	})
}

// eventTypeNamesFromSource parses event.go in the package directory and
// returns the set of types that declare an isEvent() method — i.e. the
// complete, closed Event universe. Keying on the method (rather than a
// hand-maintained list) is what makes the exhaustiveness check self-
// maintaining: a new event type is found automatically. (ResetPlayer, the
// GameReset payload struct, has no isEvent() method and is correctly
// excluded.)
func eventTypeNamesFromSource(t *testing.T) map[string]bool {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "event.go", nil, 0)
	require.NoError(t, err, "parsing event.go for the Event-type universe")

	names := map[string]bool{}
	for _, decl := range f.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok || fd.Recv == nil || fd.Name.Name != "isEvent" || len(fd.Recv.List) == 0 {
			continue
		}
		switch rt := fd.Recv.List[0].Type.(type) {
		case *ast.Ident:
			names[rt.Name] = true
		case *ast.StarExpr:
			if id, ok := rt.X.(*ast.Ident); ok {
				names[id.Name] = true
			}
		}
	}
	require.NotEmpty(t, names, "AST scan found no Event types — parser/path bug")
	return names
}
