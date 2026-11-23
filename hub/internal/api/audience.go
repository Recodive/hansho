package api

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/recodive/hansho/hub/internal/store"
)

const (
	maxAudienceEntries = 1000 // users, and groups, per audience
	maxGroupMembers    = 5000
)

// checkAudience validates an audience from a request and returns it normalized, or the failing field path (under
// prefix, e.g. "audience") and a message. Users may not have paired yet, so they are not looked up; groups are checked
// by the callers (see checkGroupsExist).
func checkAudience(a store.Audience, prefix string) (store.Audience, string, string) {
	switch a.Mode {
	case store.AudienceEveryone, store.AudienceOnly, store.AudienceExcept:
	default:
		return a, prefix + ".mode", "must be one of everyone, only, except"
	}
	if a.Mode == store.AudienceEveryone && (len(a.Users) > 0 || len(a.Groups) > 0) {
		return a, prefix + ".users", "must be empty for mode everyone"
	}
	if len(a.Users) > maxAudienceEntries {
		return a, prefix + ".users", "at most " + strconv.Itoa(maxAudienceEntries) + " entries"
	}
	if len(a.Groups) > maxAudienceEntries {
		return a, prefix + ".groups", "at most " + strconv.Itoa(maxAudienceEntries) + " entries"
	}
	for i, u := range a.Users {
		if u == "" || len(u) > maxFieldLen {
			return a, prefix + ".users[" + strconv.Itoa(i) + "]", "required, at most 256 bytes"
		}
	}
	for i, g := range a.Groups {
		if !idPattern.MatchString(g) {
			return a, prefix + ".groups[" + strconv.Itoa(i) + "]", "must match [A-Za-z0-9][A-Za-z0-9_-]{0,63}"
		}
	}
	a.Users, a.Groups = dedupe(a.Users), dedupe(a.Groups)
	return a.Normalized(), "", ""
}

// checkGroupsExist refuses an audience that names a group that does not exist, so a misspelled "except" group cannot
// silently exclude nobody. Deleting a group later stays allowed. It returns the failing field and message, or "" "".
func (s *server) checkGroupsExist(a store.Audience, prefix string) (string, string, error) {
	for _, g := range a.Groups {
		if _, err := s.Store.GetGroup(g); errors.Is(err, store.ErrNotFound) {
			return prefix + ".groups", "unknown group", nil
		} else if err != nil {
			return "", "", err
		}
	}
	return "", "", nil
}

// dedupe drops repeated entries, keeping the first of each.
func dedupe(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// noAccess is the answer to a device that asks for something outside its user's audiences.
func noAccess(w http.ResponseWriter) { writeErr(w, http.StatusForbidden, "no_access") }

// channelAccess reports whether the device's user may use the channel; it writes the 403 (or the 500) when not.
// An unknown channel is refused the same way, so a message or activity whose channel is gone leaks nothing.
func (s *server) channelAccess(w http.ResponseWriter, user, channelID string) bool {
	ok, err := s.Store.CanAccessChannel(user, channelID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		s.internal(w, "access check failed", err)
		return false
	}
	if !ok {
		noAccess(w)
	}
	return ok
}

// channelAllows reports whether acc may use the channel; an unreadable or unknown channel does not allow.
func (s *server) channelAllows(acc store.Access, channelID string) bool {
	ch, err := s.Store.GetChannel(channelID)
	return err == nil && acc.Allows(ch.Audience)
}

// devicesInAudience keeps the devices whose user is in the audience.
func (s *server) devicesInAudience(devices []store.DeviceWithPairing, aud store.Audience) ([]store.DeviceWithPairing, error) {
	allowed := map[string]bool{}
	var out []store.DeviceWithPairing
	for _, d := range devices {
		user := d.Pairing.ExternalUserID
		ok, seen := allowed[user]
		if !seen {
			acc, err := s.Store.AccessFor(user)
			if err != nil {
				return nil, err
			}
			ok = acc.Allows(aud)
			allowed[user] = ok
		}
		if ok {
			out = append(out, d)
		}
	}
	return out, nil
}

type groupView struct {
	ID      string   `json:"id"`
	Members []string `json:"members"`
}

// PUT /api/v1/groups/{id}
//
// Replaces the whole member list; there are no per-member endpoints. Members are external user ids and may not have
// paired yet.
func (s *server) putGroup(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !idPattern.MatchString(id) {
		writeFieldErr(w, http.StatusBadRequest, "invalid_request", "id", "must match [A-Za-z0-9][A-Za-z0-9_-]{0,63}")
		return
	}
	body, ok := readBody(w, r, maxJSONBody)
	if !ok {
		return
	}
	var req struct {
		Members *[]string `json:"members"`
	}
	if !decodeJSON(w, body, &req) {
		return
	}
	if req.Members == nil {
		writeFieldErr(w, http.StatusBadRequest, "invalid_request", "members", "required")
		return
	}
	got, e := s.saveGroup(id, *req.Members)
	if e != nil {
		s.fail(w, e)
		return
	}
	writeJSON(w, http.StatusOK, groupView{ID: id, Members: got})
}

// saveGroup validates a member list, replaces the group's members with it (deduplicated) and returns what is stored.
// The caller has checked that id matches idPattern. The API and the admin UI share it.
func (s *server) saveGroup(id string, members []string) ([]string, *apiError) {
	if len(members) > maxGroupMembers {
		return nil, badField("members", "at most "+strconv.Itoa(maxGroupMembers)+" entries")
	}
	for i, m := range members {
		if m == "" || len(m) > maxFieldLen {
			return nil, badField("members["+strconv.Itoa(i)+"]", "required, at most 256 bytes")
		}
	}
	if err := s.Store.PutGroup(id, dedupe(members)); err != nil {
		return nil, internalErr("put group failed", err)
	}
	got, err := s.Store.GetGroup(id)
	if err != nil {
		return nil, internalErr("group lookup failed", err)
	}
	return got, nil
}

// GET /api/v1/groups
func (s *server) listGroups(w http.ResponseWriter, _ *http.Request) {
	groups, err := s.Store.ListGroups()
	if err != nil {
		s.internal(w, "list groups failed", err)
		return
	}
	out := make([]groupView, 0, len(groups))
	for _, g := range groups {
		out = append(out, groupView{ID: g.ID, Members: g.Members})
	}
	writeJSON(w, http.StatusOK, out)
}

// GET /api/v1/groups/{id}
func (s *server) getGroup(w http.ResponseWriter, r *http.Request) {
	members, err := s.Store.GetGroup(r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		writeErr(w, http.StatusNotFound, "unknown_group")
		return
	}
	if err != nil {
		s.internal(w, "group lookup failed", err)
		return
	}
	writeJSON(w, http.StatusOK, groupView{ID: r.PathValue("id"), Members: members})
}

// DELETE /api/v1/groups/{id}
func (s *server) deleteGroup(w http.ResponseWriter, r *http.Request) {
	ok, err := s.Store.DeleteGroup(r.PathValue("id"))
	if err != nil {
		s.internal(w, "delete group failed", err)
		return
	}
	if !ok {
		writeErr(w, http.StatusNotFound, "unknown_group")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
