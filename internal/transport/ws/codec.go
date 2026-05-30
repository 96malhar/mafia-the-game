package ws

import (
	"encoding/json"
	"fmt"

	"github.com/96malhar/mafia-the-game/internal/game"
	"github.com/96malhar/mafia-the-game/internal/room"
	"github.com/96malhar/mafia-the-game/internal/wire"
)

// encodeEvent translates an engine game.Event into the wire-shape
// eventEnvelope. The shape of each `data` block is defined inline here
// to insulate the wire format from refactors of the Go event types.
//
// Unknown event types return an error rather than panicking: we'd
// rather log + drop than crash on a future engine change.
func encodeEvent(e game.Event) (eventEnvelope, error) {
	type kv map[string]any
	var (
		tag  string
		data any
	)
	switch v := e.(type) {
	case game.GameCreated:
		tag = wire.EventGameCreated
		data = kv{
			"gameId":     string(v.GameID),
			"minPlayers": v.MinPlayers,
			"maxPlayers": v.MaxPlayers,
			"mafiaCount": v.MafiaCount,
			"seed":       v.Seed,
		}
	case game.MafiaCountChanged:
		tag = wire.EventMafiaCountChanged
		data = kv{"from": v.From, "to": v.To}
	case game.PlayerJoined:
		tag = wire.EventPlayerJoined
		data = kv{"playerId": string(v.PlayerID), "name": v.Name}
	case game.HostChanged:
		tag = wire.EventHostChanged
		data = kv{"playerId": string(v.PlayerID)}
	case game.GameStarted:
		tag = wire.EventGameStarted
		data = kv{}
	case game.RoleAssigned:
		tag = wire.EventRoleAssigned
		data = kv{"playerId": string(v.PlayerID), "role": string(v.Role)}
	case game.PhaseChanged:
		tag = wire.EventPhaseChanged
		data = kv{"from": string(v.From), "to": string(v.To), "day": v.Day}
	case game.NightSubPhaseStarted:
		// One Go type now, but each sub-phase keeps its own stable wire
		// tag and data shape so existing clients are unaffected by the
		// engine-side collapse: opening carries no role; narrate and
		// ponder carry the phantom flag; the rest carry role/day/deadline.
		switch v.Sub {
		case game.NightSubOpening:
			tag = wire.EventNightOpeningStarted
			data = kv{"day": v.Day, "deadline": v.Deadline}
		case game.NightSubNarrate:
			tag = wire.EventNightNarrationStarted
			data = kv{"role": string(v.Role), "day": v.Day, "deadline": v.Deadline, "phantom": v.Phantom}
		case game.NightSubAct:
			tag = wire.EventNightActionStarted
			data = kv{"role": string(v.Role), "day": v.Day, "deadline": v.Deadline}
		case game.NightSubPonder:
			tag = wire.EventNightPonderStarted
			data = kv{"role": string(v.Role), "day": v.Day, "deadline": v.Deadline, "phantom": v.Phantom}
		case game.NightSubSleep:
			tag = wire.EventNightSleepStarted
			data = kv{"role": string(v.Role), "day": v.Day, "deadline": v.Deadline}
		case game.NightSubSettle:
			tag = wire.EventNightSettleStarted
			data = kv{"role": string(v.Role), "day": v.Day, "deadline": v.Deadline}
		default:
			return eventEnvelope{}, fmt.Errorf("ws: unknown night sub-phase %q", v.Sub)
		}
	case game.NightActionRecorded:
		tag = wire.EventNightActionRecorded
		data = kv{"actor": string(v.Actor), "target": string(v.Target), "faction": string(v.Faction)}
	case game.PlayerKilled:
		tag = wire.EventPlayerKilled
		data = kv{"playerId": string(v.PlayerID)}
	case game.PlayerSaved:
		tag = wire.EventPlayerSaved
		data = kv{"playerId": string(v.PlayerID), "doctor": string(v.Doctor)}
	case game.DetectiveResult:
		tag = wire.EventDetectiveResult
		data = kv{"detective": string(v.Detective), "target": string(v.Target), "isMafia": v.IsMafia}
	case game.VoteCast:
		tag = wire.EventVoteCast
		data = kv{"voter": string(v.Voter), "target": string(v.Target)}
	case game.VoteChanged:
		tag = wire.EventVoteChanged
		data = kv{"voter": string(v.Voter), "from": string(v.From), "to": string(v.To)}
	case game.VoteRetracted:
		tag = wire.EventVoteRetracted
		data = kv{"voter": string(v.Voter), "was": string(v.Was)}
	case game.VotesRevealed:
		tag = wire.EventVotesRevealed
		data = kv{"day": v.Day, "tally": voteMapToStrings(v.Tally)}
	case game.VoteCleared:
		tag = wire.EventVoteCleared
		data = kv{"day": v.Day}
	case game.PlayerLynched:
		tag = wire.EventPlayerLynched
		data = kv{"playerId": string(v.PlayerID)}
	case game.NoLynch:
		tag = wire.EventNoLynch
		data = kv{"day": v.Day}
	case game.GameEnded:
		tag = wire.EventGameEnded
		data = kv{"winner": string(v.Winner), "finalRoles": rolesMapToStrings(v.FinalRoles)}
	default:
		return eventEnvelope{}, fmt.Errorf("ws: unknown event type %T", e)
	}

	raw, err := json.Marshal(data)
	if err != nil {
		return eventEnvelope{}, fmt.Errorf("ws: marshal event %s: %w", tag, err)
	}
	return eventEnvelope{Type: tag, Data: raw}, nil
}

