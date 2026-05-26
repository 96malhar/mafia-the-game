package ws

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/require"

	"github.com/malhar/mafia-the-game/internal/room"
	"github.com/malhar/mafia-the-game/internal/wire"
)

// silentLogger discards all log output so tests don't pollute stdout.
func silentLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// newTestServer spins up a manager + ws.Handler behind an httptest
// server. The cleanup tears everything down.
func newTestServer(t *testing.T) (*httptest.Server, *room.Manager) {
	t.Helper()

	mgrCtx, cancelMgr := context.WithCancel(context.Background())
	t.Cleanup(cancelMgr)

	mgr := room.NewManager(mgrCtx, silentLogger())
	t.Cleanup(func() {
		shutdown, c := context.WithTimeout(context.Background(), time.Second)
		defer c()
		_ = mgr.Close(shutdown)
	})

	h := NewHandler(mgr, silentLogger(), HandlerConfig{InsecureSkipOriginCheck: true})

	r := chi.NewRouter()
	r.Post("/api/rooms", h.CreateRoom)
	r.Get("/ws/{code}", h.Connect)
	ts := httptest.NewServer(r)
	t.Cleanup(ts.Close)
	return ts, mgr
}

// dialWS opens a websocket to the given path on the test server.
func dialWS(t *testing.T, ts *httptest.Server, path string) *websocket.Conn {
	t.Helper()
	wsURL := strings.Replace(ts.URL, "http://", "ws://", 1) + path
	// coder/websocket: after a successful Dial we own conn; the resp's
	// Body is already attached to the connection and must NOT be closed
	// by the caller.
	conn, _, err := websocket.Dial(context.Background(), wsURL, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.CloseNow() })
	return conn
}

// recvFrame reads one text frame and decodes the envelope.
func recvFrame(t *testing.T, conn *websocket.Conn) envelope {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	mt, data, err := conn.Read(ctx)
	require.NoError(t, err)
	require.Equal(t, websocket.MessageText, mt)
	var env envelope
	require.NoError(t, json.Unmarshal(data, &env))
	return env
}

// sendFrame encodes an envelope and writes it as a text frame.
func sendFrame(t *testing.T, conn *websocket.Conn, kind string, data any) {
	t.Helper()
	raw, err := json.Marshal(data)
	require.NoError(t, err)
	frame, err := json.Marshal(envelope{Type: kind, Data: raw})
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	require.NoError(t, conn.Write(ctx, websocket.MessageText, frame))
}

// --- Tests ---------------------------------------------------------------

func TestHTTP_CreateRoom(t *testing.T) {
	ts, mgr := newTestServer(t)

	res, err := http.Post(ts.URL+"/api/rooms", "application/json", nil)
	require.NoError(t, err)
	defer func() { _ = res.Body.Close() }()
	require.Equal(t, http.StatusOK, res.StatusCode)

	var body struct {
		Code string `json:"code"`
	}
	require.NoError(t, json.NewDecoder(res.Body).Decode(&body))
	require.Len(t, body.Code, 4)

	// The room must actually exist in the manager.
	_, err = mgr.Get(body.Code)
	require.NoError(t, err)
}

func TestWS_JoinReceivesJoinedAndPlayerJoined(t *testing.T) {
	ts, _ := newTestServer(t)
	code := createRoom(t, ts)

	conn := dialWS(t, ts, "/ws/"+code)
	sendFrame(t, conn, "join", map[string]string{"name": "Alice"})

	// First message: joined ack.
	got := recvFrame(t, conn)
	require.Equal(t, "joined", got.Type)
	var joined serverJoinedData
	require.NoError(t, json.Unmarshal(got.Data, &joined))
	require.NotEmpty(t, joined.PlayerID)
	require.Equal(t, "Alice", joined.Name)
	require.NotEmpty(t, joined.Secret)
	require.Equal(t, code, joined.RoomCode)
	require.True(t, joined.IsHost)
	// The first joiner replays one prior event: the GameCreated
	// emitted by newRoom. This is what tells the web client the
	// lobby's MinPlayers / MaxPlayers / MafiaCount so it doesn't
	// have to hardcode them.
	require.Len(t, joined.Events, 1,
		"the first joiner should replay GameCreated and nothing else")
	require.Equal(t, wire.EventGameCreated, joined.Events[0].Type)

	// Second message: PlayerJoined event.
	got = recvFrame(t, conn)
	require.Equal(t, "event", got.Type)
	var evMsg serverEventData
	require.NoError(t, json.Unmarshal(got.Data, &evMsg))
	require.Equal(t, wire.EventPlayerJoined, evMsg.Event.Type)
}

