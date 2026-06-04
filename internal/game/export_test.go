package game

// Test-only read accessors.
//
// These live in a _test.go file so the compiler includes them ONLY when
// building the package's tests; they never ship in a production binary.
// That keeps inspection-only surface — getters no production code calls —
// out of the source files (state.go).
//
// Accessors that production OR a cross-package test depends on stay in
// state.go instead: GameState.Players and HasLivingRole, and Player.ID.

// ID returns the game identifier.
func (s *GameState) ID() GameID { return s.id }

// Phase returns the current phase.
func (s *GameState) Phase() Phase { return s.phase }

// PlayerCount returns the number of players in the game (alive or dead).
func (s *GameState) PlayerCount() int { return len(s.players) }

// MinPlayers returns the minimum player count required to start.
func (s *GameState) MinPlayers() int { return s.minPlayers }

// MaxPlayers returns the hard cap on AddPlayer.
func (s *GameState) MaxPlayers() int { return s.maxPlayers }

// MafiaCount returns the configured number of mafia for the upcoming game.
func (s *GameState) MafiaCount() int { return s.mafiaCount }

// ConsortEnabled reports whether the optional Consort role is toggled on.
func (s *GameState) ConsortEnabled() bool { return s.consortEnabled }

// VigilanteEnabled reports whether the optional Vigilante role is toggled on.
func (s *GameState) VigilanteEnabled() bool { return s.vigilanteEnabled }

// YakuzaEnabled reports whether the optional Yakuza role is toggled on.
func (s *GameState) YakuzaEnabled() bool { return s.yakuzaEnabled }

// VigilanteShotUsed reports whether the Vigilante has already fired his
// single bullet on a prior night.
func (s *GameState) VigilanteShotUsed() bool { return s.vigilanteShotUsed }

// DayLynchResolved reports whether the current day already finalized a vote.
func (s *GameState) DayLynchResolved() bool { return s.dayLynchResolved }

// VotesRevealed reports whether the host has revealed the current tally.
func (s *GameState) VotesRevealed() bool { return s.votesRevealed }

// CurrentNightRole returns the role whose night turn is active, or the
// empty Role between turns / outside Night.
func (s *GameState) CurrentNightRole() Role { return s.currentNightRole }

// CurrentNightSubPhase returns the active night sub-phase, or the empty
// NightSubPhase between turns / outside Night.
func (s *GameState) CurrentNightSubPhase() NightSubPhase { return s.currentNightSubPhase }

// Name returns the player's display name.
func (p Player) Name() string { return p.name }

// Role returns the player's dealt role. It is the zero value Role("") in
// PhaseLobby (before StartGame).
func (p Player) Role() Role { return p.role }

// Alive reports whether the player is currently alive.
func (p Player) Alive() bool { return p.alive }
