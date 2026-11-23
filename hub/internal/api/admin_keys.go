package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/recodive/hansho/hub/internal/store"
)

type adminAPIKey struct {
	ID            int64
	Name, Created string
	Revoked       string // "" while the key works
}

type adminKeysPage struct {
	Keys  []adminAPIKey
	Name  string // what the create form shows again after an error
	Error string
}

type adminKeyNew struct{ Name, Key string }

func toAdminKey(k store.APIKey) adminAPIKey {
	out := adminAPIKey{ID: k.ID, Name: k.Name, Created: adminTime(k.CreatedAt)}
	if !k.RevokedAt.IsZero() {
		out.Revoked = adminTime(k.RevokedAt)
	}
	return out
}

func (s *server) adminKeysList() ([]adminAPIKey, error) {
	keys, err := s.Store.ListAPIKeys()
	if err != nil {
		return nil, err
	}
	out := make([]adminAPIKey, len(keys))
	for i, k := range keys {
		out[i] = toAdminKey(k)
	}
	return out, nil
}

func (s *server) adminKeysError(w http.ResponseWriter, csrf string, err error) {
	s.log.Error("admin: api keys failed", "err", err)
	s.adminNotice(w, http.StatusInternalServerError, csrf, "Something went wrong", "The hub could not do that. Try again.", "/admin/keys", "Back to API keys")
}

func (s *server) adminKeys(w http.ResponseWriter, _ *http.Request, csrf string) {
	keys, err := s.adminKeysList()
	if err != nil {
		s.adminKeysError(w, csrf, err)
		return
	}
	s.adminRender(w, http.StatusOK, "keys", "keys", "API keys", csrf, adminKeysPage{Keys: keys})
}

// adminCreateKey mints a key and shows it in the POST's own response: only its hash is stored, so it cannot be shown
// again.
func (s *server) adminCreateKey(w http.ResponseWriter, r *http.Request, csrf string) {
	name := strings.TrimSpace(r.PostForm.Get("name"))
	if name == "" || len(name) > maxFieldLen {
		keys, err := s.adminKeysList()
		if err != nil {
			s.adminKeysError(w, csrf, err)
			return
		}
		s.adminRender(w, http.StatusBadRequest, "keys", "keys", "API keys", csrf, adminKeysPage{
			Keys: keys, Name: name, Error: "Enter a name that tells you what the key is for, at most 256 bytes long.",
		})
		return
	}
	key, err := s.Store.CreateAPIKey(name)
	if err != nil {
		s.adminKeysError(w, csrf, err)
		return
	}
	// The id is only for the log line; the key itself is never logged.
	k, err := s.Store.LookupAPIKey(key)
	if err != nil {
		s.adminKeysError(w, csrf, err)
		return
	}
	s.adminInfo("admin: api key created", "id", k.ID, "name", name)
	s.adminRender(w, http.StatusOK, "key_new", "keys", "New API key", csrf, adminKeyNew{Name: name, Key: key})
}

func (s *server) adminFindKey(r *http.Request) (adminAPIKey, bool, error) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		return adminAPIKey{}, false, nil
	}
	keys, err := s.Store.ListAPIKeys()
	if err != nil {
		return adminAPIKey{}, false, err
	}
	for _, k := range keys {
		if k.ID == id {
			return toAdminKey(k), true, nil
		}
	}
	return adminAPIKey{}, false, nil
}

// adminRevokePage asks before revoking a key.
func (s *server) adminRevokePage(w http.ResponseWriter, r *http.Request, csrf string) {
	k, ok, err := s.adminFindKey(r)
	switch {
	case err != nil:
		s.adminKeysError(w, csrf, err)
	case !ok:
		s.adminNotice(w, http.StatusNotFound, csrf, "Key not found", "There is no API key with this number.", "/admin/keys", "Back to API keys")
	case k.Revoked != "":
		s.adminNotice(w, http.StatusOK, csrf, "Already revoked", "The key "+k.Name+" was revoked on "+k.Revoked+".", "/admin/keys", "Back to API keys")
	default:
		s.adminRender(w, http.StatusOK, "key_revoke", "keys", "Revoke "+k.Name, csrf, k)
	}
}

func (s *server) adminRevoke(w http.ResponseWriter, r *http.Request, csrf string) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err == nil {
		err = s.Store.RevokeAPIKey(id)
	}
	switch {
	case errors.Is(err, store.ErrNotFound), errors.Is(err, strconv.ErrSyntax), errors.Is(err, strconv.ErrRange):
		s.adminNotice(w, http.StatusNotFound, csrf, "Key not found", "There is no API key with this number.", "/admin/keys", "Back to API keys")
	case err != nil:
		s.adminKeysError(w, csrf, err)
	default:
		s.adminInfo("admin: api key revoked", "id", id)
		s.adminNotice(w, http.StatusOK, csrf, "Key revoked", "The key no longer works. Anything still using it will be refused.", "/admin/keys", "Back to API keys")
	}
}
