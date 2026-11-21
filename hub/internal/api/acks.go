package api

import (
	"net/http"
	"strings"

	"github.com/recodive/hansho/hub/internal/store"
)

// maxAckIDs bounds one GET /device/v1/acks lookup.
const maxAckIDs = 100

// GET /device/v1/acks?ids=<comma separated message ids, at most 100>
//
// The app asks which of its pages were acknowledged elsewhere (the cancel push can be missed). The answer is
// {"acked": {"<id>": <unix seconds>}} and lists only acknowledged messages of the calling device's user: unknown,
// unacknowledged and other users' ids are omitted alike, so the endpoint cannot be used to probe for messages.
func (s *server) deviceAcks(w http.ResponseWriter, r *http.Request, dev store.DeviceWithPairing, _ []byte) {
	var ids []string
	seen := map[string]bool{}
	n := 0
	for _, id := range strings.Split(r.URL.Query().Get("ids"), ",") {
		if id = strings.TrimSpace(id); id == "" {
			continue
		}
		if n++; n > maxAckIDs {
			writeFieldErr(w, http.StatusBadRequest, "invalid_request", "ids", "at most 100")
			return
		}
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	at, err := s.Store.AckedAtForUser(dev.Pairing.ExternalUserID, ids)
	if err != nil {
		s.internal(w, "ack lookup failed", err)
		return
	}
	acked := make(map[string]int64, len(at))
	for id, t := range at {
		acked[id] = t.Unix()
	}
	writeJSON(w, http.StatusOK, map[string]any{"acked": acked})
}