// encodeOutbound translates a room.Outbound value into a wire-format
// JSON envelope. Returns the encoded bytes plus a boolean indicating
// whether the value was a known shape; unknown shapes return ok=false
// so the caller can log + drop without sending malformed JSON.
func encodeOutbound(msg room.Outbound) ([]byte, bool, error) {
	switch m := msg.(type) {
	case room.OutJoined:
		evs, errs := encodeEventsBatch(m.Events)
		if len(errs) > 0 {
			return nil, true, errs[0]
		}
		raw, err := marshalEnvelope(string(serverMsgJoined), serverJoinedData{
			PlayerID: string(m.PlayerID),
			Name:     m.Name,
			Secret:   m.Secret,
			RoomCode: m.RoomCode,
			IsHost:   m.IsHost,
			Events:   evs,
		})
		return raw, true, err

	case room.OutRejoined:
		evs, errs := encodeEventsBatch(m.Events)
		if len(errs) > 0 {
			return nil, true, errs[0]
		}
		raw, err := marshalEnvelope(string(serverMsgRejoined), serverRejoinedData{
			PlayerID: string(m.PlayerID),
			Name:     m.Name,
			RoomCode: m.RoomCode,
			IsHost:   m.IsHost,
			Events:   evs,
		})
		return raw, true, err

	case room.OutEvent:
		envev, err := encodeEvent(m.Event)
		if err != nil {
			return nil, true, err
		}
		raw, err := marshalEnvelope(string(serverMsgEvent), serverEventData{Event: envev})
		return raw, true, err

	case room.OutError:
		// Explicit cast at the wire boundary: serverErrorData.Code is
		// `string` (the JSON shape), m.Code is wire.ErrorCode (the
		// in-process typed value). Keeping the cast here documents
		// the boundary and avoids leaking the typed alias into the
		// JSON struct, which would force every test that decodes an
		// error frame to import internal/wire.
		raw, err := marshalEnvelope(string(serverMsgError), serverErrorData{
			Code:    string(m.Code),
			Message: m.Message,
		})
		return raw, true, err

	default:
		return nil, false, fmt.Errorf("ws: unknown outbound type %T", msg)
	}
}

func marshalEnvelope(tag string, data any) ([]byte, error) {
	raw, err := json.Marshal(data)
	if err != nil {
		return nil, fmt.Errorf("ws: marshal %s: %w", tag, err)
	}
	return json.Marshal(envelope{Type: tag, Data: raw})
}

// --- Inbound decoding ----------------------------------------------------

