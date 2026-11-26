package api

import (
	"net/http"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/recodive/hansho/hub/internal/store"
)

// maxDisplayNameChars bounds a display name, counted in characters (not bytes).
const maxDisplayNameChars = 40

// cleanDisplayName trims a display name and reports whether it may be stored. Any language, accents and emoji are fine;
// control characters (including line breaks) are not, and "" is allowed and clears the name.
func cleanDisplayName(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if !utf8.ValidString(s) || utf8.RuneCountInString(s) > maxDisplayNameChars {
		return "", false
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return "", false
		}
	}
	return s, true
}

const displayNameHelp = "at most 40 characters, no control characters"

// PUT /api/v1/users/{external_user_id}
//
// Sets what other people see for a user instead of their external user id (for example in a page's acknowledged
// banner). The user need not have paired yet. An empty display_name clears it.
func (s *server) putUser(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("external_user_id")
	if !validExternalUserID(id) {
		writeFieldErr(w, http.StatusBadRequest, "invalid_request", "external_user_id", "required, at most 256 bytes")
		return
	}
	body, ok := readBody(w, r, maxJSONBody)
	if !ok {
		return
	}
	var req struct {
		DisplayName string `json:"display_name"`
	}
	if !decodeJSON(w, body, &req) {
		return
	}
	name, ok := cleanDisplayName(req.DisplayName)
	if !ok {
		writeFieldErr(w, http.StatusBadRequest, "invalid_request", "display_name", displayNameHelp)
		return
	}
	if err := s.Store.SetDisplayName(id, name, s.Now()); err != nil {
		s.internal(w, "set display name failed", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// PUT /device/v1/profile
//
// A device changes its user's display name, only when the hub allows it (HUB_ALLOW_DISPLAY_NAME_EDIT). The last write
// wins, whether it came from the sender or from a person.
func (s *server) putProfile(w http.ResponseWriter, _ *http.Request, dev store.DeviceWithPairing, body []byte) {
	if !s.Cfg.AllowDisplayNameEdit {
		writeErr(w, http.StatusForbidden, "display_name_locked")
		return
	}
	var req struct {
		DisplayName string `json:"display_name"`
	}
	if !decodeJSON(w, body, &req) {
		return
	}
	name, ok := cleanDisplayName(req.DisplayName)
	if !ok {
		writeFieldErr(w, http.StatusBadRequest, "invalid_request", "display_name", displayNameHelp)
		return
	}
	if err := s.Store.SetDisplayName(dev.Pairing.ExternalUserID, name, s.Now()); err != nil {
		s.internal(w, "set display name failed", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
