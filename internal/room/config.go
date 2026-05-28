package room

import (
	"log/slog"
	mrand "math/rand/v2"
	"time"

	"github.com/malhar/mafia-the-game/internal/game"
)

// Config tunes a room. All fields are optional; zero values get safe
// defaults. We keep this minimal — only knobs we actually need today.
type Config struct {
	// GameID is the engine's game identifier; if empty, the room
	// generates one from its code.
	GameID game.GameID

	// MinPlayers, MaxPlayers, MafiaCount describe the variable lobby
	// configuration the engine will use. Zero values fall back to
	// engine defaults (currently 5..20 players, ~maxPlayers/3 mafia).
	MinPlayers int
	MaxPlayers int
	MafiaCount int

	// Seed is the deterministic shuffle seed passed to the engine.
	// 0 (the default) is a valid seed.
	Seed int64

	// NightActionDuration is the time a role has to ACT on its night
	// turn, measured from the moment the spoken narration prompt
	// finishes. Default DefaultNightActionDuration.
	//
	// The total per-turn deadline broadcast to clients is
	//   NightTurnGrace(role, day) + NightActionDuration
	// so that the actor still gets their full action window even when
	// the prompt audio (or its visible-card fallback) is mid-playback
	// when the turn starts.
	NightActionDuration time.Duration

	// NightTurnGrace returns the audio-grace period for the given role
	// on the given day (engine's state.day, 0 for the first night).
	// It's added BEFORE NightActionDuration to form the per-turn
	// deadline. Default returns DefaultNightTurnGrace.
	//
	// Day 0 mafia gets a longer grace because the narration includes
	// the "look around and recognize each other" beat.
	NightTurnGrace func(role game.Role, day int) time.Duration

	// PhantomTurnDuration returns the wall-clock duration of a phantom
	// night turn — a turn for a role with no living holder. The
	// narrator audio still plays so the room can't deduce a role is
	// dead from the missing cues; but no action will be accepted, so
	// we shorten the timer to something plausible (a fast-acting
	// actor's range) rather than the full grace + 45s.
	//
	// Default DefaultPhantomTurnDuration returns a uniformly-random
	// duration in [8s, 20s]. The room owns the randomness so the
	// engine stays deterministic.
	PhantomTurnDuration func(role game.Role, day int) time.Duration

	// DetectivePauseDuration is the wall-clock pause inserted AFTER a
	// detective records a night action and BEFORE the next role's
	// turn begins. The engine emits DetectiveResult at action time
	// (so the detective's modal pops immediately) but stops short of
	// starting the doctor's turn — this pause is what gives the
	// detective a beat to read the result before audio narration for
	// the doctor kicks in. Default DefaultDetectivePauseDuration.
	DetectivePauseDuration time.Duration

	// MaxLifetime is the hard upper bound on a room's wall-clock age.
	// Once a room has existed for this long the manager's sweeper
	// closes it unconditionally — no matter how many subscribers are
	// connected, no matter whether the game has ended. This is the
	// ONLY reap policy: rooms with active connections, idle lobbies,
	// completed games, and abandoned-but-attached zombies all live
	// up to this cap and then get cleared.
	//
	// Counts from CreateRoom. Default DefaultMaxLifetime. Zero or
	// negative disables reaping (useful for tests / future
	// deployments).
	MaxLifetime time.Duration

	// Logger is used for room-lifetime events. Defaults to slog.Default().
	Logger *slog.Logger
}

// DefaultNightActionDuration is the action window every role gets on
// its night turn, measured from when the narration prompt finishes.
const DefaultNightActionDuration = 45 * time.Second

// DefaultDetectivePauseDuration is how long the room waits after a
// detective records a night action before kicking off the next
// queued role's turn. Tuned to comfortably cover "read the modal +
// click Got it" without dragging the night out.
const DefaultDetectivePauseDuration = 3 * time.Second

// DefaultMaxLifetime caps how long any room may live before the
// manager forcibly closes it. Long enough that no real game session
// — including breaks, rules debates, and the social postgame — will
// brush up against it; short enough that abandoned rooms (any
// flavor: empty, full of zombies, ended) don't accumulate forever
// on a long-running server.
const DefaultMaxLifetime = 10 * time.Hour

