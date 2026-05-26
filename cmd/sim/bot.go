package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/coder/websocket"
)

// Bot is one simulated player. It owns its WebSocket connection and a
// small local model of the world derived from the events it has seen.
// Bots run one per goroutine, reacting to incoming events; the main
// goroutine separately drives phase advancement (see main.go).
type Bot struct {
	name string
	log  *slog.Logger

	// playerID is empty until we receive the "joined" ack.
	playerID string

	// role is empty until we receive our private "roleAssigned" event.
	// Other bots' roles remain unknown to us forever (except after
	// gameEnded, which includes the full roster).
	role string

	// alivePlayers tracks the set of living player IDs, updated from
	// playerJoined / playerKilled / playerLynched events. Used by
	// every strategy to pick a valid target.
	alivePlayers map[string]struct{}

	// detectiveKnown is the detective's running list of investigation
	// results: target -> isMafia. Updated as detectiveResult events
	// arrive (only the detective will ever see any).
	detectiveKnown map[string]bool

	// phase is the current game phase, updated on phaseChanged.
	phase string

	// currentNightRole is the role whose turn it is during PhaseNight,
	// updated on nightTurnStarted. Empty between turns and outside
	// Night. The bot uses this to gate its night action: only act
	// when its own role matches.
	currentNightRole string

	// dayLynchResolved mirrors the engine's same-named flag. Set when
	// a PlayerLynched arrives during PhaseDayVote; cleared on entry
	// into PhaseNight. The host driver uses it to choose between
	// OpenVoting (false) and BeginNight (true) when sitting in
	// PhaseDayDiscussion.
	dayLynchResolved bool

	conn *websocket.Conn
}

// NewBot constructs a bot that will join with the given display name.
func NewBot(name string, logger *slog.Logger) *Bot {
	return &Bot{
		name:           name,
		log:            logger.With("bot", name),
		alivePlayers:   make(map[string]struct{}),
		detectiveKnown: make(map[string]bool),
		phase:          phaseLobby,
	}
}

// Connect dials the WebSocket endpoint for the given room code and
// blocks until the connection is established. The bot is NOT yet
// joined — call Join after.
func (b *Bot) Connect(ctx context.Context, wsURL string) error {
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}
	b.conn = conn
	return nil
}

// Close shuts down the WebSocket connection.
func (b *Bot) Close() {
	if b.conn != nil {
		_ = b.conn.CloseNow()
	}
}

// Join sends the initial {type:"join"} frame. The "joined" ack is
// consumed by Run.
func (b *Bot) Join(ctx context.Context) error {
	return b.send(ctx, "join", clientJoin{Name: b.name})
}

// Run is the bot's main loop. It reads messages, updates state, and
// dispatches to the strategy module on every state change.
//
// Returns when the context is done, the server closes the connection,
// or a gameEnded event arrives.
func (b *Bot) Run(ctx context.Context, ended chan<- evGameEnded) error {
	for {
		mt, raw, err := b.conn.Read(ctx)
		if err != nil {
			// Normal close, ctx cancel, etc. — let main decide.
			if errors.Is(err, context.Canceled) || ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("read: %w", err)
		}
		if mt != websocket.MessageText {
			continue
		}

		var env envelope
		if err := json.Unmarshal(raw, &env); err != nil {
			b.log.Warn("bad envelope", "err", err)
			continue
		}

		switch env.Type {
		case msgJoined:
			var d serverJoined
			_ = json.Unmarshal(env.Data, &d)
			b.playerID = d.PlayerID
			b.log = b.log.With("pid", d.PlayerID)
			b.log.Info("joined", "host", d.IsHost)

		case msgError:
			var d serverError
			_ = json.Unmarshal(env.Data, &d)
			// Errors are typically benign in a sim (e.g. trying to act
			// after we're dead). Log at debug.
			b.log.Debug("server error", "code", d.Code, "msg", d.Message)

		case msgEvent:
			var ev serverEvent
			if err := json.Unmarshal(env.Data, &ev); err != nil {
				b.log.Warn("bad event envelope", "err", err)
				continue
			}
			if done, end := b.handleEvent(ctx, ev.Event); done {
				select {
				case ended <- end:
				default:
				}
				return nil
			}
		}
	}
}

