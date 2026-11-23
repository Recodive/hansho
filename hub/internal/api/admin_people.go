package api

import (
	"net/http"
	"sort"
	"strings"
)

type adminDevice struct{ Name, Platform, Joined string }

type adminPerson struct {
	Name, Paired string
	Devices      []adminDevice
}

// adminPersons groups active devices by the person (external_user_id) they belong to, people in name order.
func (s *server) adminPersons() ([]adminPerson, error) {
	devs, err := s.Store.AllActiveDevices()
	if err != nil {
		return nil, err
	}
	var out []adminPerson
	at := map[string]int{}
	for _, d := range devs { // oldest first, so the first device's pairing is the person's earliest
		i, ok := at[d.Pairing.ExternalUserID]
		if !ok {
			i = len(out)
			at[d.Pairing.ExternalUserID] = i
			out = append(out, adminPerson{Name: d.Pairing.ExternalUserID, Paired: adminTime(d.Pairing.CreatedAt)})
		}
		out[i].Devices = append(out[i].Devices, adminDevice{Name: d.Name, Platform: d.Platform, Joined: adminTime(d.CreatedAt)})
	}
	sort.SliceStable(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	return out, nil
}

func (s *server) adminPeople(w http.ResponseWriter, _ *http.Request, csrf string) {
	people, err := s.adminPersons()
	if err != nil {
		s.log.Error("admin: list people failed", "err", err)
		s.adminNotice(w, http.StatusInternalServerError, csrf, "Something went wrong", "The hub could not list people. Try again.", "/admin/", "Back to the start")
		return
	}
	s.adminRender(w, http.StatusOK, "people", "people", "People", csrf, people)
}

// adminRemovePage asks before removing someone.
func (s *server) adminRemovePage(w http.ResponseWriter, r *http.Request, csrf string) {
	name := r.URL.Query().Get("person")
	devs, err := s.Store.ActiveDevicesForUser(name)
	if err != nil {
		s.log.Error("admin: list devices failed", "err", err)
		s.adminNotice(w, http.StatusInternalServerError, csrf, "Something went wrong", "The hub could not look this person up. Try again.", "/admin/people", "Back to people")
		return
	}
	if len(devs) == 0 {
		s.adminNotice(w, http.StatusNotFound, csrf, "Person not found", "No one with this name is paired with this hub.", "/admin/people", "Back to people")
		return
	}
	p := adminPerson{Name: name}
	for _, d := range devs {
		p.Devices = append(p.Devices, adminDevice{Name: d.Name, Platform: d.Platform, Joined: adminTime(d.CreatedAt)})
	}
	s.adminRender(w, http.StatusOK, "person_remove", "people", "Remove "+name, csrf, p)
}

// adminRemove ends the person's pairings exactly like the API's unpair call.
func (s *server) adminRemove(w http.ResponseWriter, r *http.Request, csrf string) {
	name := r.PostForm.Get("person")
	n, err := s.kickUser(r.Context(), name)
	if err != nil {
		s.log.Error("admin: remove person failed", "external_user_id", lc(name), "err", lc(err.Error()))
		s.adminNotice(w, http.StatusInternalServerError, csrf, "Something went wrong", "The hub could not remove this person completely. Check the list and try again.", "/admin/people", "Back to people")
		return
	}
	s.log.Info("admin: person removed", "external_user_id", lc(name), "pairings", n)
	msg := name + " was removed. Their devices stop receiving messages and need a new pair code to join again."
	if n == 0 {
		msg = name + " was not paired any more, so there was nothing to remove."
	}
	s.adminNotice(w, http.StatusOK, csrf, "Removed", msg, "/admin/people", "Back to people")
}