// TestWS_LateJoinerSeesExistingRoster proves the second player can
// reconstruct who's already in the room from their join ack, not just
// from future broadcasts. Without this, the late joiner's UI would
// show an empty roster until somebody else joined after them.
func TestWS_LateJoinerSeesExistingRoster(t *testing.T) {
	ts, _ := newTestServer(t)
	code := createRoom(t, ts)

	// Alice joins first.
	connA := dialWS(t, ts, "/ws/"+code)
	sendFrame(t, connA, "join", map[string]string{"name": "Alice"})
	_ = recvFrame(t, connA) // joined ack
	_ = recvFrame(t, connA) // own PlayerJoined broadcast

	// Bob joins second; his joined ack must carry Alice's PlayerJoined.
	connB := dialWS(t, ts, "/ws/"+code)
	sendFrame(t, connB, "join", map[string]string{"name": "Bob"})

	ack := recvFrame(t, connB)
	require.Equal(t, "joined", ack.Type)
	var joined serverJoinedData
	require.NoError(t, json.Unmarshal(ack.Data, &joined))
	require.Equal(t, "Bob", joined.Name)
	require.False(t, joined.IsHost)
	// Bob replays exactly two events: the room's GameCreated
	// followed by Alice's PlayerJoined. The order matches r.events
	// (insertion order), which is the contract the web client's
	// reducer relies on.
	require.Len(t, joined.Events, 2,
		"Bob's join ack should carry GameCreated then Alice's PlayerJoined")
	require.Equal(t, wire.EventGameCreated, joined.Events[0].Type)
	require.Equal(t, wire.EventPlayerJoined, joined.Events[1].Type)

	// Bob's own PlayerJoined still arrives separately right after.
	got := recvFrame(t, connB)
	require.Equal(t, "event", got.Type)
}

func TestWS_RejoinReplaysLog(t *testing.T) {
	ts, _ := newTestServer(t)
	code := createRoom(t, ts)

	// First connection joins.
	conn1 := dialWS(t, ts, "/ws/"+code)
	sendFrame(t, conn1, "join", map[string]string{"name": "Alice"})

	ack := recvFrame(t, conn1)
	require.Equal(t, "joined", ack.Type)
	var joined serverJoinedData
	require.NoError(t, json.Unmarshal(ack.Data, &joined))
	// Drain the broadcast PlayerJoined.
	_ = recvFrame(t, conn1)
	_ = conn1.Close(websocket.StatusNormalClosure, "leaving")

	// Reconnect with rejoin params.
	conn2 := dialWS(t, ts,
		"/ws/"+code+"?playerId="+joined.PlayerID+"&secret="+joined.Secret)

	got := recvFrame(t, conn2)
	require.Equal(t, "rejoined", got.Type)
	var rejoined serverRejoinedData
	require.NoError(t, json.Unmarshal(got.Data, &rejoined))
	require.Equal(t, joined.PlayerID, rejoined.PlayerID)
	require.Equal(t, "Alice", rejoined.Name,
		"rejoin ack must echo the player's display name")
	require.NotEmpty(t, rejoined.Events,
		"rejoin must replay events the player can see")
}

func TestWS_BadJSONReturnsError(t *testing.T) {
	ts, _ := newTestServer(t)
	code := createRoom(t, ts)

	conn := dialWS(t, ts, "/ws/"+code)
	// Write a malformed frame DIRECTLY (not via sendFrame).
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	require.NoError(t, conn.Write(ctx, websocket.MessageText, []byte(`not json`)))

	got := recvFrame(t, conn)
	require.Equal(t, "error", got.Type)
	var er serverErrorData
	require.NoError(t, json.Unmarshal(got.Data, &er))
	require.Equal(t, string(wire.ErrCodeBadMessage), er.Code)
}