// DefaultNightTurnGrace returns the per-role audio grace used when
// Config.NightTurnGrace is nil. The values are tuned to match the
// narrator script in web/index.html — kept in sync intentionally so
// the action window starts roughly when the spoken prompt ends.
//
// Every night opens with a shared "City, go to sleep" cue (~1.5s)
// queued by narratePhaseChange, then a 5s pre-wake beat so the
// room has time to settle before any role is named. After that
// the mafia turn's audio plays — duration depends on whether
// it's Night 1 (the longer "look around" beat) or any later night
// (single "Mafia, wake up. Choose your target." cue).
//
// Night 1 mafia timeline (relative to NightTurnStarted dispatch):
//
//	t=0       "City, go to sleep." (~1.5s)         [from narratePhaseChange]
//	t=5s      "Mafia, wake up. Look around..." (~2.5s)
//	t=9s      "Mafia, choose your target." (~1.5s)
//	t=10.5s   audio complete  → grace ~10s
//
// Later nights collapse to a single mafia cue:
//
//	t=0       "City, go to sleep." (~1.5s)
//	t=5s      "Mafia, wake up. Choose your target." (~1.5s)
//	t=6.5s    audio complete  → grace ~7s
//
// Detective and doctor turns start AFTER the mafia turn ends and
// have no "City, go to sleep" preface (the room is already
// settled), so their grace stays at 2.5s to cover a single short
// prompt.
func DefaultNightTurnGrace(role game.Role, day int) time.Duration {
	if role == game.RoleMafia {
		if day == 0 {
			return 10 * time.Second
		}
		return 7 * time.Second
	}
	return 2500 * time.Millisecond
}

// PhantomTurnMin and PhantomTurnMax bound the randomized duration of a
// phantom night turn. The values are tuned so a phantom turn feels like
// a real turn where the actor decided quickly: long enough to be
// plausible, short enough not to drag the night out per dead role.
const (
	PhantomTurnMin = 8 * time.Second
	PhantomTurnMax = 20 * time.Second
)

// DefaultPhantomTurnDuration returns a uniformly-random wall-clock
// duration in [PhantomTurnMin, PhantomTurnMax] for a phantom (dead-
// role) night turn.
//
// Why no mafia branch: a phantom MAFIA turn is unreachable. The
// engine's checkWin runs after every state change that can kill a
// player; the moment living-mafia hits zero it emits GameEnded with
// Winner=FactionTown and transitions to PhaseEnded. beginNightTurns
// therefore can only be invoked while at least one mafia is alive,
// so NightTurnStarted{Role: RoleMafia} always has Phantom=false and
// is routed through NightTurnGrace + NightActionDuration in
// nightTurnDuration — never through this function.
//
// Phantom turns DO occur for detective and doctor (one dies, but
// the game continues), and their single ~2.5s prompt fits
// comfortably inside the [PhantomTurnMin, PhantomTurnMax] bounds
// without any role-specific floor.
//
// The role + day parameters are kept on the signature because the
// custom-grace test (and future deployments) may want to override
// based on them; the default just doesn't read them.
func DefaultPhantomTurnDuration(_ game.Role, _ int) time.Duration {
	lo, hi := PhantomTurnMin, PhantomTurnMax
	span := hi - lo
	if span <= 0 {
		return lo
	}
	return lo + time.Duration(mrand.Int64N(int64(span)+1))
}

func (c *Config) applyDefaults() {
	// Zero values for MinPlayers/MaxPlayers/MafiaCount are intentionally
	// left as-is so the engine's CreateGame can apply its own defaults
	// (keeping the "what's the default lobby?" answer in one place).
	if c.NightActionDuration == 0 {
		c.NightActionDuration = DefaultNightActionDuration
	}
	if c.NightTurnGrace == nil {
		c.NightTurnGrace = DefaultNightTurnGrace
	}
	if c.PhantomTurnDuration == nil {
		c.PhantomTurnDuration = DefaultPhantomTurnDuration
	}
	if c.DetectivePauseDuration == 0 {
		c.DetectivePauseDuration = DefaultDetectivePauseDuration
	}
	if c.MaxLifetime == 0 {
		c.MaxLifetime = DefaultMaxLifetime
	}
	if c.Logger == nil {
		c.Logger = slog.Default()
	}
}

// nightTurnDuration returns the total per-turn deadline for the given
// role on the given day. Real turns use NightTurnGrace +
// NightActionDuration; phantom turns (no living holder of role) use
// the shorter PhantomTurnDuration to avoid stalling the night while
// still letting the audio cue play.
func (c *Config) nightTurnDuration(role game.Role, day int, phantom bool) time.Duration {
	if phantom {
		return c.PhantomTurnDuration(role, day)
	}
	return c.NightTurnGrace(role, day) + c.NightActionDuration
}