// handleEvent updates the bot's local model and triggers any
// strategy-driven actions. Returns (true, end) once gameEnded arrives.
func (b *Bot) handleEvent(ctx context.Context, ev eventEnvelope) (bool, evGameEnded) {
	switch ev.Type {
	case evTagPlayerJoined:
		var d evPlayerJoined
		_ = json.Unmarshal(ev.Data, &d)
		b.alivePlayers[d.PlayerID] = struct{}{}

	case evTagRoleAssigned:
		var d evRoleAssigned
		_ = json.Unmarshal(ev.Data, &d)
		// roleAssigned is private — we should only see it for ourselves.
		if d.PlayerID == b.playerID {
			b.role = d.Role
			b.log.Info("dealt", "role", d.Role)
		}

	case evTagPhaseChanged:
		var d evPhaseChanged
		_ = json.Unmarshal(ev.Data, &d)
		b.phase = d.To
		b.currentNightRole = "" // cleared on every phase entry
		// dayLynchResolved is set by PlayerLynched (below) and stays
		// true until we re-enter Night.
		if d.To == phaseNight {
			b.dayLynchResolved = false
		}
		b.log.Debug("phase", "to", d.To, "day", d.Day)
		// On day phase entry, ask the strategy for an action. Night
		// actions are gated on nightTurnStarted below.
		if d.To != phaseNight {
			b.maybeAct(ctx)
		}

	case evTagNightTurnStarted:
		var d evNightTurnStarted
		_ = json.Unmarshal(ev.Data, &d)
		b.currentNightRole = d.Role
		b.log.Debug("night turn started", "role", d.Role, "phantom", d.Phantom)
		// Only the matching role acts; others sit out and the engine /
		// per-turn timer skips them. Phantom turns (no living holder)
		// accept no action — the room times them out — so don't try.
		if !d.Phantom && d.Role == b.role {
			b.maybeAct(ctx)
		}

	case evTagPlayerKilled:
		var d evPlayerKilled
		_ = json.Unmarshal(ev.Data, &d)
		delete(b.alivePlayers, d.PlayerID)
		if d.PlayerID == b.playerID {
			b.log.Info("killed at night")
		}

	case evTagPlayerLynched:
		var d evPlayerLynched
		_ = json.Unmarshal(ev.Data, &d)
		delete(b.alivePlayers, d.PlayerID)
		b.dayLynchResolved = true
		if d.PlayerID == b.playerID {
			b.log.Info("lynched")
		}

	case evTagDetectiveResult:
		var d evDetectiveResult
		_ = json.Unmarshal(ev.Data, &d)
		if d.Detective == b.playerID {
			b.detectiveKnown[d.Target] = d.IsMafia
			b.log.Info("detective result", "target", d.Target, "mafia", d.IsMafia)
		}

	case evTagGameEnded:
		var d evGameEnded
		_ = json.Unmarshal(ev.Data, &d)
		return true, d
	}
	return false, evGameEnded{}
}

// maybeAct asks the strategy what to do given the current phase and
// the bot's role. If the strategy returns a non-empty command, it's
// sent immediately.
func (b *Bot) maybeAct(ctx context.Context) {
	if b.role == "" {
		return // not dealt yet (still in lobby)
	}
	if _, alive := b.alivePlayers[b.playerID]; !alive && b.phase != phaseLobby {
		return // dead — don't try to act
	}

	cmd, target := decideAction(b.role, b.phase, b.playerID, b.alivePlayers, b.detectiveKnown)
	switch cmd {
	case "":
		// No action this phase for this role.
	case "nightAction":
		b.log.Info("night action", "target", target)
		_ = b.send(ctx, "nightAction", clientNightAction{Target: target})
	case "vote":
		b.log.Info("vote", "target", target)
		_ = b.send(ctx, "vote", clientVote{Target: target})
	}
}

// Phase returns the bot's current view of the game phase. The bot
// updates this on every PhaseChanged event it sees, so it lags the
// server by one frame plus network — fine for the host driver, which
// uses this only to decide which advance command to send next.
func (b *Bot) Phase() string { return b.phase }

// DayLynchResolved returns true if the most recent PhaseChanged into
// DayDiscussion was after a lynch (i.e. the day's vote has already
// been finalized). The bot tracks this via PlayerLynched events: any
// PlayerLynched landing during PhaseDayVote, followed by a transition
// to DayDiscussion, sets the flag; a transition into Night clears it.
func (b *Bot) DayLynchResolved() bool { return b.dayLynchResolved }

// send marshals a typed message and writes it as a text frame.
func (b *Bot) send(ctx context.Context, kind string, payload any) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	frame, err := json.Marshal(envelope{Type: kind, Data: raw})
	if err != nil {
		return err
	}
	return b.conn.Write(ctx, websocket.MessageText, frame)
}
