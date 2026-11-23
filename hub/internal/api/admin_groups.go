package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/recodive/hansho/hub/internal/store"
)

type adminGroupRow struct {
	ID      string
	Members []string
	Summary string
}

type adminGroupForm struct {
	New     bool
	Action  string
	ID      string
	Members string // one per line
	Errors  map[string]string
	Call    *adminAPICall
}

func (s *server) adminGroupsError(w http.ResponseWriter, csrf string, err error) {
	s.log.Error("admin: groups failed", "err", err)
	s.adminNotice(w, http.StatusInternalServerError, csrf, "Something went wrong", "The hub could not do that. Try again.", "/admin/groups", "Back to groups")
}

func (s *server) adminGroups(w http.ResponseWriter, _ *http.Request, csrf string) {
	groups, err := s.Store.ListGroups()
	if err != nil {
		s.adminGroupsError(w, csrf, err)
		return
	}
	rows := make([]adminGroupRow, len(groups))
	for i, g := range groups {
		rows[i] = adminGroupRow{ID: g.ID, Members: g.Members, Summary: adminCount(len(g.Members), "person", "people")}
	}
	s.adminRender(w, http.StatusOK, "groups", "groups", "Groups", csrf, rows)
}

func (s *server) adminGroupNew(w http.ResponseWriter, _ *http.Request, csrf string) {
	s.adminRender(w, http.StatusOK, "group_form", "groups", "New group", csrf, adminGroupForm{New: true, Action: "/admin/groups/new"})
}

// adminGroupRender shows the form for an existing group; the call shows what is stored.
func (s *server) adminGroupRender(w http.ResponseWriter, code int, csrf, id, members string, errs map[string]string) {
	f := adminGroupForm{ID: id, Members: members, Errors: errs, Action: "/admin/groups/" + id + "/edit"}
	if stored, err := s.Store.GetGroup(id); err == nil {
		call := s.adminCall("/api/v1/groups/"+id, struct {
			Members []string `json:"members"`
		}{stored})
		f.Call = &call
	}
	s.adminRender(w, code, "group_form", "groups", "Group "+id, csrf, f)
}

// adminFindGroup returns a group's members, or writes the answer and false.
func (s *server) adminFindGroup(w http.ResponseWriter, r *http.Request, csrf string) ([]string, bool) {
	members, err := s.Store.GetGroup(r.PathValue("id"))
	switch {
	case err == nil:
		return members, true
	case errors.Is(err, store.ErrNotFound):
		s.adminNotice(w, http.StatusNotFound, csrf, "Group not found", "There is no group with this id.", "/admin/groups", "Back to groups")
	default:
		s.adminGroupsError(w, csrf, err)
	}
	return nil, false
}

func (s *server) adminGroupEdit(w http.ResponseWriter, r *http.Request, csrf string) {
	if members, ok := s.adminFindGroup(w, r, csrf); ok {
		s.adminGroupRender(w, http.StatusOK, csrf, r.PathValue("id"), strings.Join(members, "\n"), nil)
	}
}

func (s *server) adminGroupCreate(w http.ResponseWriter, r *http.Request, csrf string) {
	s.adminGroupSubmit(w, r, csrf, strings.TrimSpace(r.PostForm.Get("id")), true)
}

func (s *server) adminGroupUpdate(w http.ResponseWriter, r *http.Request, csrf string) {
	if _, ok := s.adminFindGroup(w, r, csrf); ok {
		s.adminGroupSubmit(w, r, csrf, r.PathValue("id"), false)
	}
}

