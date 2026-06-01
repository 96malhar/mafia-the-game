package ws

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/96malhar/mafia-the-game/internal/game"
	"github.com/96malhar/mafia-the-game/internal/room"
	"github.com/96malhar/mafia-the-game/internal/wire"
)

// --- Inbound (client -> server) decoding ---------------------------------

func TestDecodeClientMessage_Variants(t *testing.T) {
	cases := []struct {
		name     string
		raw      string
		wantTag  clientMsgType
		wantData any
	}{
		{
			name:     "join with name",
			raw:      `{"type":"join","data":{"name":"Alice"}}`,
			wantTag:  clientMsgJoin,
			wantData: clientJoinData{Name: "Alice"},
		},
		{
			name:     "nightAction with target",
			raw:      `{"type":"nightAction","data":{"target":"p2"}}`,
			wantTag:  clientMsgNightAction,
			wantData: clientNightActionData{Target: "p2"},
		},
		{
			name:     "vote with target",
			raw:      `{"type":"vote","data":{"target":"p3"}}`,
			wantTag:  clientMsgVote,
			wantData: clientVoteData{Target: "p3"},
		},
		{
			name:     "setMafia with count",
			raw:      `{"type":"setMafia","data":{"count":3}}`,
			wantTag:  clientMsgSetMafia,
			wantData: clientSetMafiaData{Count: 3},
		},
		{
			name:     "setConsort enabled",
			raw:      `{"type":"setConsort","data":{"enabled":true}}`,
			wantTag:  clientMsgSetConsort,
			wantData: clientSetConsortData{Enabled: true},
		},
		{
			name:     "setVigilante enabled",
			raw:      `{"type":"setVigilante","data":{"enabled":true}}`,
			wantTag:  clientMsgSetVigilante,
			wantData: clientSetVigilanteData{Enabled: true},
		},
		{
			name:    "startGame no data",
			raw:     `{"type":"startGame"}`,
			wantTag: clientMsgStartGame,
		},
		{
			name:    "beginNight no data",
			raw:     `{"type":"beginNight"}`,
			wantTag: clientMsgBeginNight,
		},
		{
			name:    "openVoting null data",
			raw:     `{"type":"openVoting","data":null}`,
			wantTag: clientMsgOpenVoting,
		},
		{
			name:    "revealVotes no data",
			raw:     `{"type":"revealVotes"}`,
			wantTag: clientMsgRevealVotes,
		},
		{
			name:    "clearVotes no data",
			raw:     `{"type":"clearVotes"}`,
			wantTag: clientMsgClearVotes,
		},
		{
			name:    "finalizeVotes no data",
			raw:     `{"type":"finalizeVotes"}`,
			wantTag: clientMsgFinalizeVotes,
		},
		{
			name:    "nightPass no data",
			raw:     `{"type":"nightPass"}`,
			wantTag: clientMsgNightPass,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tag, payload, err := decodeClientMessage([]byte(tc.raw))
			require.NoError(t, err)
			require.Equal(t, tc.wantTag, tag)
			if tc.wantData != nil {
				require.Equal(t, tc.wantData, payload)
			}
		})
	}
}

