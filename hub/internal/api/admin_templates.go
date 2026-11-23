package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/recodive/hansho/hub/internal/store"
)

// adminUse is a channel that names a template or a sound.
type adminUse struct{ ID, Name string }

// adminUses lists the live channels for which names(c) is id, in channel order.
func (s *server) adminUses(id string, names func(store.Channel) string) ([]adminUse, error) {
	channels, err := s.Store.ListChannels()
	if err != nil {
		return nil, err
	}
	var out []adminUse
	for _, c := range channels {
		if names(c) == id {
			out = append(out, adminUse{c.ID, c.Name})
		}
	}
	return out, nil
}

func channelTemplate(c store.Channel) string { return c.TemplateID }
func channelSound(c store.Channel) string    { return c.Sound }

// adminNodes counts the nodes of a template the way the validator does: a foreach body once.
func adminNodes(v any) int {
	m, ok := v.(map[string]any)
	if !ok {
		return 0
	}
	n := 1
	if kids, ok := m["children"].([]any); ok {
		for _, k := range kids {
			n += adminNodes(k)
		}
	}
	return n + adminNodes(m["body"])
}

type adminTemplateRow struct {
	ID      string
	Version int
	Nodes   int // 0 when the body cannot be read
	Uses    int
}

func (s *server) adminTemplatesError(w http.ResponseWriter, csrf string, err error) {
	s.log.Error("admin: templates failed", "err", err)
	s.adminNotice(w, http.StatusInternalServerError, csrf, "Something went wrong", "The hub could not do that. Try again.", "/admin/templates", "Back to templates")
}

func (s *server) adminTemplates(w http.ResponseWriter, _ *http.Request, csrf string) {
	templates, err := s.Store.ListTemplates()
	if err != nil {
		s.adminTemplatesError(w, csrf, err)
		return
	}
	channels, err := s.Store.ListChannels()
	if err != nil {
		s.adminTemplatesError(w, csrf, err)
		return
	}
	uses := map[string]int{}
	for _, c := range channels {
		uses[c.TemplateID]++
	}
	rows := make([]adminTemplateRow, len(templates))
	for i, t := range templates {
		var v any
		_ = json.Unmarshal(t.Body, &v)
		rows[i] = adminTemplateRow{ID: t.ID, Version: t.Version, Nodes: adminNodes(v), Uses: uses[t.ID]}
	}
	s.adminRender(w, http.StatusOK, "templates", "templates", "Templates", csrf, rows)
}

// adminTemplateVals are the values of the template form.
type adminTemplateVals struct {
	ID, Body string
	// Error is the hub's first complaint about Body, in the web builder's words ("body.children[0].type: unknown
	// component x"); it is also what the AI prompt is asked to fix.
	Error string
	// Valid is set when Body was checked and is fine.
	Valid bool
}

type adminTemplateForm struct {
	New        bool
	Action     string
	ID, Body   string
	Version    int // the stored version, 0 for a template that does not exist yet
	Valid      bool
	Starters   []adminOption
	Prompt     string
	BuilderURL string
	Errors     map[string]string
	Error      string
	Call       *adminAPICall
}

func (s *server) adminTemplateRender(w http.ResponseWriter, code int, csrf string, v adminTemplateVals, isNew bool, errs map[string]string) {
	f := adminTemplateForm{
		New: isNew, ID: v.ID, Body: v.Body, Valid: v.Valid, Errors: errs, Error: v.Error,
		Prompt: adminAIPrompt(v.Body, v.Error), BuilderURL: s.homeURL() + "/templates",
		Starters: []adminOption{{Value: "", Label: "Choose an example", Selected: true}},
	}
	for _, st := range adminStarters {
		f.Starters = append(f.Starters, adminOption{Value: st.ID, Label: st.Label, Help: st.About})
	}
	if isNew {
		f.Action = "/admin/templates/new"
	} else {
		f.Action = "/admin/templates/" + v.ID + "/edit"
		// The call shows what is stored, not what is being typed. The API only takes a higher version than the stored one.
		if t, err := s.Store.GetTemplate(v.ID); err == nil {
			f.Version = t.Version
			call := s.adminCall("/api/v1/templates/"+t.ID, struct {
				Version int             `json:"version"`
				Body    json.RawMessage `json:"body"`
			}{t.Version + 1, json.RawMessage(adminCompact(t.Body))})
			f.Call = &call
		}
	}
	title := "New template"
	if !isNew {
		title = "Template " + v.ID
	}
	s.adminRender(w, code, "template_form", "templates", title, csrf, f)
}

