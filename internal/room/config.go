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
// Day 0 mafia spans two utterances with a beat between them:
//
//	"Mafia, wake up. Look around and recognize each other." (~2.5s)
//	[3.6s pause]
//	"Mafia, choose your target." (~1.5s)
//
// So the grace there is ~5.5s. Other roles play a single short prompt
// (~2s) and get a small buffer to round out to 2.5s.
func DefaultNightTurnGrace(role game.Role, day int) time.Duration {
	if role == game.RoleMafia && day == 0 {
		return 5500 * time.Millisecond
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
// role) night turn. We bake in the audio grace for Day 0 mafia so the
// "look around" beat has time to play; for other roles the lower
// bound already comfortably covers the ~2.5s prompt.
func DefaultPhantomTurnDuration(role game.Role, day int) time.Duration {
	lo, hi := PhantomTurnMin, PhantomTurnMax
	if role == game.RoleMafia && day == 0 {
		// The Day-0 mafia narration takes ~5.5s on its own; bump the
		// floor so the phantom turn never undercuts the audio.
		if lo < 7*time.Second {
			lo = 7 * time.Second
		}
	}
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