func TestDecodeClientMessage_Rejects(t *testing.T) {
	cases := []struct {
		name string
		raw  string
	}{
		{"empty", ``},
		{"not json", `garbage`},
		{"missing type", `{"data":{}}`},
		{"unknown type", `{"type":"deleteUniverse"}`},
		{"bad data shape", `{"type":"nightAction","data":"not an object"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := decodeClientMessage([]byte(tc.raw))
			require.Error(t, err)
		})
	}
}

// --- Outbound (server -> client) encoding --------------------------------

func TestEncodeOutbound_Joined(t *testing.T) {
	// A late joiner's ack carries the prior events so the new client
	// can reconstruct existing roster state.
	priorEvents := []game.Event{
		game.PlayerJoined{PlayerID: "p1", Name: "Alice"},
	}
	raw, ok, err := encodeOutbound(room.OutJoined{
		PlayerID: "p2", Name: "Bob", Secret: "shh", RoomCode: "ABCD", IsHost: false,
		Events: priorEvents,
	})
	require.NoError(t, err)
	require.True(t, ok)

	got := mustUnmarshalEnvelope(t, raw)
	require.Equal(t, "joined", got.Type)

	var data serverJoinedData
	require.NoError(t, json.Unmarshal(got.Data, &data))
	require.Equal(t, "p2", data.PlayerID)
	require.Equal(t, "Bob", data.Name)
	require.Equal(t, "shh", data.Secret)
	require.Equal(t, "ABCD", data.RoomCode)
	require.False(t, data.IsHost)
	require.Len(t, data.Events, 1)
	require.Equal(t, wire.EventPlayerJoined, data.Events[0].Type)
}

// The very first joiner gets no prior events; this guards the
// nil-events path through encodeOutbound so it doesn't emit
// `"events": null` or panic.
func TestEncodeOutbound_Joined_NoPriorEvents(t *testing.T) {
	raw, ok, err := encodeOutbound(room.OutJoined{
		PlayerID: "p1", Name: "Alice", Secret: "shh", RoomCode: "ABCD", IsHost: true,
	})
	require.NoError(t, err)
	require.True(t, ok)

	var data serverJoinedData
	got := mustUnmarshalEnvelope(t, raw)
	require.NoError(t, json.Unmarshal(got.Data, &data))
	require.Empty(t, data.Events)
}

func TestEncodeOutbound_Rejoined_IncludesEvents(t *testing.T) {
	events := []game.Event{
		game.PlayerJoined{PlayerID: "p1", Name: "Alice"},
		game.GameStarted{},
		game.PhaseChanged{From: game.PhaseLobby, To: game.PhaseNight, Day: 0},
	}
	raw, ok, err := encodeOutbound(room.OutRejoined{
		PlayerID: "p1", Name: "Alice", RoomCode: "ABCD", IsHost: true, Events: events,
	})
	require.NoError(t, err)
	require.True(t, ok)

	got := mustUnmarshalEnvelope(t, raw)
	require.Equal(t, "rejoined", got.Type)

	var data serverRejoinedData
	require.NoError(t, json.Unmarshal(got.Data, &data))
	require.Equal(t, "p1", data.PlayerID)
	require.Equal(t, "Alice", data.Name)
	require.Len(t, data.Events, 3)
	require.Equal(t, wire.EventPlayerJoined, data.Events[0].Type)
	require.Equal(t, wire.EventGameStarted, data.Events[1].Type)
	require.Equal(t, wire.EventPhaseChanged, data.Events[2].Type)
}

func TestEncodeOutbound_AllEventTypes(t *testing.T) {
	// One representative event of each engine kind. If a new event is
	// added to the engine, this test fails until codec.go is updated —
	// the desired forcing function.
	all := []game.Event{
		game.GameCreated{GameID: "g", MinPlayers: 5, MaxPlayers: 20, MafiaCount: 1, Seed: 1},
		game.MafiaCountChanged{From: 1, To: 2},
		game.PlayerJoined{PlayerID: "p1", Name: "Alice"},
		game.GameStarted{},
		game.RoleAssigned{PlayerID: "p1", Role: game.RoleMafia},
		game.MafiaRosterRevealed{Members: []game.PlayerID{"p1", "p2"}},
		game.ConsortChanged{Enabled: true},
		game.VigilanteChanged{Enabled: true},
		game.Blocked{PlayerID: "p3"},
		game.ConsortPromoted{PlayerID: "p4"},
		game.PhaseChanged{From: game.PhaseLobby, To: game.PhaseNight},
		// One NightSubPhaseStarted per Sub so every night wire tag is
		// still exercised through the single event type.
		game.NightSubPhaseStarted{Sub: game.NightSubOpening, Day: 0, Deadline: 1700000000000},
		game.NightSubPhaseStarted{Sub: game.NightSubNarrate, Role: game.RoleMafia, Day: 0, Deadline: 1700000000000, Phantom: true},
		game.NightSubPhaseStarted{Sub: game.NightSubAct, Role: game.RoleMafia, Day: 0, Deadline: 1700000000000},
		game.NightSubPhaseStarted{Sub: game.NightSubPonder, Role: game.RoleMafia, Day: 0, Deadline: 1700000000000, Phantom: false},
		game.NightSubPhaseStarted{Sub: game.NightSubSleep, Role: game.RoleMafia, Day: 0, Deadline: 1700000000000},
		game.NightSubPhaseStarted{Sub: game.NightSubSettle, Role: game.RoleMafia, Day: 0, Deadline: 1700000000000},
		game.NightActionRecorded{Actor: "p1", Target: "p2", Faction: game.FactionMafia},
		game.PlayerKilled{PlayerID: "p2"},
		game.PlayerSaved{PlayerID: "p2", Doctor: "p3"},
		game.DetectiveResult{Detective: "p4", Target: "p1", IsMafia: true},
		game.VoteCast{Voter: "p1", Target: "p2"},
		game.VoteChanged{Voter: "p1", From: "p2", To: "p3"},
		game.VoteRetracted{Voter: "p1", Was: "p2"},
		game.VotesRevealed{Day: 1, Tally: map[game.PlayerID]game.PlayerID{"p1": "p2"}},
		game.VoteCleared{Day: 1},
		game.PlayerLynched{PlayerID: "p2"},
		game.NoLynch{Day: 1},
		game.GameEnded{Winner: game.FactionTown, FinalRoles: map[game.PlayerID]game.Role{"p1": game.RoleMafia}},
	}
	for _, ev := range all {
		t.Run(eventTypeName(ev), func(t *testing.T) {
			raw, ok, err := encodeOutbound(room.OutEvent{Event: ev})
			require.NoError(t, err)
			require.True(t, ok)
			env := mustUnmarshalEnvelope(t, raw)
			require.Equal(t, "event", env.Type)
		})
	}
}

func TestEncodeOutbound_VotesRevealed(t *testing.T) {
	// The reveal event must carry the full voter→target tally as a
	// string→string object under "tally", plus the day.
	ev := game.VotesRevealed{
		Day:   2,
		Tally: map[game.PlayerID]game.PlayerID{"p1": "p3", "p2": "p3"},
	}
	raw, ok, err := encodeOutbound(room.OutEvent{Event: ev})
	require.NoError(t, err)
	require.True(t, ok)

	env := mustUnmarshalEnvelope(t, raw)
	require.Equal(t, "event", env.Type)

	var ed serverEventData
	require.NoError(t, json.Unmarshal(env.Data, &ed))
	require.Equal(t, "votesRevealed", ed.Event.Type)

	var data struct {
		Day   int               `json:"day"`
		Tally map[string]string `json:"tally"`
	}
	require.NoError(t, json.Unmarshal(ed.Event.Data, &data))
	require.Equal(t, 2, data.Day)
	require.Equal(t, map[string]string{"p1": "p3", "p2": "p3"}, data.Tally)
}

func TestEncodeOutbound_Error(t *testing.T) {
	// Producer side uses the typed wire.ErrorCode; wire-format side
	// (serverErrorData.Code) is a plain JSON string. The codec is the
	// only place that bridges the two, so we verify both directions
	// here: typed code in, raw string out.
	raw, ok, err := encodeOutbound(room.OutError{Code: wire.ErrCodeBadMessage, Message: "nope"})
	require.NoError(t, err)
	require.True(t, ok)

	got := mustUnmarshalEnvelope(t, raw)
	require.Equal(t, "error", got.Type)

	var data serverErrorData
	require.NoError(t, json.Unmarshal(got.Data, &data))
	require.Equal(t, string(wire.ErrCodeBadMessage), data.Code)
	require.Equal(t, "nope", data.Message)
}

// --- commandFromClient ----------------------------------------------------

func TestCommandFromClient(t *testing.T) {
	cmd, ok := commandFromClient(clientMsgNightAction, clientNightActionData{Target: "p2"})
	require.True(t, ok)
	require.Equal(t, game.NightAction{Target: "p2"}, cmd)

	// NightPass is payload-less; Actor is filled in server-side.
	cmd, ok = commandFromClient(clientMsgNightPass, struct{}{})
	require.True(t, ok)
	require.Equal(t, game.NightPass{}, cmd)

	cmd, ok = commandFromClient(clientMsgVote, clientVoteData{Target: ""})
	require.True(t, ok)
	require.Equal(t, game.DayVote{Target: ""}, cmd)

	cmd, ok = commandFromClient(clientMsgStartGame, struct{}{})
	require.True(t, ok)
	require.Equal(t, game.StartGame{}, cmd)

	cmd, ok = commandFromClient(clientMsgBeginNight, struct{}{})
	require.True(t, ok)
	require.Equal(t, game.BeginNight{}, cmd)

	cmd, ok = commandFromClient(clientMsgOpenVoting, struct{}{})
	require.True(t, ok)
	require.Equal(t, game.OpenVoting{}, cmd)

	cmd, ok = commandFromClient(clientMsgRevealVotes, struct{}{})
	require.True(t, ok)
	require.Equal(t, game.RevealVotes{}, cmd)

	cmd, ok = commandFromClient(clientMsgClearVotes, struct{}{})
	require.True(t, ok)
	require.Equal(t, game.ClearVotes{}, cmd)

	cmd, ok = commandFromClient(clientMsgFinalizeVotes, struct{}{})
	require.True(t, ok)
	require.Equal(t, game.FinalizeVotes{}, cmd)

	// "join" isn't a command in the engine sense.
	_, ok = commandFromClient(clientMsgJoin, clientJoinData{Name: "x"})
	require.False(t, ok)
}

// --- helpers --------------------------------------------------------------

func mustUnmarshalEnvelope(t *testing.T, raw []byte) envelope {
	t.Helper()
	var env envelope
	require.NoError(t, json.Unmarshal(raw, &env))
	return env
}

func eventTypeName(e game.Event) string {
	env, err := encodeEvent(e)
	if err != nil {
		return "unknown"
	}
	return env.Type
}
