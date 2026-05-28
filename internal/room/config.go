package room

import (
	"fmt"
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

	// NightSubPhases controls the per-sub-phase durations during a
	// PhaseNight turn. See NightSubPhaseDurations for the breakdown.
	// nil fields fall back to package defaults; nil struct uses all
	// defaults.
	//
	// Note: narrate and sleep durations are NOT in this struct.
	// Those are owned by the role spec (game.NarrateDuration /
	// game.SleepDuration); the room reads them directly when
	// stamping deadlines. This keeps "how long is mafia's wake-up
	// cue?" answerable in exactly one place (internal/game/rolespec.go).
	NightSubPhases NightSubPhaseDurations

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

// NightSubPhaseDurations supplies wall-clock durations for the six
// night sub-phases. All six durations are owned here in the room
// layer; the engine remains timeless and emits its Night*Started
// events with Deadline=0, which the room stamps before broadcasting
// (see stampNightDeadlines in broadcast.go).
//
// Each field is a function so the duration can vary by the inputs
// that matter for that sub-phase — role and day for narrate/sleep,
// role and phantom/submitted for ponder, and nothing at all for the
// universal-beat fields (opening / action / settle). nil fields fall
// back to package defaults: Default{Opening,Narrate,Action,Ponder,
// Sleep,Settle}.
//
// Adding a new role with custom narration timing: extend
// DefaultNarrate (and, if needed, DefaultSleep) below. The role's
// engine-side registration in internal/game/rolespec.go stays focused
// on faction + night action and doesn't carry duration data.
type NightSubPhaseDurations struct {
	// Opening sizes the one-shot NightSubOpening beat that runs at
	// the start of every PhaseNight before any role's narrate. Today
	// this beat covers the "City, go to sleep." cue plus a pre-wake
	// silence so the room has time to settle. Same duration on every
	// night today; if a future deployment needs day-specific tuning
	// (e.g. a longer first-night preamble), widen the signature then.
	// nil → DefaultOpening.
	Opening func() time.Duration

	// Narrate sizes the per-role "wake up" audio cue (NightSubNarrate).
	// Most roles use a single universal value; mafia overrides Day 0
	// to cover the longer "look around, recognize each other" beat.
	// nil → DefaultNarrate.
	Narrate func(role game.Role, day int) time.Duration

	// Action sizes the actor's decision window (NightSubAct). nil →
	// DefaultAction. We deliberately do NOT take role/day here today
	// — every role gets the same think-time and a single universal
	// number is easier to reason about. If a future role needs a
	// different window, widen this signature.
	Action func() time.Duration

	// Ponder sizes the post-act pause (NightSubPonder). It runs in
	// three modes, all keyed off the args:
	//
	//   - role-act-then-submit (phantom=false, submitted=true): short
	//     fixed beat to absorb the action before sleep cues fire.
	//     Detective gets a slightly longer beat so its result modal
	//     lands cleanly.
	//   - phantom (phantom=true, submitted=false): stand-in for the
	//     missing act window so the cadence can't be used to deduce
	//     a dead role. Default returns a uniformly-random value in
	//     [DefaultPhantomPonderMin, DefaultPhantomPonderMax].
	//   - real timeout (phantom=false, submitted=false): the real
	//     actor never submitted. Default returns the same beat as
	//     the post-submit case so observers can't distinguish
	//     submit from timeout by the audio cadence alone.
	//
	// `submitted` is intentionally on the signature even though the
	// default ignores it: keeping it visible documents that we
	// considered the submit-vs-timeout axis and chose to make them
	// indistinguishable. Overrides may treat them differently if a
	// future deployment wants to (test-only, ideally).
	//
	// nil → DefaultPonder.
	Ponder func(role game.Role, phantom, submitted bool) time.Duration

	// Sleep sizes the per-role "go to sleep" audio cue
	// (NightSubSleep). Every shipped role today uses the same
	// universal value (no per-day variant), but the (role, day)
	// signature matches Narrate so future roles can vary by either
	// axis without re-threading the function type. nil → DefaultSleep.
	Sleep func(role game.Role, day int) time.Duration

	// Settle sizes the post-sleep beat (NightSubSettle) that gives
	// the "go to sleep" cue room to land before the next role's
	// narrate begins. Universal — runs at the end of every role's
	// turn including the last one (just before the night→day
	// transition). nil → DefaultSettle.
	Settle func() time.Duration
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
const DefaultSettleDuration = 2 * time.Second

// DefaultPonderRealSubmit is the post-submit pause for non-detective
// real roles. A small breath between "action recorded" and the
// "go to sleep" cue.
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
const DefaultSleepDuration = 1500 * time.Millisecond

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
const DefaultMaxLifetime = 10 * time.Hour

// DefaultOpening is the default opening-sub-phase duration.
func DefaultOpening() time.Duration { return DefaultOpeningDuration }

// DefaultNarrate is the default narrate-sub-phase duration, keyed by
// role and day. The mafia branch covers the Day-0 "look around"
// variant; everyone else (and mafia on subsequent days) falls through
// to the universal value.
//
// This is the registry that grows when a new role wants custom
// narration timing. Keep entries in stable role order to make diffs
// easy to read.
func DefaultNarrate(role game.Role, day int) time.Duration {
	switch role {
	case game.RoleMafia:
		if day == 0 {
			return DefaultMafiaNarrateDay0
		}
		return DefaultMafiaNarrateDayN
	}
	return DefaultNarrateDuration
}

// DefaultSleep is the default sleep-sub-phase duration. Every shipped
// role uses the universal value today. A future role that wants a
// custom sleep cue (e.g. "Bodyguard, go to sleep. You'll get a new
// charge in 3 nights.") would add a case here.
func DefaultSleep(_ game.Role, _ int) time.Duration {
	return DefaultSleepDuration
}

// DefaultAction is the default action-sub-phase duration.
func DefaultAction() time.Duration { return DefaultActionDuration }

// DefaultPonder returns the default ponder-sub-phase duration. See
// NightSubPhaseDurations.Ponder for the three modes:
//
//   - phantom (no living holder): random in [DefaultPhantomPonderMin,
//     DefaultPhantomPonderMax]. Mirrors how a deciding actor varies.
//   - real role (submit OR timeout): DefaultPonderRealSubmit, except
//     RoleDetective which gets DefaultPonderDetectiveSubmit (so its
//     result modal lands cleanly).
//
// The `submitted` flag is intentionally unused by the default: we
// want submit and timeout to be indistinguishable by audio cadence
// alone. Future overrides may treat the two modes differently — the
// parameter is kept on the signature for that flexibility.
func DefaultPonder(role game.Role, phantom, _ bool) time.Duration {
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

// DefaultSettle is the default settle-sub-phase duration.
func DefaultSettle() time.Duration { return DefaultSettleDuration }

func (c *Config) applyDefaults() {
	// Zero values for MinPlayers/MaxPlayers/MafiaCount are intentionally
	// left as-is so the engine's CreateGame can apply its own defaults
	// (keeping the "what's the default lobby?" answer in one place).
	if c.NightSubPhases.Opening == nil {
		c.NightSubPhases.Opening = DefaultOpening
	}
	if c.NightSubPhases.Narrate == nil {
		c.NightSubPhases.Narrate = DefaultNarrate
	}
	if c.NightSubPhases.Action == nil {
		c.NightSubPhases.Action = DefaultAction
	}
	if c.NightSubPhases.Ponder == nil {
		c.NightSubPhases.Ponder = DefaultPonder
	}
	if c.NightSubPhases.Sleep == nil {
		c.NightSubPhases.Sleep = DefaultSleep
	}
	if c.NightSubPhases.Settle == nil {
		c.NightSubPhases.Settle = DefaultSettle
	}
	if c.MaxLifetime == 0 {
		c.MaxLifetime = DefaultMaxLifetime
	}
	if c.Logger == nil {
		c.Logger = slog.Default()
	}
}

// subPhaseDuration returns the wall-clock duration of the active night
// sub-phase implied by `evt`. Used by stampNightDeadlines and
// armSubPhaseTimer to size the deadline / timer for a freshly emitted
// *Started event.
//
// Every piece of context this function needs (role, day, phantom-vs-
// real) is carried on the event itself — those fields are stamped by
// the engine at emit time and survive to late joiners via the event
// log. The one piece that ISN'T on the event is `submitted`: the
// engine emits NightPonderStarted whether the actor submitted or
// timed out, and the room layer needs to know which to size the
// ponder beat. We thread it in as a separate argument rather than
// adding it to the event payload because it's a room-config concern
// (today's defaults treat both modes identically; an override might
// not).
//
// All six durations resolve through c.NightSubPhases, which is
// populated by applyDefaults at room construction. No engine
// dependency for timing — the engine is timeless.
//
// The switch below MUST cover every Night*Started event the engine
// emits. If a new sub-phase event is added without a matching case
// here, the timer won't arm and the night will silently deadlock —
// the default branch logs at error level to make that failure mode
// loud during dev.
func (c *Config) subPhaseDuration(evt game.Event, submitted bool) time.Duration {
	switch e := evt.(type) {
	case game.NightOpeningStarted:
		return c.NightSubPhases.Opening()
	case game.NightNarrationStarted:
		return c.NightSubPhases.Narrate(e.Role, e.Day)
	case game.NightActionStarted:
		return c.NightSubPhases.Action()
	case game.NightPonderStarted:
		return c.NightSubPhases.Ponder(e.Role, e.Phantom, submitted)
	case game.NightSleepStarted:
		return c.NightSubPhases.Sleep(e.Role, e.Day)
	case game.NightSettleStarted:
		return c.NightSubPhases.Settle()
	default:
		// Reached only when a new Night*Started event type ships
		// without being plumbed in here. The caller treats 0 as
		// "no timer to arm", which would deadlock the night — log
		// loudly so the gap surfaces on the first run after the
		// engine change. Logger may be nil in unit tests that
		// skip applyDefaults; slog.Default() is the safe fallback.
		logger := c.Logger
		if logger == nil {
			logger = slog.Default()
		}
		logger.Error("room: subPhaseDuration called with unhandled event type",
			"event_type", fmt.Sprintf("%T", evt))
		return 0
	}
}
