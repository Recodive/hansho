package api

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"
	"strings"

	"github.com/recodive/hansho/hub/internal/store"
)

var adminSoundNames = map[string]string{"audio/x-caf": "CAF", "audio/wav": "WAV", "audio/aiff": "AIFF"}

// adminSoundType says what type an uploaded file is. The file's own first bytes decide; a file that is none of the
// three is judged by its name, so the shared checks then say what is wrong with it. "" when it is neither.
func adminSoundType(name string, data []byte) string {
	switch {
	case isCAF(data):
		return "audio/x-caf"
	case len(data) >= 12 && string(data[0:4]) == "RIFF" && string(data[8:12]) == "WAVE":
		return "audio/wav"
	case len(data) >= 12 && string(data[0:4]) == "FORM" && (string(data[8:12]) == "AIFF" || string(data[8:12]) == "AIFC"):
		return "audio/aiff"
	}
	switch strings.ToLower(path.Ext(name)) {
	case ".caf":
		return "audio/x-caf"
	case ".wav":
		return "audio/wav"
	case ".aif", ".aiff", ".aifc":
		return "audio/aiff"
	}
	return ""
}

// adminSize writes a byte count for people.
func adminSize(n int) string {
	if n < 1024 {
		return fmt.Sprintf("%d bytes", n)
	}
	return fmt.Sprintf("%.1f KB", float64(n)/1024)
}

type adminSoundRow struct {
	ID, Type, Size, Short string
	Uses                  int
}

type adminSoundsPage struct {
	Rows   []adminSoundRow
	ID     string // what the upload form shows again after an error
	Errors map[string]string
}

func (s *server) adminSoundsError(w http.ResponseWriter, csrf string, err error) {
	s.log.Error("admin: sounds failed", "err", err)
	s.adminNotice(w, http.StatusInternalServerError, csrf, "Something went wrong", "The hub could not do that. Try again.", "/admin/sounds", "Back to sounds")
}

func (s *server) adminSoundsRender(w http.ResponseWriter, code int, csrf, id string, errs map[string]string) {
	sounds, err := s.Store.ListSounds()
	if err != nil {
		s.adminSoundsError(w, csrf, err)
		return
	}
	channels, err := s.Store.ListChannels()
	if err != nil {
		s.adminSoundsError(w, csrf, err)
		return
	}
	uses := map[string]int{}
	for _, c := range channels {
		uses[c.Sound]++
	}
	p := adminSoundsPage{ID: id, Errors: errs}
	for _, x := range sounds {
		short := x.SHA256
		if len(short) > 12 {
			short = short[:12]
		}
		kind := adminSoundNames[x.ContentType]
		if kind == "" {
			kind = x.ContentType
		}
		p.Rows = append(p.Rows, adminSoundRow{ID: x.ID, Type: kind, Size: adminSize(x.Size), Short: short, Uses: uses[x.ID]})
	}
	s.adminRender(w, code, "sounds", "sounds", "Sounds", csrf, p)
}

func (s *server) adminSounds(w http.ResponseWriter, _ *http.Request, csrf string) {
	s.adminSoundsRender(w, http.StatusOK, csrf, "", nil)
}

// adminSoundUpload stores an uploaded sound through the same code as the API's PUT.
func (s *server) adminSoundUpload(w http.ResponseWriter, r *http.Request, csrf string) {
	id := strings.TrimSpace(r.PostForm.Get("id"))
	errs := map[string]string{}
	if !idPattern.MatchString(id) {
		errs["id"] = "Use letters, numbers, dashes and underscores only, starting with a letter or number, at most 64 characters."
	}
	var data []byte
	var name string
	if r.MultipartForm != nil && len(r.MultipartForm.File["file"]) > 0 {
		fh := r.MultipartForm.File["file"][0]
		name = fh.Filename
		f, err := fh.Open()
		if err == nil {
			data, err = io.ReadAll(io.LimitReader(f, maxSoundBytes+1))
			f.Close()
		}
		if err != nil {
			s.adminSoundsError(w, csrf, err)
			return
		}
	}
	if len(data) == 0 {
		errs["file"] = "Choose a sound file."
	}
	ct := adminSoundType(name, data)
	if len(errs) == 0 {
		snd, e := s.saveSound(id, ct, data)
		switch {
		case e == nil:
			s.log.Info("admin: sound saved", "id", id, "type", ct, "bytes", len(data))
			s.adminNotice(w, http.StatusOK, csrf, "Sound saved", "The sound "+snd.ID+" is saved. You can choose it for a channel now. Apps download it the next time they sync.", "/admin/sounds", "Back to sounds")
			return
		case e.Err != nil:
			s.adminSoundsError(w, csrf, e.Err)
			return
		case e.Code == "unsupported_media_type":
			errs["file"] = "This is not a CAF, WAV or AIFF file. Convert it first, for example in a sound editor."
		case e.Code == "body_too_large":
			errs["file"] = "This file is larger than 1 MB. Choose a smaller one."
		case e.Code == "sound_too_long":
			errs["file"] = "This sound is 30 seconds or longer. A notification sound must be shorter than 30 seconds."
		default:
			errs["file"] = "This does not look like a valid " + adminSoundNames[ct] + " file. Check that it is not damaged and try again."
		}
	}
	s.adminSoundsRender(w, http.StatusBadRequest, csrf, id, errs)
}

type adminSoundDelete struct {
	ID      string
	UsedBy  []adminUse
	Refused bool
}

func (s *server) adminSoundDeletePage(w http.ResponseWriter, r *http.Request, csrf string) {
	snd, err := s.Store.GetSound(r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		s.adminNotice(w, http.StatusNotFound, csrf, "Sound not found", "There is no sound with this id.", "/admin/sounds", "Back to sounds")
		return
	}
	if err != nil {
		s.adminSoundsError(w, csrf, err)
		return
	}
	uses, err := s.adminUses(snd.ID, channelSound)
	if err != nil {
		s.adminSoundsError(w, csrf, err)
		return
	}
	s.adminRender(w, http.StatusOK, "sound_delete", "sounds", "Delete "+snd.ID, csrf, adminSoundDelete{ID: snd.ID, UsedBy: uses})
}

// adminSoundDeleteDo deletes the sound through the store, which refuses while a live channel names it (the API's 409).
func (s *server) adminSoundDeleteDo(w http.ResponseWriter, r *http.Request, csrf string) {
	id := r.PathValue("id")
	ok, err := s.Store.DeleteSound(id)
	switch {
	case errors.Is(err, store.ErrInUse):
		uses, err := s.adminUses(id, channelSound)
		if err != nil {
			s.adminSoundsError(w, csrf, err)
			return
		}
		s.adminRender(w, http.StatusConflict, "sound_delete", "sounds", "Delete "+id, csrf, adminSoundDelete{ID: id, UsedBy: uses, Refused: true})
	case err != nil:
		s.adminSoundsError(w, csrf, err)
	case !ok:
		s.adminNotice(w, http.StatusNotFound, csrf, "Sound not found", "There is no sound with this id.", "/admin/sounds", "Back to sounds")
	default:
		s.log.Info("admin: sound deleted", "id", id)
		s.adminNotice(w, http.StatusOK, csrf, "Sound deleted", "The sound is gone. You can upload one with the same id again later.", "/admin/sounds", "Back to sounds")
	}
}
