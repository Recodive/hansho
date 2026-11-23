package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/recodive/hansho/hub/internal/store"
)

// adminStatusText says in words what a message's status means.
var adminStatusText = map[string]string{
	store.MsgQueued:        "Waiting to be sent to the person's devices.",
	store.MsgDelivered:     "Sent to the person's devices.",
	store.MsgUndeliverable: "No device could be reached. Check that the person is paired on the People page.",
	store.MsgPaging:        "Ringing, and repeating until someone acknowledges it.",
	store.MsgAcked:         "Acknowledged.",
	store.MsgExpired:       "Nobody acknowledged it before it ran out of repeats.",
}

// adminMessageView is what GET /api/v1/messages/{id} returns, for people.
type adminMessageView struct {
	ID, Status, StatusText, Level, LevelHelp, Acked string
	Attempts                                        int
	Deliveries                                      []deliveryView
}

func adminMessageViewOf(m store.Message, dels []deliveryView) adminMessageView {
	v := adminMessageView{ID: m.ID, Status: m.Status, StatusText: adminStatusText[m.Status], Level: adminLevelLabel(m.Level),
		LevelHelp: adminLevelHelp(m.Level), Attempts: m.Attempts, Deliveries: dels}
	if !m.AckedAt.IsZero() {
		v.Acked = adminTime(m.AckedAt)
	}
	return v
}

type adminSendStart struct {
	People []adminOption
	Other  string
	Error  string
}

type adminSendForm struct {
	Person                               string
	Devices                              int
	Channels, Levels                     []adminOption
	Title, Body, Data, Interval, Retries string
	Errors                               map[string]string
}

type adminSendDone struct {
	adminMessageView
	Person, Channel, Lowered string
	Devices                  int
}

func (s *server) adminSendError(w http.ResponseWriter, csrf string, err error) {
	s.log.Error("admin: test message failed", "err", lc(err.Error()))
	s.adminNotice(w, http.StatusInternalServerError, csrf, "Something went wrong", "The hub could not do that. Try again.", "/admin/send", "Back")
}

// adminSendStartPage asks who gets the message.
func (s *server) adminSendStartPage(w http.ResponseWriter, code int, csrf, other, msg string) {
	people, err := s.adminPersons()
	if err != nil {
		s.adminSendError(w, csrf, err)
		return
	}
	d := adminSendStart{People: []adminOption{{Value: "", Label: "Choose a person", Selected: true}}, Other: other, Error: msg}
	for _, p := range people {
		d.People = append(d.People, adminOption{Value: p.Name, Label: p.Name})
	}
	s.adminRender(w, code, "send_who", "send", "Send a test message", csrf, d)
}

// adminSend is step one (who gets it) until the query names a person, then step two (what to send). Both are plain page
// loads, so the channels offered are the ones that person may use without any script.
func (s *server) adminSend(w http.ResponseWriter, r *http.Request, csrf string) {
	q := r.URL.Query()
	if !q.Has("who") && !q.Has("other") {
		s.adminSendStartPage(w, http.StatusOK, csrf, "", "")
		return
	}
	other := strings.TrimSpace(q.Get("other"))
	name := other
	if name == "" {
		name = q.Get("who")
	}
	if !validExternalUserID(name) {
		s.adminSendStartPage(w, http.StatusBadRequest, csrf, other, "Choose a person, or type a name of at most 256 bytes.")
		return
	}
	s.adminSendRender(w, http.StatusOK, csrf, name, "", adminSendForm{}, nil)
}

// adminSendRender shows step two for person. v holds what was typed; channel is the chosen channel and level the chosen
// level ("" is the channel's usual).
func (s *server) adminSendRender(w http.ResponseWriter, code int, csrf, person, channel string, v adminSendForm, errs map[string]string) {
	devs, err := s.Store.ActiveDevicesForUser(person)
	if err != nil {
		s.adminSendError(w, csrf, err)
		return
	}
	channels, err := s.Store.ListChannels()
	if err != nil {
		s.adminSendError(w, csrf, err)
		return
	}
	acc, err := s.Store.AccessFor(person)
	if err != nil {
		s.adminSendError(w, csrf, err)
		return
	}
	v.Person, v.Devices, v.Errors = person, len(devs), errs
	if v.Levels == nil {
		v.Levels = adminLevelOptions("", "The channel's usual level")
	}
	for _, c := range channels {
		if acc.Allows(c.Audience) {
			v.Channels = append(v.Channels, adminOption{Value: c.ID, Label: c.Name + " (" + c.ID + "), up to " + adminLevelLabel(c.MaxLevel), Selected: c.ID == channel})
		}
	}
	s.adminRender(w, code, "send_form", "send", "Send a test message", csrf, v)
}

