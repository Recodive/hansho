package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/recodive/hansho/hub/internal/level"
	"github.com/recodive/hansho/hub/internal/store"
)

// adminOption is one choice of a select (or a radio group).
type adminOption struct {
	Value, Label, Help string
	Selected           bool
}

// adminChoice is one tick box of a list.
type adminChoice struct {
	Value, Label string
	Checked      bool
}

// adminLevels says what each level does in words an admin without code can use.
var adminLevels = []struct{ Value, Label, Help string }{
	{level.Passive, "Passive", "Arrives quietly in the inbox, with no sound. Good for news nobody needs to see right now."},
	{level.Active, "Active", "A normal notification with a sound."},
	{level.TimeSensitive, "Time-sensitive", "Breaks through Focus modes, so it is seen even when the person asked not to be disturbed."},
	{level.Critical, "Critical", "Sounds even when the phone is on silent."},
	{level.Page, "Page", "Rings like an alarm and repeats until someone acknowledges it on any of their devices."},
}

// adminLevelOptions lists the levels with selected marked. A first choice with an empty value is added when usual is not "".
func adminLevelOptions(selected, usual string) []adminOption {
	var out []adminOption
	if usual != "" {
		out = append(out, adminOption{Value: "", Label: usual, Selected: selected == ""})
	}
	for _, l := range adminLevels {
		out = append(out, adminOption{Value: l.Value, Label: l.Label, Help: l.Help, Selected: l.Value == selected})
	}
	return out
}

func adminLevelLabel(v string) string {
	for _, l := range adminLevels {
		if l.Value == v {
			return l.Label
		}
	}
	return v
}

func adminLevelHelp(v string) string {
	for _, l := range adminLevels {
		if l.Value == v {
			return l.Help
		}
	}
	return ""
}

// The kinds of a channel's button: none (the press is reported to your software), a link, or a typed reply.
var adminKinds = []adminOption{
	{Value: "", Label: "Plain button (your software is told when it is pressed)"},
	{Value: "url", Label: "Opens a link"},
	{Value: "text", Label: "Asks for a typed reply"},
}

const adminMaxActions = 20

type adminActionRow struct {
	N, Num               int // Num is N for people, counting from 1
	ID, Title, Kind, URL string
	Ack                  bool
	Kinds                []adminOption
	Error                string
}

func adminKindOptions(selected string) []adminOption {
	out := make([]adminOption, 0, len(adminKinds)+1)
	known := false
	for _, k := range adminKinds {
		k.Selected = k.Value == selected
		known = known || k.Selected
		out = append(out, k)
	}
	if !known { // a kind set through the API stays as it is
		out = append(out, adminOption{Value: selected, Label: selected, Selected: true})
	}
	return out
}

// adminAPICall is a record shown for copying: the curl command that stores it through the API, and the JSON body.
type adminAPICall struct{ Curl, Body string }

// adminJSON indents v without HTML escaping.
func adminJSON(v any) string {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
	return strings.TrimRight(b.String(), "\n")
}

// adminCall is the API call that makes body the state of path. The key is left as $KEY for the admin to fill in.
func (s *server) adminCall(path string, body any) adminAPICall {
	j := adminJSON(body)
	curl := "curl -X PUT '" + s.Cfg.PublicURL + path + "' \\\n" +
		"  -H \"Authorization: Bearer $KEY\" \\\n" +
		"  -H 'Content-Type: application/json' \\\n" +
		"  -d '" + strings.ReplaceAll(j, "'", "'\\''") + "'"
	return adminAPICall{Curl: curl, Body: j}
}

// adminChannelVals are the values of the channel form.
type adminChannelVals struct {
	ID, Name, DefaultLevel, MaxLevel, Sound, Template, Thread string
	Actions                                                   []adminActionRow
	Mode, Extra                                               string
	Users, Groups                                             []string // ticked
}

func adminChannelValsOf(c store.Channel) adminChannelVals {
	a := c.Audience.Normalized()
	v := adminChannelVals{ID: c.ID, Name: c.Name, DefaultLevel: c.DefaultLevel, MaxLevel: c.MaxLevel, Sound: c.Sound,
		Template: c.TemplateID, Thread: c.Thread, Mode: a.Mode, Users: a.Users, Groups: a.Groups}
	if v.Thread == v.ID {
		v.Thread = "" // the same as leaving it out
	}
	var list []struct {
		ID    string `json:"id"`
		Title string `json:"title"`
		Kind  string `json:"kind"`
		URL   string `json:"url"`
		Ack   bool   `json:"ack"`
	}
	_ = json.Unmarshal([]byte(c.ActionsJSON), &list)
	for _, x := range list {
		v.Actions = append(v.Actions, adminActionRow{ID: x.ID, Title: x.Title, Kind: x.Kind, URL: x.URL, Ack: x.Ack})
	}
	return v
}