// adminCompact removes the white space between a JSON value's tokens; text that is not JSON is returned as it is.
func adminCompact(b []byte) []byte {
	var out bytes.Buffer
	if json.Compact(&out, b) != nil {
		return b
	}
	return out.Bytes()
}

// adminIndent lays a JSON value out over lines, two spaces a level; text that is not JSON is returned as it is.
func adminIndent(b []byte) string {
	var out bytes.Buffer
	if json.Indent(&out, b, "", "  ") != nil {
		return string(b)
	}
	return out.String()
}

// adminFindTemplate returns the stored template, or writes the answer and false.
func (s *server) adminFindTemplate(w http.ResponseWriter, r *http.Request, csrf string) (store.Template, bool) {
	t, err := s.Store.GetTemplate(r.PathValue("id"))
	switch {
	case err == nil:
		return t, true
	case errors.Is(err, store.ErrNotFound):
		s.adminNotice(w, http.StatusNotFound, csrf, "Template not found", "There is no template with this id.", "/admin/templates", "Back to templates")
	default:
		s.adminTemplatesError(w, csrf, err)
	}
	return store.Template{}, false
}

func (s *server) adminTemplateNew(w http.ResponseWriter, _ *http.Request, csrf string) {
	s.adminTemplateRender(w, http.StatusOK, csrf, adminTemplateVals{}, true, nil)
}

func (s *server) adminTemplateEdit(w http.ResponseWriter, r *http.Request, csrf string) {
	if t, ok := s.adminFindTemplate(w, r, csrf); ok {
		s.adminTemplateRender(w, http.StatusOK, csrf, adminTemplateVals{ID: t.ID, Body: adminIndent(t.Body)}, false, nil)
	}
}

func (s *server) adminTemplateCreate(w http.ResponseWriter, r *http.Request, csrf string) {
	s.adminTemplateSubmit(w, r, csrf, true)
}

func (s *server) adminTemplateUpdate(w http.ResponseWriter, r *http.Request, csrf string) {
	if _, ok := s.adminFindTemplate(w, r, csrf); ok {
		s.adminTemplateSubmit(w, r, csrf, false)
	}
}

// adminTemplateProblem checks the text of a template and says what is wrong in plain words, "" when it is fine. It
// uses the validator of the API's PUT; a problem inside the template reads like the web builder's: the path, then why.
func adminTemplateProblem(text string) (json.RawMessage, string) {
	if text == "" {
		return nil, "Write or paste the template first."
	}
	var v any
	if err := json.Unmarshal([]byte(text), &v); err != nil {
		var syn *json.SyntaxError
		if errors.As(err, &syn) {
			return nil, "body: this is not valid JSON (the hub stopped reading at character " + strconv.FormatInt(syn.Offset, 10) + "). Check the brackets, quotes and commas."
		}
		return nil, "body: this is not valid JSON. Check the brackets, quotes and commas."
	}
	body := json.RawMessage(adminCompact([]byte(text)))
	if e := checkTemplate(body); e != nil {
		return body, e.Field + ": " + e.Msg
	}
	return body, ""
}