func TestWS_StartGameProducesPhaseChange(t *testing.T) {
	ts, _ := newTestServer(t)
	code := createRoom(t, ts)

	// Default roster is 5 players. Connect 5 clients, join each.
	// We deliberately do NOT use a deadline-based drain here — coder/
	// websocket treats a cancelled Read as a connection failure, so any
	// drain timeout would kill the connection we still need.
	conns := make([]*websocket.Conn, 5)
	for i := range conns {
		conns[i] = dialWS(t, ts, "/ws/"+code)
		sendFrame(t, conns[i], "join", map[string]string{"name": string(rune('A' + i))})
	}

	// Each connection now has: 1 joined ack + N PlayerJoined broadcasts
	// where N is (i+1) up through (5). Read exactly that many on the
	// host's connection so the upcoming StartGame response is the very
	// next frame. We don't bother draining the others — the connections
	// remain valid even with buffered data.
	for i := 0; i < 1+len(conns); i++ {
		_ = recvFrame(t, conns[0])
	}

	// Host (conn 0) starts the game (deals roles, stays in Lobby),
	// then issues BeginNight to transition into Night. We expect a
	// flurry of events on BeginNight; scan for PhaseChanged → night.
	sendFrame(t, conns[0], "startGame", struct{}{})
	sendFrame(t, conns[0], "beginNight", struct{}{})

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		env := recvFrame(t, conns[0])
		if env.Type != "event" {
			continue
		}
		var ev serverEventData
		require.NoError(t, json.Unmarshal(env.Data, &ev))
		if ev.Event.Type != wire.EventPhaseChanged {
			continue
		}
		var pc struct {
			To string `json:"to"`
		}
		require.NoError(t, json.Unmarshal(ev.Event.Data, &pc))
		if pc.To == "night" {
			return // success
		}
	}
	t.Fatal("did not see phase change into night")
}

// TestWS_SetMafiaRoundTrip drives a setMafia client message and asserts
// the resulting mafiaCountChanged event is broadcast back to the host.
// The host is whoever joins first (the engine's per-room rule).
func TestWS_SetMafiaRoundTrip(t *testing.T) {
	ts, _ := newTestServer(t)
	code := createRoom(t, ts)

	conn := dialWS(t, ts, "/ws/"+code)
	sendFrame(t, conn, "join", map[string]string{"name": "Host"})

	// Drain join ack + own PlayerJoined broadcast.
	_ = recvFrame(t, conn)
	_ = recvFrame(t, conn)

	// Default mafia count for a fresh 5-min lobby is 1; change to 3.
	sendFrame(t, conn, "setMafia", map[string]int{"count": 3})

	env := recvFrame(t, conn)
	require.Equal(t, "event", env.Type)
	var evMsg serverEventData
	require.NoError(t, json.Unmarshal(env.Data, &evMsg))
	require.Equal(t, wire.EventMafiaCountChanged, evMsg.Event.Type)

	var d struct {
		From int `json:"from"`
		To   int `json:"to"`
	}
	require.NoError(t, json.Unmarshal(evMsg.Event.Data, &d))
	require.Equal(t, 1, d.From)
	require.Equal(t, 3, d.To)
}

// TestWS_SetMafiaOutOfRangeReturnsError verifies a bad count is rejected
// with an error frame and no event is broadcast.
func TestWS_SetMafiaOutOfRangeReturnsError(t *testing.T) {
	ts, _ := newTestServer(t)
	code := createRoom(t, ts)

	conn := dialWS(t, ts, "/ws/"+code)
	sendFrame(t, conn, "join", map[string]string{"name": "Host"})
	_ = recvFrame(t, conn)
	_ = recvFrame(t, conn)

	// MaxPlayers default is 20, so max mafia = 17. 99 is way over.
	sendFrame(t, conn, "setMafia", map[string]int{"count": 99})

	env := recvFrame(t, conn)
	require.Equal(t, "error", env.Type)
}

// --- helpers --------------------------------------------------------------

// createRoom hits the create-room endpoint and returns the new code.
func createRoom(t *testing.T, ts *httptest.Server) string {
	t.Helper()
	res, err := http.Post(ts.URL+"/api/rooms", "application/json", nil)
	require.NoError(t, err)
	defer func() { _ = res.Body.Close() }()
	require.Equal(t, http.StatusOK, res.StatusCode)
	var body struct {
		Code string `json:"code"`
	}
	require.NoError(t, json.NewDecoder(res.Body).Decode(&body))
	return body.Code
}