// adminLines splits a text area into trimmed, non-empty lines (browsers send CRLF).
func adminLines(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out
}

// adminChannelValsFromForm reads the channel form. The id of an existing channel comes from the path, not the form.
func adminChannelValsFromForm(f url.Values) adminChannelVals {
	v := adminChannelVals{
		ID: strings.TrimSpace(f.Get("id")), Name: strings.TrimSpace(f.Get("name")),
		DefaultLevel: f.Get("default_level"), MaxLevel: f.Get("max_level"), Sound: f.Get("sound"), Template: f.Get("template"),
		Thread: strings.TrimSpace(f.Get("thread")), Mode: f.Get("mode"), Extra: f.Get("extra"),
		Users: f["user"], Groups: f["group"],
	}
	n, _ := strconv.Atoi(f.Get("actions"))
	for i := 0; i < min(max(n, 0), adminMaxActions); i++ {
		p := "a" + strconv.Itoa(i) + "_"
		v.Actions = append(v.Actions, adminActionRow{
			ID: strings.TrimSpace(f.Get(p + "id")), Title: strings.TrimSpace(f.Get(p + "title")), Kind: f.Get(p + "kind"),
			URL: strings.TrimSpace(f.Get(p + "url")), Ack: f.Get(p+"ack") != "",
		})
	}
	return v
}

type adminChannelForm struct {
	New                      bool
	Action                   string
	ID, Name, Thread, Extra  string
	DefaultLevels, MaxLevels []adminOption
	DefaultHelp, MaxHelp     string
	Sounds, Templates        []adminOption
	Actions                  []adminActionRow
	CanAdd                   bool
	Mode                     string
	People, Groups           []adminChoice
	Errors                   map[string]string
	Call                     *adminAPICall
}

// adminChoices lists the known names with those in ticked ticked. A ticked name that is not known is kept at the end,
// marked with unknown, so it can be unticked.
func adminChoices(known, ticked []string, unknown string) []adminChoice {
	is := map[string]bool{}
	for _, t := range ticked {
		is[t] = true
	}
	seen := map[string]bool{}
	var out []adminChoice
	for _, k := range known {
		seen[k] = true
		out = append(out, adminChoice{Value: k, Label: k, Checked: is[k]})
	}
	var extra []string
	for _, t := range ticked {
		if !seen[t] {
			seen[t] = true
			extra = append(extra, t)
		}
	}
	sort.Strings(extra)
	for _, t := range extra {
		out = append(out, adminChoice{Value: t, Label: t + unknown, Checked: true})
	}
	return out
}

// adminChannelPage builds what the channel form shows.
func (s *server) adminChannelPage(v adminChannelVals, isNew bool, errs map[string]string) (adminChannelForm, error) {
	people, err := s.adminPersons()
	if err != nil {
		return adminChannelForm{}, err
	}
	groups, err := s.Store.ListGroups()
	if err != nil {
		return adminChannelForm{}, err
	}
	sounds, err := s.Store.ListSounds()
	if err != nil {
		return adminChannelForm{}, err
	}
	templates, err := s.Store.ListTemplates()
	if err != nil {
		return adminChannelForm{}, err
	}
	var names, gids []string
	for _, p := range people {
		names = append(names, p.Name)
	}
	for _, g := range groups {
		gids = append(gids, g.ID)
	}
	f := adminChannelForm{
		New: isNew, ID: v.ID, Name: v.Name, Thread: v.Thread, Extra: v.Extra, Mode: v.Mode, Errors: errs,
		DefaultLevels: adminLevelOptions(v.DefaultLevel, ""), MaxLevels: adminLevelOptions(v.MaxLevel, ""),
		DefaultHelp: adminLevelHelp(v.DefaultLevel), MaxHelp: adminLevelHelp(v.MaxLevel),
		Sounds:    []adminOption{{Value: "", Label: "The app's default sound", Selected: v.Sound == ""}},
		Templates: []adminOption{{Value: "", Label: "No template (a plain notification)", Selected: v.Template == ""}},
		Actions:   v.Actions, CanAdd: len(v.Actions) < adminMaxActions,
		People: adminChoices(names, v.Users, " (not paired yet)"), Groups: adminChoices(gids, v.Groups, " (no longer exists)"),
	}
	for _, x := range sounds {
		f.Sounds = append(f.Sounds, adminOption{Value: x.ID, Label: x.ID, Selected: x.ID == v.Sound})
	}
	for _, t := range templates {
		f.Templates = append(f.Templates, adminOption{Value: t.ID, Label: t.ID, Selected: t.ID == v.Template})
	}
	for i := range f.Actions {
		a := &f.Actions[i]
		a.N, a.Num, a.Kinds, a.Error = i, i+1, adminKindOptions(a.Kind), errs["a"+strconv.Itoa(i)]
	}
	if isNew {
		f.Action = "/admin/channels/new"
	} else {
		f.Action = "/admin/channels/" + v.ID + "/edit"
	}
	return f, nil
}

