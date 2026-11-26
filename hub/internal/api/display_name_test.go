package api

import (
	"strings"
	"testing"

	"github.com/recodive/hansho/hub/internal/config"
)

func TestSenderSetsDisplayNameAndSyncShowsIt(t *testing.T) {
	e := newEnv(t)
	d := e.newDevice("phone")
	d.pair("alice")
	for name, want := range map[string]int{"Álice Müller 🔔": 204, "  Bob  ": 204, "": 204, strings.Repeat("あ", 41): 400, "a\nb": 400} {
		if w := e.api("PUT", "/api/v1/users/alice", map[string]any{"display_name": name}); w.Code != want {
			t.Fatalf("%q: %d %s", name, w.Code, w.Body)
		}
	}
	e.api("PUT", "/api/v1/users/alice", map[string]any{"display_name": strings.Repeat("あ", 40)})
	s := decode[struct {
		Profile struct {
			DisplayName string `json:"display_name"`
			Editable    bool   `json:"editable"`
		} `json:"profile"`
	}](t, d.request("GET", "/device/v1/sync", nil))
	if s.Profile.DisplayName != strings.Repeat("あ", 40) || s.Profile.Editable {
		t.Fatalf("%+v", s.Profile)
	}
}

func TestPersonEditsDisplayNameOnlyWhenTheHubAllowsIt(t *testing.T) {
	e := newEnv(t)
	d := e.newDevice("phone")
	d.pair("alice")
	if w := d.request("PUT", "/device/v1/profile", map[string]any{"display_name": "Alice"}); w.Code != 403 {
		t.Fatalf("locked hub: %d", w.Code)
	}
	e = newEnvCfg(t, func(c *config.Config) { c.AllowDisplayNameEdit = true })
	d = e.newDevice("phone")
	d.pair("alice")
	if w := d.request("PUT", "/device/v1/profile", map[string]any{"display_name": "Alice"}); w.Code != 204 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if n, _ := e.st.DisplayName("alice"); n != "Alice" {
		t.Fatal(n)
	}
	// Last write wins: the sender's change overwrites the person's.
	e.api("PUT", "/api/v1/users/alice", map[string]any{"display_name": "Alice M."})
	if n, _ := e.st.DisplayName("alice"); n != "Alice M." {
		t.Fatal(n)
	}
}

func TestAckRecordsTheDisplayNameButNotForSomeoneWithout(t *testing.T) {
	e, alice, _, am, bm := sendToTwo(t, "")
	e.api("PUT", "/api/v1/users/alice", map[string]any{"display_name": "Alice M."})
	ack(t, alice, am)
	b, _ := e.st.GetMessage(bm)
	if b.AckedBy != "alice" || b.AckedByName != "Alice M." {
		t.Fatalf("%+v", b)
	}
	if ev := e.events("message.acked"); ev[1]["acked_by_name"] != "Alice M." {
		t.Fatalf("%v", ev[1])
	}
}
