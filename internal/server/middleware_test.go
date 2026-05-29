package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// captureRemoteAddr is a terminal handler that records the RemoteAddr it
// sees, so realClientIP tests can assert what the next handler observed.
func captureRemoteAddr(got *string) http.Handler {
	return http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		*got = r.RemoteAddr
	})
}

func TestRealClientIP(t *testing.T) {
	const directPeer = "10.0.0.1:54321" // what the socket reports

	cases := []struct {
		name    string
		header  string            // configured trusted header ("" = disabled)
		reqHdrs map[string]string // headers set on the incoming request
		want    string            // expected RemoteAddr seen downstream
	}{
		{
			name:   "disabled: RemoteAddr untouched even if spoof headers present",
			header: "",
			reqHdrs: map[string]string{
				"Fly-Client-IP":   "1.2.3.4",
				"X-Forwarded-For": "5.6.7.8",
			},
			want: directPeer,
		},
		{
			name:    "configured header present: RemoteAddr rewritten to it",
			header:  "Fly-Client-IP",
			reqHdrs: map[string]string{"Fly-Client-IP": "203.0.113.7"},
			want:    "203.0.113.7",
		},
		{
			name:    "configured header absent: RemoteAddr unchanged",
			header:  "Fly-Client-IP",
			reqHdrs: map[string]string{"X-Forwarded-For": "5.6.7.8"},
			want:    directPeer,
		},
		{
			name:    "only the configured header is trusted, not other proxy headers",
			header:  "Fly-Client-IP",
			reqHdrs: map[string]string{"X-Forwarded-For": "5.6.7.8"},
			want:    directPeer,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var seen string
			h := realClientIP(tc.header)(captureRemoteAddr(&seen))

			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.RemoteAddr = directPeer
			for k, v := range tc.reqHdrs {
				req.Header.Set(k, v)
			}

			h.ServeHTTP(httptest.NewRecorder(), req)
			require.Equal(t, tc.want, seen)
		})
	}
}

func TestSecurityHeaders(t *testing.T) {
	h := securityHeaders()(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	require.Equal(t, "nosniff", rec.Header().Get("X-Content-Type-Options"))
	require.Equal(t, "DENY", rec.Header().Get("X-Frame-Options"))
	require.Equal(t, "no-referrer", rec.Header().Get("Referrer-Policy"))
}

func TestLimitBody(t *testing.T) {
	const limit = 16

	// The handler tries to read the whole body; MaxBytesReader makes the
	// read fail once the cap is exceeded, which we surface as 413.
	h := limitBody(limit)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := io.ReadAll(r.Body); err != nil {
			http.Error(w, "too large", http.StatusRequestEntityTooLarge)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))

	t.Run("under the cap passes", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(strings.Repeat("a", limit)))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		require.Equal(t, http.StatusOK, rec.Code)
	})

	t.Run("over the cap is rejected", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(strings.Repeat("a", limit+1)))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		require.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)
	})
}
