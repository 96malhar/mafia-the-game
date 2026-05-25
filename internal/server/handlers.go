package server

import "net/http"

// handleHealth returns a plain-text 200 for liveness probes.
func handleHealth(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}
