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
)

// Faction is the win-condition group a role belongs to. Town wins when all
// mafia are dead; Mafia wins when mafia ≥ remaining town.
type Faction string

const (
	FactionTown  Faction = "town"
	FactionMafia Faction = "mafia"
)

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
