package game

// PlayerID uniquely identifies a player within a single game. It is a typed
// string so the compiler prevents accidentally mixing it with other string
// IDs (room codes, game IDs, etc.).
type PlayerID string

// GameID uniquely identifies a game (one per room, for v1).
type GameID string