// decodeClientMessage parses a raw JSON frame from the client into a
// well-typed clientMsg* value plus its tag, returning errBadEnvelope on
// any shape mismatch. The handler then builds the right room.inbound.
//
// Unknown tags return errBadEnvelope so the caller can send a typed
// error frame back to the client without disconnecting.
func decodeClientMessage(raw []byte) (clientMsgType, any, error) {
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return "", nil, badEnvelopef("invalid JSON: %v", err)
	}
	if env.Type == "" {
		return "", nil, badEnvelopef("missing type")
	}

	switch clientMsgType(env.Type) {
	case clientMsgJoin:
		var d clientJoinData
		if err := unmarshalData(env.Data, &d); err != nil {
			return "", nil, err
		}
		return clientMsgJoin, d, nil

	case clientMsgNightAction:
		var d clientNightActionData
		if err := unmarshalData(env.Data, &d); err != nil {
			return "", nil, err
		}
		return clientMsgNightAction, d, nil

	case clientMsgVote:
		var d clientVoteData
		if err := unmarshalData(env.Data, &d); err != nil {
			return "", nil, err
		}
		return clientMsgVote, d, nil

	case clientMsgSetMafia:
		var d clientSetMafiaData
		if err := unmarshalData(env.Data, &d); err != nil {
			return "", nil, err
		}
		return clientMsgSetMafia, d, nil

	case clientMsgStartGame:
		return clientMsgStartGame, struct{}{}, nil

	case clientMsgBeginNight:
		return clientMsgBeginNight, struct{}{}, nil

	case clientMsgOpenVoting:
		return clientMsgOpenVoting, struct{}{}, nil

	case clientMsgRevealVotes:
		return clientMsgRevealVotes, struct{}{}, nil

	case clientMsgClearVotes:
		return clientMsgClearVotes, struct{}{}, nil

	case clientMsgFinalizeVotes:
		return clientMsgFinalizeVotes, struct{}{}, nil

	default:
		return "", nil, badEnvelopef("unknown type %q", env.Type)
	}
}

// unmarshalData is a small helper that treats a missing/null `data`
// field as an empty object — clients often omit it when there's no
// payload.
func unmarshalData(raw json.RawMessage, into any) error {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	if err := json.Unmarshal(raw, into); err != nil {
		return badEnvelopef("invalid data: %v", err)
	}
	return nil
}

// commandFromClient turns a decoded client message into a game.Command
// suitable for room.SubmitCommand. Returns (nil, false) for messages
// that aren't commands at the engine level (e.g. clientMsgJoin, which
// the handler treats specially).
//
// The room rewrites identity fields server-side; we leave them blank.
func commandFromClient(tag clientMsgType, data any) (game.Command, bool) {
	switch tag {
	case clientMsgNightAction:
		d := data.(clientNightActionData)
		return game.NightAction{Target: game.PlayerID(d.Target)}, true
	case clientMsgVote:
		d := data.(clientVoteData)
		return game.DayVote{Target: game.PlayerID(d.Target)}, true
	case clientMsgSetMafia:
		d := data.(clientSetMafiaData)
		return game.SetMafiaCount{Count: d.Count}, true
	case clientMsgStartGame:
		return game.StartGame{}, true
	case clientMsgBeginNight:
		return game.BeginNight{}, true
	case clientMsgOpenVoting:
		return game.OpenVoting{}, true
	case clientMsgRevealVotes:
		return game.RevealVotes{}, true
	case clientMsgClearVotes:
		return game.ClearVotes{}, true
	case clientMsgFinalizeVotes:
		return game.FinalizeVotes{}, true
	default:
		return nil, false
	}
}

// --- Small utility shims --------------------------------------------------

func rolesMapToStrings(m map[game.PlayerID]game.Role) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[string(k)] = string(v)
	}
	return out
}

// voteMapToStrings flattens a voter→target PlayerID map into a
// string→string map for the wire (used by the VotesRevealed event). A
// nil map encodes as an empty object so the client always gets a {}.
func voteMapToStrings(m map[game.PlayerID]game.PlayerID) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[string(k)] = string(v)
	}
	return out
}

// encodeEventsBatch is used by serverRejoinedData payload assembly.
// Errors are logged and the offending event is omitted, not fatal.
func encodeEventsBatch(events []game.Event) ([]eventEnvelope, []error) {
	out := make([]eventEnvelope, 0, len(events))
	var errs []error
	for _, e := range events {
		env, err := encodeEvent(e)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		out = append(out, env)
	}
	return out, errs
}
