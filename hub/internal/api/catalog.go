package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"mime"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"time"

	"github.com/recodive/hansho/hub/internal/level"
	"github.com/recodive/hansho/hub/internal/store"
	"github.com/recodive/hansho/hub/internal/template"
)

const maxSoundBytes = 1 << 20

var idPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)

// channelID additionally allows dots, e.g. "billing.alerts".
var channelIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

type channelView struct {
	ID           string          `json:"id"`
	Name         string          `json:"name"`
	DefaultLevel string          `json:"default_level"`
	MaxLevel     string          `json:"max_level"`
	Sound        string          `json:"sound"`
	TemplateID   string          `json:"template_id"`
	Thread       string          `json:"thread"`
	Actions      json.RawMessage `json:"actions"`
}

func viewOf(c store.Channel) channelView {
	return channelView{c.ID, c.Name, c.DefaultLevel, c.MaxLevel, c.Sound, c.TemplateID, c.Thread, json.RawMessage(c.ActionsJSON)}
}

// senderChannelView is what the sender API returns. Devices never get the audience: it names other people.
type senderChannelView struct {
	channelView
	Audience store.Audience `json:"audience"`
}

func senderViewOf(c store.Channel) senderChannelView {
	return senderChannelView{viewOf(c), c.Audience.Normalized()}
}

// channelInput is the body of PUT /api/v1/channels/{id}, and what the admin UI's channel form fills in.
type channelInput struct {
	Name         string          `json:"name"`
	DefaultLevel string          `json:"default_level"`
	MaxLevel     string          `json:"max_level"`
	Sound        string          `json:"sound"`
	TemplateID   string          `json:"template_id"`
	Thread       string          `json:"thread"`
	Actions      json.RawMessage `json:"actions"`
	Audience     *store.Audience `json:"audience"`
}

// PUT /api/v1/channels/{id}
func (s *server) putChannel(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !channelIDPattern.MatchString(id) {
		writeFieldErr(w, http.StatusBadRequest, "invalid_request", "id", "must match [A-Za-z0-9][A-Za-z0-9._-]{0,63}")
		return
	}
	body, ok := readBody(w, r, maxJSONBody)
	if !ok {
		return
	}
	var req channelInput
	if !decodeJSON(w, body, &req) {
		return
	}
	c, e := s.saveChannel(id, req)
	if e != nil {
		s.fail(w, e)
		return
	}
	writeJSON(w, http.StatusOK, senderViewOf(c))
}

// saveChannel validates a channel and stores it (creating or replacing it, and undeleting it). The caller has checked
// that id matches channelIDPattern. It is the one place the API and the admin UI share the rules.
func (s *server) saveChannel(id string, req channelInput) (store.Channel, *apiError) {
	switch {
	case req.Name == "" || len(req.Name) > maxFieldLen:
		return store.Channel{}, badField("name", "required, at most 256 bytes")
	case !level.Valid(req.DefaultLevel):
		return store.Channel{}, badField("default_level", "must be one of passive, active, time-sensitive, critical, page")
	case !level.Valid(req.MaxLevel):
		return store.Channel{}, badField("max_level", "must be one of passive, active, time-sensitive, critical, page")
	case level.Rank(req.DefaultLevel) > level.Rank(req.MaxLevel):
		return store.Channel{}, badField("default_level", "must not exceed max_level")
	case len(req.Thread) > maxFieldLen:
		return store.Channel{}, badField("thread", "at most 256 bytes")
	}
	if req.TemplateID != "" {
		if _, err := s.Store.GetTemplate(req.TemplateID); errors.Is(err, store.ErrNotFound) {
			return store.Channel{}, badField("template_id", "unknown template")
		} else if err != nil {
			return store.Channel{}, internalErr("template lookup failed", err)
		}
	}
	if req.Sound != "" {
		if _, err := s.Store.GetSound(req.Sound); errors.Is(err, store.ErrNotFound) {
			return store.Channel{}, badField("sound", "unknown sound")
		} else if err != nil {
			return store.Channel{}, internalErr("sound lookup failed", err)
		}
	}
	actions, field, msg := checkActions(req.Actions)
	if field != "" {
		return store.Channel{}, badField(field, msg)
	}
	thread := req.Thread
	if thread == "" {
		thread = id
	}
	// Leaving the audience out keeps the stored one, so a republish from a client that does not know audiences never
	// widens access; send {"mode":"everyone"} to open a channel up again.
	var audience store.Audience
	if req.Audience == nil {
		if old, err := s.Store.GetChannel(id); err == nil {
			audience = old.Audience
		}
	} else {
		var field, msg string
		if audience, field, msg = checkAudience(*req.Audience, "audience"); field != "" {
			return store.Channel{}, badField(field, msg)
		}
		field, msg, err := s.checkGroupsExist(audience, "audience")
		if err != nil {
			return store.Channel{}, internalErr("group lookup failed", err)
		}
		if field != "" {
			return store.Channel{}, badField(field, msg)
		}
	}
	c := store.Channel{ID: id, Name: req.Name, DefaultLevel: req.DefaultLevel, MaxLevel: req.MaxLevel,
		Sound: req.Sound, TemplateID: req.TemplateID, Thread: thread, ActionsJSON: actions, Audience: audience.Normalized()}
	if err := s.Store.UpsertChannel(c); err != nil {
		return store.Channel{}, internalErr("upsert channel failed", err)
	}
	return c, nil
}