// adminTemplateSubmit handles the template form. "Use this example" fills the editor, "Check it" only validates,
// "Save" stores the template through the same code as the API's PUT, with the next version when the template changed.
func (s *server) adminTemplateSubmit(w http.ResponseWriter, r *http.Request, csrf string, isNew bool) {
	f := r.PostForm
	v := adminTemplateVals{ID: strings.TrimSpace(f.Get("id")), Body: strings.TrimSpace(strings.ReplaceAll(f.Get("body"), "\r\n", "\n"))}
	if !isNew {
		v.ID = r.PathValue("id")
	}
	if f.Get("load") != "" {
		for _, st := range adminStarters {
			if st.ID == f.Get("starter") {
				v.Body = st.Template
			}
		}
		s.adminTemplateRender(w, http.StatusOK, csrf, v, isNew, nil)
		return
	}

	errs := map[string]string{}
	var cur store.Template
	exists := false
	if isNew && !idPattern.MatchString(v.ID) {
		errs["id"] = "Use letters, numbers, dashes and underscores only, starting with a letter or number, at most 64 characters."
	} else {
		t, err := s.Store.GetTemplate(v.ID)
		switch {
		case err == nil:
			cur, exists = t, true
		case !errors.Is(err, store.ErrNotFound):
			s.adminTemplatesError(w, csrf, err)
			return
		}
		if isNew && exists {
			errs["id"] = "A template with this id already exists. Open it from the list to change it."
		}
	}
	body, problem := adminTemplateProblem(v.Body)
	if problem != "" {
		errs["body"], v.Error = problem, problem
	}
	if len(errs) > 0 {
		s.adminTemplateRender(w, http.StatusBadRequest, csrf, v, isNew, errs)
		return
	}
	if f.Get("check") != "" {
		v.Valid = true
		s.adminTemplateRender(w, http.StatusOK, csrf, v, isNew, nil)
		return
	}

	version := 1
	if exists {
		if bytes.Equal(adminCompact(cur.Body), body) {
			s.adminNotice(w, http.StatusOK, csrf, "Nothing to save", "The template "+v.ID+" is the same as the saved one, so it stays at version "+strconv.Itoa(cur.Version)+".", "/admin/templates", "Back to templates")
			return
		}
		version = cur.Version + 1
	}
	if e := s.saveTemplate(v.ID, version, body); e != nil {
		switch {
		case e.Err != nil:
			s.adminTemplatesError(w, csrf, e.Err)
			return
		case e.Code == "version_conflict":
			errs["form"] = "Someone saved this template while you were editing it. Open it again from the list to see their version, then make your change again."
		case e.Code == "invalid_template":
			errs["body"], v.Error = e.Field+": "+e.Msg, e.Field+": "+e.Msg
		default:
			errs["form"] = "The hub refused this: " + e.Field + " " + e.Msg + "."
		}
		s.adminTemplateRender(w, http.StatusBadRequest, csrf, v, isNew, errs)
		return
	}
	s.adminInfo("admin: template saved", "id", v.ID, "version", version)
	s.adminNotice(w, http.StatusOK, csrf, "Template saved", "The template "+v.ID+" is saved as version "+strconv.Itoa(version)+" and live on the hub now. Apps pick it up the next time they sync.", "/admin/templates", "Back to templates")
}

type adminTemplateDelete struct {
	ID      string
	Version int
	UsedBy  []adminUse
	Refused bool // the hub refused to delete it just now because channels use it
}

func (s *server) adminTemplateDeletePage(w http.ResponseWriter, r *http.Request, csrf string) {
	t, ok := s.adminFindTemplate(w, r, csrf)
	if !ok {
		return
	}
	uses, err := s.adminUses(t.ID, channelTemplate)
	if err != nil {
		s.adminTemplatesError(w, csrf, err)
		return
	}
	s.adminRender(w, http.StatusOK, "template_delete", "templates", "Delete "+t.ID, csrf, adminTemplateDelete{ID: t.ID, Version: t.Version, UsedBy: uses})
}

// adminTemplateDeleteDo deletes the template through the store, which refuses while a live channel names it (the
// API's 409). The store decides; the list of channels is only for the page.
func (s *server) adminTemplateDeleteDo(w http.ResponseWriter, r *http.Request, csrf string) {
	id := r.PathValue("id")
	ok, err := s.Store.DeleteTemplate(id)
	switch {
	case errors.Is(err, store.ErrInUse):
		uses, err := s.adminUses(id, channelTemplate)
		if err != nil {
			s.adminTemplatesError(w, csrf, err)
			return
		}
		s.adminRender(w, http.StatusConflict, "template_delete", "templates", "Delete "+id, csrf, adminTemplateDelete{ID: id, UsedBy: uses, Refused: true})
	case err != nil:
		s.adminTemplatesError(w, csrf, err)
	case !ok:
		s.adminNotice(w, http.StatusNotFound, csrf, "Template not found", "There is no template with this id.", "/admin/templates", "Back to templates")
	default:
		s.adminInfo("admin: template deleted", "id", id)
		s.adminNotice(w, http.StatusOK, csrf, "Template deleted", "The template is gone. You can make one with the same id again later.", "/admin/templates", "Back to templates")
	}
}
