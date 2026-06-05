package room

import (
	"log/slog"
	mrand "math/rand/v2"
	"time"

	"github.com/96malhar/mafia-the-game/internal/game"
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

// Package defaults. All durations are tuned to match the narrator
// script in web/index.html — keep them in sync intentionally.

// DefaultOpeningDuration is the night-scoped opening beat: the
// "City, go to sleep." cue (~1.5s) plus a fixed pre-wake silence
// (~5s) so the room has time to settle before any role's narrate.
// Total runs slightly longer than the audio to absorb TTS slop
// across browsers.
const DefaultOpeningDuration = 7 * time.Second

// DefaultActionDuration is the action window every role gets on
// its night turn. The client doesn't carry a competing constant —
// it shows the countdown by reading the server-stamped deadline on
// NightActionStarted, so this number is single-source-of-truth here.
const DefaultActionDuration = 60 * time.Second

// DefaultSettleDuration is the universal post-sleep beat. Lets the
// "Mafia, go to sleep." cue land cleanly before the next role's
// "wake up" begins.
const DefaultSettleDuration = 3 * time.Second

// DefaultPonderRealSubmit is the post-submit pause for non-detective
// real roles. A breath between "action recorded" and the "go to
// sleep" cue.
const DefaultPonderRealSubmit = 2 * time.Second

// DefaultPonderDetectiveSubmit is the post-submit pause for the
// detective. Sized to comfortably cover "read the modal + click Got
// it" without dragging the night out.
const DefaultPonderDetectiveSubmit = 3 * time.Second

// DefaultPhantomPonderMin and DefaultPhantomPonderMax bound the
// randomized duration of a phantom (dead-role) ponder sub-phase. The
// values are tuned so a phantom turn feels like a real turn where the
// actor decided quickly: long enough to be plausible, short enough
// not to drag the night out per dead role.
const (
	DefaultPhantomPonderMin = 5 * time.Second
	DefaultPhantomPonderMax = 10 * time.Second
)

// DefaultNarrateDuration is the universal narrate-cue duration for
// roles that don't need per-day variation. Tuned to comfortably cover
// a single short spoken prompt like "Detective, wake up. Choose
// someone to investigate."
//
// Per-role overrides live in DefaultNarrate below. Adding a new role
// with non-default narration: add a case there.
//
// COUPLED with the client narration text in web/index.html →
// ROLE_NARRATION. If you edit a spoken cue to be substantially
// longer, bump the matching duration here or the engine will advance
// to act mid-sentence. Clock the slowest TTS voice you support, add
// ~500ms slop.
const DefaultNarrateDuration = 2500 * time.Millisecond

// DefaultSleepDuration is the universal sleep-cue duration. Tuned to
// cover a single short closing line like "Mafia, go to sleep."
//
// COUPLED with ROLE_SLEEP in web/index.html (same rule as the narrate
// constants above).
const DefaultSleepDuration = 2 * time.Second

// DefaultMafiaNarrateDay0 is the Day-0-only mafia narrate duration.
// Day 1 mafia includes a "look around and recognize each other" beat
// in addition to the standard "Choose your target." cue, so the
// audio runs longer. From Day 1 onward we collapse to the standard
// per-night value.
const DefaultMafiaNarrateDay0 = 4 * time.Second

// DefaultMafiaNarrateDayN is the mafia narrate duration on every
// night after Day 0. Shorter than the universal default because
// mafia's per-night line ("Mafia, wake up. Choose your target.") is
// slightly shorter than the generic "<role>, wake up. Choose someone
// to <verb>" template.
const DefaultMafiaNarrateDayN = 1500 * time.Millisecond

// DefaultMaxLifetime caps how long any room may live before the
// manager forcibly closes it. Long enough that no real game session
// — including breaks, rules debates, and the social postgame — will
// brush up against it; short enough that abandoned rooms (any
// flavor: empty, full of zombies, ended) don't accumulate forever
// on a long-running server.
const DefaultMaxLifetime = 5 * time.Hour

// defaultSubPhaseDuration returns the built-in wall-clock duration for
// a night sub-phase. These values are the single source of timing
// truth (the engine is timeless).
//
// Everything the sizing needs rides on the event itself (Sub, Role, Day,
// Phantom). In particular there is no `blocked` input: a blocked actor's
// turn is phantom (no act window — see roleTurnIsPhantom), so it sizes
// through the phantom ponder branch like any other cannot-act turn.
// Submit vs timeout deliberately gets NO duration distinction (same audio
// cadence, so observers can't tell them apart).
//
// Narrate is the one sub-phase with per-role variation today: mafia's
// Day-0 "look around and recognize each other" beat runs longer than
// its later-night line. A future role wanting custom narrate (or
// sleep) timing adds a branch here.
//
// This switch MUST cover every NightSubPhase value. A missing case
// returns 0, which the caller treats as "no timer to arm" — that would
// silently deadlock the night, so subPhaseDuration logs loudly on 0.
func defaultSubPhaseDuration(e game.NightSubPhaseStarted) time.Duration {
	switch e.Sub {
	case game.NightSubOpening:
		return DefaultOpeningDuration
	case game.NightSubNarrate:
		if e.Role == game.RoleMafia {
			if e.Day == 0 {
				return DefaultMafiaNarrateDay0
			}
			return DefaultMafiaNarrateDayN
		}
		return DefaultNarrateDuration
	case game.NightSubAct:
		return DefaultActionDuration
	case game.NightSubPonder:
		return defaultPonderDuration(e.Role, e.Phantom)
	case game.NightSubSleep:
		return DefaultSleepDuration
	case game.NightSubSettle:
		return DefaultSettleDuration
	}
	return 0
}

// defaultPonderDuration sizes the post-act / phantom-substitute pause
// in three modes:
//   - phantom (no actionable holder — dead, spent, or blocked): uniformly
//     random in [DefaultPhantomPonderMin, DefaultPhantomPonderMax] so the
//     cadence can't be used to deduce WHY the turn was inert.
//   - detective (real): DefaultPonderDetectiveSubmit, so its result
//     modal lands cleanly.
//   - any other real role (submit OR timeout): DefaultPonderRealSubmit.
func defaultPonderDuration(role game.Role, phantom bool) time.Duration {
	if phantom {
		lo, hi := DefaultPhantomPonderMin, DefaultPhantomPonderMax
		span := hi - lo
		if span <= 0 {
			return lo
		}
		return lo + time.Duration(mrand.Int64N(int64(span)+1))
	}
	if role == game.RoleDetective {
		return DefaultPonderDetectiveSubmit
	}
	return DefaultPonderRealSubmit
}

// logger returns c.Logger, or slog.Default() when it's nil (unit tests
// that build a Config without applyDefaults).
func (c *Config) logger() *slog.Logger {
	if c.Logger == nil {
		return slog.Default()
	}
	return c.Logger
}

func (c *Config) applyDefaults() {
	// Zero values for MinPlayers/MaxPlayers/MafiaCount are intentionally
	// left as-is so the engine's CreateGame can apply its own defaults
	// (keeping the "what's the default lobby?" answer in one place).
	//
	// Night sub-phase durations are NOT primed here: they resolve in
	// subPhaseDuration from the Default* constants, so there's no
	// per-field wiring.
	if c.MaxLifetime == 0 {
		c.MaxLifetime = DefaultMaxLifetime
	}
	if c.Logger == nil {
		c.Logger = slog.Default()
	}
}

// subPhaseDuration returns the wall-clock duration of the night
// sub-phase implied by `evt`. Used by stampNightDeadlines and
// armSubPhaseTimer to size the deadline / timer for a freshly emitted
// NightSubPhaseStarted event.
//
// Non-sub-phase events return 0 SILENTLY: the sole caller
// (stampNightDeadlines) probes every event in a batch and skips those
// with no duration, so a PlayerJoined / VoteCast / PhaseChanged landing
// here is the normal, expected case — not an error. The loud log is
// reserved for a NightSubPhaseStarted carrying a Sub we don't size,
// which would silently deadlock the night (the caller arms no timer).
func (c *Config) subPhaseDuration(evt game.Event) time.Duration {
	e, ok := evt.(game.NightSubPhaseStarted)
	if !ok {
		return 0
	}

	dur := defaultSubPhaseDuration(e)
	if dur <= 0 {
		c.logger().Error("room: no duration for night sub-phase",
			"sub", string(e.Sub))
	}
	return dur
}