// adminChannelPutBody is the body of the API call that stores c.
func adminChannelPutBody(c store.Channel) channelInput {
	aud := c.Audience.Normalized()
	actions := c.ActionsJSON
	if actions == "" {
		actions = "[]"
	}
	return channelInput{Name: c.Name, DefaultLevel: c.DefaultLevel, MaxLevel: c.MaxLevel, Sound: c.Sound, TemplateID: c.TemplateID,
		Thread: c.Thread, Actions: json.RawMessage(actions), Audience: &aud}
}

// adminAudienceText says who may use a channel, briefly.
func adminAudienceText(a store.Audience) string {
	a = a.Normalized()
	if a.Mode == store.AudienceEveryone {
		return "Everyone"
	}
	var parts []string
	if n := len(a.Users); n > 0 {
		parts = append(parts, adminCount(n, "person", "people"))
	}
	if n := len(a.Groups); n > 0 {
		parts = append(parts, adminCount(n, "group", "groups"))
	}
	list := "nobody"
	if len(parts) > 0 {
		list = strings.Join(parts, " and ")
	}
	if len(a.Users)+len(a.Groups) <= 3 {
		list = strings.Join(append(append([]string{}, a.Users...), a.Groups...), ", ")
		if list == "" {
			list = "nobody"
		}
	}
	switch a.Mode {
	case store.AudienceOnly:
		return "Only " + list
	case store.AudienceExcept:
		return "Everyone except " + list
	}
	return "Unknown (the hub refuses everyone)"
}

func adminCount(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return strconv.Itoa(n) + " " + many
}

type adminChannelRow struct {
	ID, Name, Default, Max, Who string
}

func (s *server) adminChannelsError(w http.ResponseWriter, csrf string, err error) {
	s.log.Error("admin: channels failed", "err", err)
	s.adminNotice(w, http.StatusInternalServerError, csrf, "Something went wrong", "The hub could not do that. Try again.", "/admin/channels", "Back to channels")
}

func (s *server) adminChannels(w http.ResponseWriter, _ *http.Request, csrf string) {
	channels, err := s.Store.ListChannels()
	if err != nil {
		s.adminChannelsError(w, csrf, err)
		return
	}
	rows := make([]adminChannelRow, len(channels))
	for i, c := range channels {
		rows[i] = adminChannelRow{c.ID, c.Name, adminLevelLabel(c.DefaultLevel), adminLevelLabel(c.MaxLevel), adminAudienceText(c.Audience)}
	}
	s.adminRender(w, http.StatusOK, "channels", "channels", "Channels", csrf, rows)
}

func (s *server) adminChannelNew(w http.ResponseWriter, _ *http.Request, csrf string) {
	v := adminChannelVals{DefaultLevel: level.Active, MaxLevel: level.TimeSensitive, Mode: store.AudienceEveryone}
	s.adminChannelRender(w, http.StatusOK, csrf, v, true, nil)
}

func (s *server) adminChannelRender(w http.ResponseWriter, code int, csrf string, v adminChannelVals, isNew bool, errs map[string]string) {
	f, err := s.adminChannelPage(v, isNew, errs)
	if err != nil {
		s.adminChannelsError(w, csrf, err)
		return
	}
	if !isNew { // the call shows what is stored, not what is being typed
		if c, err := s.Store.GetChannel(v.ID); err == nil {
			call := s.adminCall("/api/v1/channels/"+c.ID, adminChannelPutBody(c))
			f.Call = &call
		}
	}
	title := "New channel"
	if !isNew {
		title = "Channel " + v.ID
	}
	s.adminRender(w, code, "channel_form", "channels", title, csrf, f)
}

