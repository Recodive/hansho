package api

import "net/http"

// GET /device/v1/hub is unauthenticated: the app reads it before pairing so it
// can show the sender and compare the key fingerprint with the invite's.
func (s *server) publicHubInfo(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.hubInfo())
}
