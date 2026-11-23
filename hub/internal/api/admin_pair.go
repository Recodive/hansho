package api

import (
	"fmt"
	"html/template"
	"net/http"
	"strings"
	"time"

	"rsc.io/qr"
)

type adminPairForm struct{ Name, Error string }

type adminPairDone struct {
	Name, Link, Expires string
	ExpiresAt           time.Time
	Minutes             int
	QR                  template.HTML
}

func (s *server) adminPairPage(w http.ResponseWriter, _ *http.Request, csrf string) {
	s.adminRender(w, http.StatusOK, "pair", "pair", "Pair a person", csrf, adminPairForm{})
}

// adminPair mints a pairing ticket the way createTicket does, for a name the admin typed, and shows it. The page is the
// POST's own response: the ticket is not stored anywhere that could show it again.
func (s *server) adminPair(w http.ResponseWriter, r *http.Request, csrf string) {
	name := strings.TrimSpace(r.PostForm.Get("name"))
	if !validExternalUserID(name) {
		s.adminRender(w, http.StatusBadRequest, "pair", "pair", "Pair a person", csrf,
			adminPairForm{Name: name, Error: "Enter a name for this person, at most 256 bytes long (a letter without accents is one byte)."})
		return
	}
	_, link, expires, err := s.issueTicket(name)
	if err != nil {
		s.log.Error("admin: create pair code failed", "err", err)
		s.adminNotice(w, http.StatusInternalServerError, csrf, "Something went wrong", "The hub could not make a pair code. Try again.", "/admin/pair", "Back")
		return
	}
	svg, err := qrSVG(link)
	if err != nil {
		s.log.Error("admin: render QR code failed", "err", err)
	}
	s.adminInfo("admin: pair code created", "external_user_id", name, "expires_at", expires.UTC().Format(time.RFC3339))
	s.adminRender(w, http.StatusOK, "pair_done", "pair", "Pair code for "+name, csrf, adminPairDone{
		Name: name, Link: link, Expires: adminTime(expires), ExpiresAt: expires, Minutes: int(ticketTTL.Minutes()), QR: svg,
	})
}

// qrSVG draws text as a QR code: an inline SVG with the four-module quiet zone, in plain black on white so that every
// scanner reads it whatever the page's theme.
func qrSVG(text string) (template.HTML, error) {
	c, err := qr.Encode(text, qr.M)
	if err != nil {
		return "", err
	}
	const quiet = 4
	dim := c.Size + 2*quiet
	var b strings.Builder
	fmt.Fprintf(&b, `<svg class="qr" viewBox="0 0 %[1]d %[1]d" role="img" aria-label="QR code of the pair link" shape-rendering="crispEdges"><rect width="%[1]d" height="%[1]d" fill="#fff"/><path fill="#000" d="`, dim)
	for y := 0; y < c.Size; y++ {
		for x := 0; x < c.Size; x++ {
			if !c.Black(x, y) {
				continue
			}
			start := x
			for x+1 < c.Size && c.Black(x+1, y) {
				x++
			}
			fmt.Fprintf(&b, "M%d %dh%dv1h-%dz", start+quiet, y+quiet, x-start+1, x-start+1)
		}
	}
	b.WriteString(`"/></svg>`)
	return template.HTML(b.String()), nil
}
