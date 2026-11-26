package api

import (
	"net/http"
)

type adminFeedRow struct {
	ID, Title, Person, Template, Who, Updated string
}

// adminFeeds lists the feeds the sender's software has published, without their data.
func (s *server) adminFeeds(w http.ResponseWriter, _ *http.Request, csrf string) {
	feeds, err := s.Store.ListFeeds()
	if err != nil {
		s.log.Error("admin: list feeds failed", "err", err)
		s.adminNotice(w, http.StatusInternalServerError, csrf, "Something went wrong", "The hub could not list feeds. Try again.", "/admin/", "Back to the start")
		return
	}
	rows := make([]adminFeedRow, len(feeds))
	for i, f := range feeds {
		r := adminFeedRow{ID: f.ID, Title: f.Title, Person: f.ExternalUserID, Template: f.TemplateID, Updated: adminTime(f.UpdatedAt)}
		if r.Template == "" {
			r.Template = "Automatic layout"
		}
		if f.ExternalUserID != "" {
			r.Who = "Only " + f.ExternalUserID
		} else {
			r.Who = adminAudienceText(f.Effective)
			if f.Audience == nil && f.ChannelID != "" {
				r.Who += " (from channel " + f.ChannelID + ")"
			}
		}
		rows[i] = r
	}
	s.adminRender(w, http.StatusOK, "feeds", "feeds", "Feeds", csrf, rows)
}
