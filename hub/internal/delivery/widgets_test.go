package delivery

import "testing"

// A feed change must wake the app (it is the only one that can fetch and cache the feed) as well as reload the widgets.
func TestReloadWidgetsWakesAppAndWidgets(t *testing.T) {
	f := newFixture(t)
	f.pairing("p1", "alice", "phone")
	devs, err := f.st.ActiveDevicesForUser("alice")
	if err != nil {
		t.Fatal(err)
	}
	n, err := f.w.ReloadWidgets(t.Context(), devs)
	if err != nil || n != 1 {
		t.Fatalf("%d %v", n, err)
	}
	bg, wid := f.pushesTo("bg-phone"), f.pushesTo("wid-phone")
	if len(bg) != 1 || bg[0].Kind != "background" || string(bg[0].Payload) != feedSyncPayload {
		t.Fatalf("background: %+v", bg)
	}
	if len(wid) != 1 || wid[0].Kind != "widgets" {
		t.Fatalf("widgets: %+v", wid)
	}
}