// adminFindChannel returns a live channel, or writes the answer and false.
func (s *server) adminFindChannel(w http.ResponseWriter, r *http.Request, csrf string) (store.Channel, bool) {
	c, err := s.Store.GetChannel(r.PathValue("id"))
	switch {
	case err == nil && c.DeletedAt.IsZero():
		return c, true
	case err == nil || errors.Is(err, store.ErrNotFound):
		s.adminNotice(w, http.StatusNotFound, csrf, "Channel not found", "There is no channel with this id.", "/admin/channels", "Back to channels")
	default:
		s.adminChannelsError(w, csrf, err)
	}
	return store.Channel{}, false
}

func (s *server) adminChannelEdit(w http.ResponseWriter, r *http.Request, csrf string) {
	if c, ok := s.adminFindChannel(w, r, csrf); ok {
		s.adminChannelRender(w, http.StatusOK, csrf, adminChannelValsOf(c), false, nil)
	}
}

func (s *server) adminChannelCreate(w http.ResponseWriter, r *http.Request, csrf string) {
	s.adminChannelSubmit(w, r, csrf, true)
}

func (s *server) adminChannelUpdate(w http.ResponseWriter, r *http.Request, csrf string) {
	if _, ok := s.adminFindChannel(w, r, csrf); ok {
		s.adminChannelSubmit(w, r, csrf, false)
	}
}

// adminChannelSubmit handles the channel form: "Add a button" and "Remove" show the form again, "Save" stores the
// channel through the same code as the API's PUT.
func (s *server) adminChannelSubmit(w http.ResponseWriter, r *http.Request, csrf string, isNew bool) {
	f := r.PostForm
	v := adminChannelValsFromForm(f)
	if !isNew {
		v.ID = r.PathValue("id")
	}
	if rm := f.Get("remove"); rm != "" {
		if i, err := strconv.Atoi(rm); err == nil && i >= 0 && i < len(v.Actions) {
			v.Actions = append(v.Actions[:i:i], v.Actions[i+1:]...)
		}
		s.adminChannelRender(w, http.StatusOK, csrf, v, isNew, nil)
		return
	}
	if f.Get("add") != "" {
		if len(v.Actions) < adminMaxActions {
			v.Actions = append(v.Actions, adminActionRow{})
		}
		s.adminChannelRender(w, http.StatusOK, csrf, v, isNew, nil)
		return
	}

	errs := map[string]string{}
	if isNew {
		if !channelIDPattern.MatchString(v.ID) {
			errs["id"] = "Use letters, numbers, dots, dashes and underscores only, starting with a letter or number, at most 64 characters."
		} else if c, err := s.Store.GetChannel(v.ID); err == nil && c.DeletedAt.IsZero() {
			errs["id"] = "A channel with this id already exists. Open it from the list to change it."
		} else if err != nil && !errors.Is(err, store.ErrNotFound) {
			s.adminChannelsError(w, csrf, err)
			return
		}
	}
	// Blank button rows are dropped; shown maps the rows that stay back to the rows on the page.
	var list []adminActionJSON
	var shown []int
	for i, a := range v.Actions {
		if a.ID == "" && a.Title == "" && a.URL == "" && a.Kind == "" && !a.Ack {
			continue
		}
		x := adminActionJSON{ID: a.ID, Title: a.Title, Kind: a.Kind, Ack: a.Ack}
		if a.Kind == "url" {
			x.URL = a.URL
		}
		list, shown = append(list, x), append(shown, i)
	}
	actions := json.RawMessage("[]")
	if len(list) > 0 {
		actions, _ = json.Marshal(list)
	}
	aud := store.Audience{Mode: v.Mode}
	if v.Mode != store.AudienceEveryone {
		extra := adminLines(v.Extra)
		aud.Users, aud.Groups = dedupe(append(append([]string{}, v.Users...), extra...)), v.Groups
		if v.Mode == store.AudienceOnly && len(aud.Users)+len(aud.Groups) == 0 {
			errs["audience"] = "Pick at least one person or group, or choose Everyone."
		}
	}
	if len(errs) == 0 {
		c, e := s.saveChannel(v.ID, channelInput{Name: v.Name, DefaultLevel: v.DefaultLevel, MaxLevel: v.MaxLevel, Sound: v.Sound,
			TemplateID: v.Template, Thread: v.Thread, Actions: actions, Audience: &aud})
		if e != nil && e.Err != nil {
			s.adminChannelsError(w, csrf, e.Err)
			return
		}
		if e != nil {
			key, msg := adminChannelFieldError(e, shown)
			errs[key] = msg
		} else {
			verb := "saved"
			if isNew {
				verb = "created"
			}
			s.log.Info("admin: channel "+verb, "id", c.ID)
			s.adminNotice(w, http.StatusOK, csrf, "Channel saved", "The channel "+c.Name+" is saved and live on the hub now.", "/admin/channels", "Back to channels")
			return
		}
	}
	v.Extra = strings.Join(adminLines(v.Extra), "\n")
	s.adminChannelRender(w, http.StatusBadRequest, csrf, v, isNew, errs)
}

