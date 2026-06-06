package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/require"

	"github.com/96malhar/mafia-the-game/internal/room"
	"github.com/96malhar/mafia-the-game/internal/transport/ws"
)

// newTestServer builds a Server backed by an in-memory filesystem so tests
// don't depend on the on-disk web/ directory.
func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	fakeFS := fstest.MapFS{
		"index.html": &fstest.MapFile{Data: []byte("<!doctype html><title>test</title>")},
	}
	srv := New(Config{
		Addr:  ":0", // unused; httptest binds its own listener
		WebFS: fakeFS,
	})
	ts := httptest.NewServer(srv.handler())
	t.Cleanup(ts.Close)
	return ts
}

// newTestServerWithWS builds a Server with the game routes wired in
// (so POST /api/rooms exists), using the given room-create rate limit.
func newTestServerWithWS(t *testing.T, roomCreateRPM int) *httptest.Server {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	mgr := room.NewManager(ctx, nil)
	wsHandler := ws.NewHandler(mgr, nil, ws.HandlerConfig{})

	srv := New(Config{
		Addr:          ":0",
		WebFS:         fstest.MapFS{},
		WS:            wsHandler,
		RoomCreateRPM: roomCreateRPM,
	})
	ts := httptest.NewServer(srv.handler())
	t.Cleanup(ts.Close)
	return ts
}

func postRoom(t *testing.T, baseURL string) int {
	t.Helper()
	resp, err := http.Post(baseURL+"/api/rooms", "application/json", nil)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode
}

func TestRoomCreateRateLimit(t *testing.T) {
	t.Run("limit enforced per IP", func(t *testing.T) {
		// All requests originate from the same loopback IP, so the
		// per-IP limiter counts them together: 2 allowed, 3rd rejected.
		ts := newTestServerWithWS(t, 2)
		require.Equal(t, http.StatusOK, postRoom(t, ts.URL))
		require.Equal(t, http.StatusOK, postRoom(t, ts.URL))
		require.Equal(t, http.StatusTooManyRequests, postRoom(t, ts.URL))
	})

	t.Run("disabled when RPM is zero", func(t *testing.T) {
		ts := newTestServerWithWS(t, 0)
		for range 5 {
			require.Equal(t, http.StatusOK, postRoom(t, ts.URL),
				"no limiter should be installed at RPM=0")
		}
	})
}

func TestCheckRoom(t *testing.T) {
	ts := newTestServerWithWS(t, 0)

	// Create a room, then read back its code from the JSON response.
	resp, err := http.Post(ts.URL+"/api/rooms", "application/json", nil)
	require.NoError(t, err)
	var created struct {
		Code string `json:"code"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&created))
	_ = resp.Body.Close()
	require.NotEmpty(t, created.Code)

	t.Run("existing open room returns 200, joinable", func(t *testing.T) {
		r, err := http.Get(ts.URL + "/api/rooms/" + created.Code)
		require.NoError(t, err)
		defer func() { _ = r.Body.Close() }()
		require.Equal(t, http.StatusOK, r.StatusCode)
		var got struct {
			Code     string `json:"code"`
			Joinable bool   `json:"joinable"`
			Reason   string `json:"reason"`
			Message  string `json:"message"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&got))
		require.Equal(t, created.Code, got.Code)
		// A fresh room's lobby is open, so the probe reports it joinable
		// with no reason/message (those are reserved for the unjoinable
		// states — in progress / full / ended — covered by the room-layer
		// TestRoom_JoinStatus).
		require.True(t, got.Joinable)
		require.Empty(t, got.Reason)
		require.Empty(t, got.Message)
	})

	t.Run("missing room returns 404", func(t *testing.T) {
		r, err := http.Get(ts.URL + "/api/rooms/NOPE")
		require.NoError(t, err)
		defer func() { _ = r.Body.Close() }()
		require.Equal(t, http.StatusNotFound, r.StatusCode)
	})
}

func TestRoutes(t *testing.T) {
	ts := newTestServer(t)

	cases := []struct {
		name       string
		method     string
		path       string
		wantStatus int
		// wantBody is asserted only when non-empty. The 404 case, for
		// example, doesn't care about the body content (Go's file server
		// renders its own message we don't want to lock in).
		wantBody string
	}{
		{
			name:       "healthz returns ok",
			method:     http.MethodGet,
			path:       "/healthz",
			wantStatus: http.StatusOK,
			wantBody:   "ok",
		},
		{
			name:       "root serves index.html from WebFS",
			method:     http.MethodGet,
			path:       "/",
			wantStatus: http.StatusOK,
			wantBody:   "<!doctype html><title>test</title>",
		},
		{
			// Forward-looking guard: we do NOT want SPA-style fallback
			// where every unknown path returns index.html. If we ever
			// adopt a frontend router, this test will fail loudly and
			// force a deliberate decision.
			name:       "unknown path is 404, not SPA fallback",
			method:     http.MethodGet,
			path:       "/does-not-exist",
			wantStatus: http.StatusNotFound,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			req, err := http.NewRequest(tc.method, ts.URL+tc.path, nil)
			require.NoError(t, err, "build request")

			resp, err := http.DefaultClient.Do(req)
			require.NoError(t, err, "do request")
			defer func() { _ = resp.Body.Close() }()

			require.Equal(t, tc.wantStatus, resp.StatusCode, "status")

			if tc.wantBody != "" {
				body, err := io.ReadAll(resp.Body)
				require.NoError(t, err, "read body")
				require.Equal(t, tc.wantBody, string(body), "body")
			}
		})
	}
}