// GET /api/v1/channels/{id}
func (s *server) getChannel(w http.ResponseWriter, r *http.Request) {
	c, err := s.Store.GetChannel(r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) || (err == nil && !c.DeletedAt.IsZero()) {
		writeErr(w, http.StatusNotFound, "unknown_channel")
		return
	}
	if err != nil {
		s.internal(w, "channel lookup failed", err)
		return
	}
	writeJSON(w, http.StatusOK, senderViewOf(c))
}

// GET /api/v1/channels
func (s *server) listChannels(w http.ResponseWriter, _ *http.Request) {
	channels, err := s.Store.ListChannels()
	if err != nil {
		s.internal(w, "list channels failed", err)
		return
	}
	out := make([]senderChannelView, 0, len(channels))
	for _, c := range channels {
		out = append(out, senderViewOf(c))
	}
	writeJSON(w, http.StatusOK, out)
}

// DELETE /api/v1/channels/{id}
func (s *server) deleteChannel(w http.ResponseWriter, r *http.Request) {
	ok, err := s.Store.DeleteChannel(r.PathValue("id"), s.Now())
	if err != nil {
		s.internal(w, "delete channel failed", err)
		return
	}
	if !ok {
		writeErr(w, http.StatusNotFound, "unknown_channel")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// PUT /api/v1/templates/{id}
func (s *server) putTemplate(w http.ResponseWriter, r *http.Request) {
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
		Version int             `json:"version"`
		Body    json.RawMessage `json:"body"`
	}
	if !decodeJSON(w, body, &req) {
		return
	}
	if e := s.saveTemplate(id, req.Version, req.Body); e != nil {
		s.fail(w, e)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "version": req.Version})
}

// checkTemplate validates a template body with the hub's validator. A failure names the first problem, as
// "body.<path>" and the reason.
func checkTemplate(body json.RawMessage) *apiError {
	err := template.Validate(body)
	if err == nil {
		return nil
	}
	var fe *template.FieldError
	field := "body"
	msg := err.Error()
	if errors.As(err, &fe) {
		if fe.Path != "" {
			field = "body." + fe.Path
		}
		msg = fe.Msg
	}
	return &apiError{Status: http.StatusBadRequest, Code: "invalid_template", Field: field, Msg: msg}
}

// saveTemplate validates a template body and stores it as version. The caller has checked that id matches idPattern.
// It is the one place the API and the admin UI share the rules, including "the version must increase".
func (s *server) saveTemplate(id string, version int, body json.RawMessage) *apiError {
	if version < 1 {
		return badField("version", "must be a positive integer")
	}
	if len(body) == 0 {
		return badField("body", "required")
	}
	if e := checkTemplate(body); e != nil {
		return e
	}
	err := s.Store.PutTemplate(id, version, body)
	if errors.Is(err, store.ErrVersionConflict) {
		return &apiError{Status: http.StatusConflict, Code: "version_conflict"}
	}
	if err != nil {
		return internalErr("put template failed", err)
	}
	return nil
}

type templateView struct {
	ID      string          `json:"id"`
	Version int             `json:"version"`
	Body    json.RawMessage `json:"body"`
}

// GET /api/v1/templates
func (s *server) listTemplates(w http.ResponseWriter, _ *http.Request) {
	templates, err := s.Store.ListTemplates()
	if err != nil {
		s.internal(w, "list templates failed", err)
		return
	}
	out := make([]templateView, 0, len(templates))
	for _, t := range templates {
		out = append(out, templateView{t.ID, t.Version, t.Body})
	}
	writeJSON(w, http.StatusOK, out)
}

// GET /api/v1/templates/{id}
func (s *server) getTemplate(w http.ResponseWriter, r *http.Request) {
	t, err := s.Store.GetTemplate(r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		writeErr(w, http.StatusNotFound, "unknown_template")
		return
	}
	if err != nil {
		s.internal(w, "template lookup failed", err)
		return
	}
	writeJSON(w, http.StatusOK, templateView{t.ID, t.Version, t.Body})
}