// adminGroupSubmit stores a group through the same code as the API's PUT. The member list is one name per line.
func (s *server) adminGroupSubmit(w http.ResponseWriter, r *http.Request, csrf, id string, isNew bool) {
	lines := adminLines(r.PostForm.Get("members"))
	text := strings.Join(lines, "\n")
	errs := map[string]string{}
	if isNew {
		if !idPattern.MatchString(id) {
			errs["id"] = "Use letters, numbers, dashes and underscores only, starting with a letter or number, at most 64 characters."
		} else if _, err := s.Store.GetGroup(id); err == nil {
			errs["id"] = "A group with this id already exists. Open it from the list to change it."
		} else if !errors.Is(err, store.ErrNotFound) {
			s.adminGroupsError(w, csrf, err)
			return
		}
	}
	if len(errs) == 0 {
		got, e := s.saveGroup(id, lines)
		switch {
		case e != nil && e.Err != nil:
			s.adminGroupsError(w, csrf, e.Err)
			return
		case e != nil:
			errs["members"] = adminGroupFieldError(e, lines)
		default:
			verb := "saved"
			if isNew {
				verb = "created"
			}
			s.adminInfo("admin: group "+verb, "id", id, "members", len(got))
			s.adminNotice(w, http.StatusOK, csrf, "Group saved", "The group "+id+" now has "+adminCount(len(got), "person", "people")+".", "/admin/groups", "Back to groups")
			return
		}
	}
	if isNew {
		s.adminRender(w, http.StatusBadRequest, "group_form", "groups", "New group", csrf,
			adminGroupForm{New: true, Action: "/admin/groups/new", ID: id, Members: text, Errors: errs})
		return
	}
	s.adminGroupRender(w, http.StatusBadRequest, csrf, id, text, errs)
}

// adminGroupFieldError says in words why the API refused a member list.
func adminGroupFieldError(e *apiError, lines []string) string {
	if rest, ok := strings.CutPrefix(e.Field, "members["); ok {
		if i, err := strconv.Atoi(strings.TrimSuffix(rest, "]")); err == nil && i >= 0 && i < len(lines) {
			name := lines[i]
			if len(name) > 40 {
				name = name[:40] + "..."
			}
			return "The name " + name + " is longer than 256 bytes."
		}
	}
	if e.Field == "members" {
		return "A group can have at most " + strconv.Itoa(maxGroupMembers) + " people."
	}
	return "The hub refused this: " + e.Field + " " + e.Msg + "."
}

type adminGroupUse struct{ Channel, ID, Effect string }

type adminGroupDelete struct {
	ID      string
	Members int
	Uses    []adminGroupUse
}

// adminGroupDeletePage asks before deleting, and says which channels name the group: deleting is allowed, and an
// audience that still names the group then behaves as if the group were empty.
func (s *server) adminGroupDeletePage(w http.ResponseWriter, r *http.Request, csrf string) {
	members, ok := s.adminFindGroup(w, r, csrf)
	if !ok {
		return
	}
	id := r.PathValue("id")
	channels, err := s.Store.ListChannels()
	if err != nil {
		s.adminGroupsError(w, csrf, err)
		return
	}
	d := adminGroupDelete{ID: id, Members: len(members)}
	for _, c := range channels {
		for _, g := range c.Audience.Groups {
			if g != id {
				continue
			}
			effect := "Only listed people may use it: the group's members lose access unless they are named another way."
			if c.Audience.Mode == store.AudienceExcept {
				effect = "Everyone except the listed people may use it: the group's members get access, because they are no longer left out."
			}
			d.Uses = append(d.Uses, adminGroupUse{Channel: c.Name, ID: c.ID, Effect: effect})
		}
	}
	s.adminRender(w, http.StatusOK, "group_delete", "groups", "Delete "+id, csrf, d)
}

func (s *server) adminGroupDeleteDo(w http.ResponseWriter, r *http.Request, csrf string) {
	id := r.PathValue("id")
	ok, err := s.Store.DeleteGroup(id)
	switch {
	case err != nil:
		s.adminGroupsError(w, csrf, err)
	case !ok:
		s.adminNotice(w, http.StatusNotFound, csrf, "Group not found", "There is no group with this id.", "/admin/groups", "Back to groups")
	default:
		s.adminInfo("admin: group deleted", "id", id)
		s.adminNotice(w, http.StatusOK, csrf, "Group deleted", "The group is gone. Channels that still name it treat it as empty.", "/admin/groups", "Back to groups")
	}
}