// adminSendPost sends through the same code as POST /api/v1/messages.
func (s *server) adminSendPost(w http.ResponseWriter, r *http.Request, csrf string) {
	f := r.PostForm
	person, channel, lvl := f.Get("person"), f.Get("channel"), f.Get("level")
	if !validExternalUserID(person) {
		s.adminSendStartPage(w, http.StatusBadRequest, csrf, "", "Choose a person first.")
		return
	}
	v := adminSendForm{Title: strings.TrimSpace(f.Get("title")), Body: strings.TrimSpace(f.Get("body")), Data: strings.TrimSpace(f.Get("data")),
		Interval: strings.TrimSpace(f.Get("interval")), Retries: strings.TrimSpace(f.Get("retries")), Levels: adminLevelOptions(lvl, "The channel's usual level")}
	errs := map[string]string{}
	req := sendRequest{ExternalUserID: person, Channel: channel, Title: v.Title, Body: v.Body}
	if lvl != "" {
		req.Level = &lvl
	}
	if v.Data != "" {
		if !json.Valid([]byte(v.Data)) || v.Data[0] != '{' {
			errs["data"] = "This must be a JSON object such as {\"name\": \"Ada\"}, or empty."
		} else {
			req.Data = json.RawMessage(v.Data)
		}
	}
	page := sendPage{}
	for _, n := range []struct {
		key, text string
		dst       **int
	}{{"interval", v.Interval, &page.IntervalS}, {"retries", v.Retries, &page.MaxRetries}} {
		if n.text == "" {
			continue
		}
		i, err := strconv.Atoi(n.text)
		if err != nil {
			errs[n.key] = "Enter a whole number, or leave this empty."
			continue
		}
		*n.dst = &i
	}
	req.Page = &page

	var done *adminSendDone
	if len(errs) == 0 {
		var recipients []recipient
		var sent string
		var e *apiError
		sent, recipients, e = s.send(r.Context(), req)
		switch {
		case e != nil && e.Err != nil:
			s.adminSendError(w, csrf, e.Err)
			return
		case e != nil:
			key, msg := adminSendFieldError(e)
			errs[key] = msg
		case recipients[0].MessageID == "":
			errs["channel"] = person + " may not use this channel. Choose another one, or change who may use it on the Channels page."
		default:
			done = &adminSendDone{Person: person, Channel: channel, Devices: len(recipients[0].Deliveries)}
			m, dels, e := s.loadMessage(recipients[0].MessageID)
			if e != nil {
				if e.Err == nil {
					e.Err = errors.New(e.Code)
				}
				s.adminSendError(w, csrf, e.Err)
				return
			}
			done.adminMessageView = adminMessageViewOf(m, dels)
			if lvl != "" && lvl != sent {
				done.Lowered = "You asked for " + adminLevelLabel(lvl) + ", but this channel never goes above " + adminLevelLabel(sent) + ", so it was sent as " + adminLevelLabel(sent) + "."
			}
			s.log.Info("admin: test message sent", "message_id", lc(m.ID), "channel", lc(channel), "external_user_id", lc(person), "level", lc(fmt.Sprint(sent)), "status", lc(fmt.Sprint(m.Status)))
		}
	}
	if done != nil {
		s.adminRender(w, http.StatusOK, "send_done", "send", "Message sent", csrf, done)
		return
	}
	s.adminSendRender(w, http.StatusBadRequest, csrf, person, channel, v, errs)
}

// adminSendFieldError turns the API's field and message into the form's field and plain words.
func adminSendFieldError(e *apiError) (key, msg string) {
	switch e.Field {
	case "title":
		return "title", "Enter a title."
	case "body":
		return "body", "Enter the message text."
	case "channel":
		return "channel", "Choose a channel."
	case "level":
		return "level", "Choose one of the levels."
	case "page.interval_s":
		return "interval", "Enter a number of seconds from 1 to 86400, or leave this empty."
	case "page.max_retries":
		return "retries", "Enter a number from 0 to 100, or leave this empty."
	}
	switch e.Code {
	case "unknown_channel":
		return "channel", "This channel does not exist any more. Choose another one."
	case "payload_too_large":
		return "body", "The message is too big to send. Shorten it."
	}
	return "form", "The hub refused this: " + e.Field + " " + e.Msg + "."
}

// adminMessage is the status of a message: what GET /api/v1/messages/{id} returns, with a button to look again.
func (s *server) adminMessage(w http.ResponseWriter, r *http.Request, csrf string) {
	m, dels, e := s.loadMessage(r.PathValue("id"))
	switch {
	case e != nil && e.Err != nil:
		s.adminSendError(w, csrf, e.Err)
	case e != nil:
		s.adminNotice(w, http.StatusNotFound, csrf, "Message not found", "There is no message with this id.", "/admin/send", "Send a test message")
	default:
		s.adminRender(w, http.StatusOK, "message", "send", "Message status", csrf, adminMessageViewOf(m, dels))
	}
}
