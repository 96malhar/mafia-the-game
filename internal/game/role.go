package game

// Role is the secret card a player is dealt at game start. Roles drive
// what actions a player may take at night and which faction wins the game.
//
// We intentionally model Role as an explicit string-backed enum (rather
// than iota integers) so that:
//   - JSON encoding is human-readable when we later send role events over
//     the wire ({"role":"detective"} rather than {"role":2}).
//   - Adding/removing roles doesn't shift the numeric values of others.
type Role string

const (
	RoleVillager  Role = "villager"
	RoleMafia     Role = "mafia"
	RoleDetective Role = "detective"
	RoleDoctor    Role = "doctor"

	// RoleConsort is an OPTIONAL mafia-aligned role (the host toggles it
	// on before StartGame). Each night she gets her own turn to "block"
	// one player, nullifying that player's night action (a blocked
	// doctor saves no one, a blocked detective learns nothing). She does
	// NOT know who the mafia are and they don't know her (her own
	// faction, FactionConsort, isolates her from mafia-scoped events).
	// She wins with the mafia, and if the entire mafia cabal is wiped
	// out while she still lives she is PROMOTED to full RoleMafia (see
	// promoteConsortIfNeeded) — the sleeper who takes over the kill.
	RoleConsort Role = "consort"
)

// Faction is the win-condition + knowledge group a role belongs to.
// Town wins when all mafia-aligned roles are dead; the mafia side wins
// when it reaches numerical parity with the town.
//
// Faction doubles as the visibility (knowledge) group for FactionOnly
// events. The Consort is mafia-ALIGNED for winning but sits in her own
// faction so she neither sees nor appears in mafia-scoped coordination
// (roster reveal, kill ack); see MafiaAligned for the win-side grouping.
type Faction string

const (
	FactionTown    Faction = "town"
	FactionMafia   Faction = "mafia"
	FactionConsort Faction = "consort"
)

// MafiaAligned reports whether this faction wins with the mafia. The
// Consort is a separate knowledge group (so she's kept out of mafia
// coordination) yet shares the mafia's win condition and reads as
// "mafia" to the detective — both of those use this predicate rather
// than a bare == FactionMafia check.
func (f Faction) MafiaAligned() bool {
	return f == FactionMafia || f == FactionConsort
}

// Faction returns which faction this role wins with. The answer is
// sourced from the role registry (roleSpecs in rolespec.go), which is
// the single source of truth for per-role metadata. An unknown role
// falls back to FactionTown — defensive, since the engine never deals
// roles outside the registry.
func (r Role) Faction() Faction {
	if spec, ok := roleSpecs[r]; ok {
		return spec.Faction
	}
	return FactionTown
}

// Valid reports whether r is a known role — i.e. it has an entry in
// the role registry.
func (r Role) Valid() bool {
	_, ok := roleSpecs[r]
	return ok
}