type adminActionJSON struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Kind  string `json:"kind,omitempty"`
	URL   string `json:"url,omitempty"`
	Ack   bool   `json:"ack,omitempty"`
}

// adminChannelFieldError turns the API's field and message into the form's field and plain words. shown maps the
// index of a button in the list that was checked to its row on the page.
func adminChannelFieldError(e *apiError, shown []int) (key, msg string) {
	switch {
	case e.Field == "name":
		return "name", "Enter a name, at most 256 bytes long."
	case e.Field == "default_level" && strings.Contains(e.Msg, "exceed"):
		return "default_level", "The starting level cannot be higher than the highest level."
	case e.Field == "default_level", e.Field == "max_level":
		return e.Field, "Choose one of the levels."
	case e.Field == "thread":
		return "thread", "Keep the thread name to 256 bytes or fewer."
	case e.Field == "template_id":
		return "template", "That template does not exist. Choose another one, or none."
	case e.Field == "sound":
		return "sound", "That sound does not exist. Choose another one, or none."
	case strings.HasPrefix(e.Field, "audience.mode"):
		return "audience", "Choose who may use this channel."
	case strings.HasPrefix(e.Field, "audience.groups") && e.Msg == "unknown group":
		return "audience", "A group you picked does not exist any more. Untick it, or make it again on the Groups page."
	case strings.HasPrefix(e.Field, "audience."):
		return "audience", "Names must be between 1 and 256 bytes long, with at most 1000 people and 1000 groups."
	case strings.HasPrefix(e.Field, "actions["):
		rest := strings.TrimPrefix(e.Field, "actions[")
		idx, field, _ := strings.Cut(rest, "].")
		i, err := strconv.Atoi(idx)
		if err != nil || i < 0 || i >= len(shown) {
			break
		}
		why := "Check this button."
		switch field {
		case "id":
			why = "Give the button an id: a short word your software knows it by, such as ack."
		case "title":
			why = "Give the button a label people can read."
		case "url":
			why = "A link button needs a link that starts with https://"
		}
		return "a" + strconv.Itoa(shown[i]), why
	}
	return "form", "The hub refused this: " + e.Field + " " + e.Msg + "."
}

type adminChannelDelete struct{ ID, Name, Who string }

func (s *server) adminChannelDeletePage(w http.ResponseWriter, r *http.Request, csrf string) {
	if c, ok := s.adminFindChannel(w, r, csrf); ok {
		s.adminRender(w, http.StatusOK, "channel_delete", "channels", "Delete "+c.Name, csrf, adminChannelDelete{c.ID, c.Name, adminAudienceText(c.Audience)})
	}
}

func (s *server) adminChannelDeleteDo(w http.ResponseWriter, r *http.Request, csrf string) {
	id := r.PathValue("id")
	ok, err := s.Store.DeleteChannel(id, s.Now())
	switch {
	case err != nil:
		s.adminChannelsError(w, csrf, err)
	case !ok:
		s.adminNotice(w, http.StatusNotFound, csrf, "Channel not found", "There is no channel with this id.", "/admin/channels", "Back to channels")
	default:
		s.log.Info("admin: channel deleted", "id", id)
		s.adminNotice(w, http.StatusOK, csrf, "Channel deleted", "Your software can no longer send to it, and the app stops listing it.", "/admin/channels", "Back to channels")
	}
}
