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

// Faction returns which faction this role wins with.
func (r Role) Faction() Faction {
	if r == RoleMafia {
		return FactionMafia
	}
	return FactionTown
}

// Valid reports whether r is a known role.
func (r Role) Valid() bool {
	switch r {
	case RoleVillager, RoleMafia, RoleDetective, RoleDoctor:
		return true
	}
	return false
}