// DELETE /api/v1/templates/{id}
//
// A template a live channel still names is refused with 409: deleting it would leave that channel pointing at nothing.
func (s *server) deleteTemplate(w http.ResponseWriter, r *http.Request) {
	s.deleteCatalog(w, r, "unknown_template", "delete template failed", s.Store.DeleteTemplate)
}

// deleteCatalog answers a template or sound deletion.
func (s *server) deleteCatalog(w http.ResponseWriter, r *http.Request, unknown, what string, del func(string) (bool, error)) {
	ok, err := del(r.PathValue("id"))
	switch {
	case errors.Is(err, store.ErrInUse):
		writeErr(w, http.StatusConflict, "in_use")
	case err != nil:
		s.internal(w, what, err)
	case !ok:
		writeErr(w, http.StatusNotFound, unknown)
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

type soundView struct {
	ID          string `json:"id"`
	ContentType string `json:"content_type"`
	Bytes       int    `json:"bytes"`
	SHA256      string `json:"sha256"`
}

// GET /api/v1/sounds
func (s *server) listSounds(w http.ResponseWriter, _ *http.Request) {
	sounds, err := s.Store.ListSounds()
	if err != nil {
		s.internal(w, "list sounds failed", err)
		return
	}
	out := make([]soundView, 0, len(sounds))
	for _, x := range sounds {
		out = append(out, soundView{x.ID, x.ContentType, x.Size, x.SHA256})
	}
	writeJSON(w, http.StatusOK, out)
}

// GET /api/v1/sounds/{id}: metadata only; devices download the audio from the device API.
func (s *server) getSound(w http.ResponseWriter, r *http.Request) {
	x, err := s.Store.GetSound(r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		writeErr(w, http.StatusNotFound, "unknown_sound")
		return
	}
	if err != nil {
		s.internal(w, "sound lookup failed", err)
		return
	}
	writeJSON(w, http.StatusOK, soundView{x.ID, x.ContentType, x.Size, x.SHA256})
}

// DELETE /api/v1/sounds/{id}; refused with 409 while a live channel names the sound.
func (s *server) deleteSound(w http.ResponseWriter, r *http.Request) {
	s.deleteCatalog(w, r, "unknown_sound", "delete sound failed", s.Store.DeleteSound)
}

var soundTypes = map[string]string{"audio/x-caf": ".caf", "audio/wav": ".wav", "audio/aiff": ".aiff"}

// PUT /api/v1/sounds/{id}
func (s *server) putSound(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !idPattern.MatchString(id) {
		writeFieldErr(w, http.StatusBadRequest, "invalid_request", "id", "must match [A-Za-z0-9][A-Za-z0-9_-]{0,63}")
		return
	}
	ct, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if _, supported := soundTypes[ct]; err != nil || !supported {
		writeErr(w, http.StatusUnsupportedMediaType, "unsupported_media_type")
		return
	}
	data, ok := readBody(w, r, maxSoundBytes)
	if !ok {
		return
	}
	snd, e := s.saveSound(id, ct, data)
	if e != nil {
		s.fail(w, e)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "filename": snd.Filename, "sha256": snd.SHA256, "size": len(data)})
}

// saveSound checks a sound's type and audio and stores it. ct is the media type, without parameters. The caller has
// checked that id matches idPattern. It is the one place the API and the admin UI share the rules.
func (s *server) saveSound(id, ct string, data []byte) (store.Sound, *apiError) {
	ext, supported := soundTypes[ct]
	switch {
	case !supported:
		return store.Sound{}, &apiError{Status: http.StatusUnsupportedMediaType, Code: "unsupported_media_type"}
	case len(data) > maxSoundBytes:
		return store.Sound{}, &apiError{Status: http.StatusRequestEntityTooLarge, Code: "body_too_large"}
	}
	var d time.Duration
	var err error
	switch ct {
	case "audio/wav":
		d, err = wavDuration(data)
	case "audio/aiff":
		d, err = aiffDuration(data)
	case "audio/x-caf": // its duration is not checked
		if !isCAF(data) {
			err = errBadAudio
		}
	}
	if err != nil {
		return store.Sound{}, &apiError{Status: http.StatusBadRequest, Code: "invalid_audio"}
	}
	if d >= maxSoundDuration {
		return store.Sound{}, &apiError{Status: http.StatusBadRequest, Code: "sound_too_long"}
	}
	sum := sha256.Sum256(data)
	snd := store.Sound{ID: id, Filename: id + ext, ContentType: ct, Data: data, SHA256: hex.EncodeToString(sum[:])}
	if err := s.Store.PutSound(snd); err != nil {
		return store.Sound{}, internalErr("put sound failed", err)
	}
	return snd, nil
}

// GET /device/v1/sync
//
// Channels the device's user cannot access are left out, not marked: the app learns nothing about them.
// profileView is the user's display name and whether the person may change it in the app.
type profileView struct {
	DisplayName string `json:"display_name"`
	Editable    bool   `json:"editable"`
}

func (s *server) sync(w http.ResponseWriter, _ *http.Request, dev store.DeviceWithPairing, _ []byte) {
	channels, err := s.Store.ListChannels()
	if err != nil {
		s.internal(w, "list channels failed", err)
		return
	}
	acc, err := s.Store.AccessFor(dev.Pairing.ExternalUserID)
	if err != nil {
		s.internal(w, "access check failed", err)
		return
	}
	templates, err := s.Store.ListTemplates()
	if err != nil {
		s.internal(w, "list templates failed", err)
		return
	}
	sounds, err := s.Store.ListSounds()
	if err != nil {
		s.internal(w, "list sounds failed", err)
		return
	}
	type tpl struct {
		ID      string          `json:"id"`
		Version int             `json:"version"`
		Body    json.RawMessage `json:"body"`
	}
	type snd struct {
		ID     string `json:"id"`
		URL    string `json:"url"`
		SHA256 string `json:"sha256"`
	}
	out := struct {
		Hub       map[string]string `json:"hub"`
		Channels  []channelView     `json:"channels"`
		Templates []tpl             `json:"templates"`
		Sounds    []snd             `json:"sounds"`
		Profile   profileView       `json:"profile"`
	}{Hub: s.hubInfo(), Channels: []channelView{}, Templates: []tpl{}, Sounds: []snd{}}
	if out.Profile.DisplayName, err = s.Store.DisplayName(dev.Pairing.ExternalUserID); err != nil {
		s.internal(w, "profile lookup failed", err)
		return
	}
	out.Profile.Editable = s.Cfg.AllowDisplayNameEdit
	for _, c := range channels {
		if acc.Allows(c.Audience) {
			out.Channels = append(out.Channels, viewOf(c))
		}
	}
	for _, t := range templates {
		out.Templates = append(out.Templates, tpl{t.ID, t.Version, t.Body})
	}
	for _, x := range sounds {
		out.Sounds = append(out.Sounds, snd{x.ID, s.Cfg.PublicURL + "/device/v1/sounds/" + x.ID, x.SHA256})
	}
	writeJSON(w, http.StatusOK, out)
}

// GET /device/v1/sounds/{id}
func (s *server) downloadSound(w http.ResponseWriter, r *http.Request, _ store.DeviceWithPairing, _ []byte) {
	snd, err := s.Store.GetSound(r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		writeErr(w, http.StatusNotFound, "unknown_sound")
		return
	}
	if err != nil {
		s.internal(w, "sound lookup failed", err)
		return
	}
	w.Header().Set("Content-Type", snd.ContentType)
	w.Header().Set("Content-Length", strconv.Itoa(len(snd.Data)))
	w.Header().Set("ETag", `"`+snd.SHA256+`"`)
	w.Write(snd.Data)
}

// isHTTPSURL reports whether s is an https URL with a host.
func isHTTPSURL(s string) bool {
	u, err := url.Parse(s)
	return err == nil && u.Scheme == "https" && u.Hostname() != ""
}

// checkActions validates an actions array (each entry needs string id and
// title; ack, if present, is a boolean; a "url" action needs an https url). It returns the JSON to store, or the
// failing field path and a message.
func checkActions(raw json.RawMessage) (norm, field, msg string) {
	if len(raw) == 0 || string(raw) == "null" {
		return "[]", "", ""
	}
	var list []map[string]any
	if err := json.Unmarshal(raw, &list); err != nil {
		return "", "actions", "must be an array of objects"
	}
	for i, a := range list {
		for _, f := range []string{"id", "title"} {
			if v, ok := a[f].(string); !ok || v == "" {
				return "", "actions[" + strconv.Itoa(i) + "]." + f, "required string"
			}
		}
		if v, present := a["ack"]; present {
			if _, ok := v.(bool); !ok {
				return "", "actions[" + strconv.Itoa(i) + "].ack", "must be a boolean"
			}
		}
		if a["kind"] == "url" {
			if u, ok := a["url"].(string); !ok || !isHTTPSURL(u) {
				return "", "actions[" + strconv.Itoa(i) + "].url", "must be an https URL with a host"
			}
		}
	}
	return string(raw), "", ""
}
