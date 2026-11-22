package store

import (
	"errors"
	"reflect"
	"testing"
)

func TestAudienceModes(t *testing.T) {
	st := openTest(t)
	if err := st.PutGroup("oncall", []string{"alice", "bob"}); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		aud  Audience
		want map[string]bool // user -> allowed
	}{
		{"zero value is everyone", Audience{}, map[string]bool{"alice": true, "carol": true}},
		{"everyone", Audience{Mode: AudienceEveryone}, map[string]bool{"alice": true, "carol": true}},
		{"only a user", Audience{Mode: AudienceOnly, Users: []string{"carol"}}, map[string]bool{"alice": false, "carol": true}},
		{"only a group", Audience{Mode: AudienceOnly, Groups: []string{"oncall"}}, map[string]bool{"alice": true, "bob": true, "carol": false}},
		{"only users and groups", Audience{Mode: AudienceOnly, Users: []string{"carol"}, Groups: []string{"oncall"}}, map[string]bool{"alice": true, "carol": true, "dave": false}},
		{"only nobody", Audience{Mode: AudienceOnly}, map[string]bool{"alice": false}},
		{"except a user", Audience{Mode: AudienceExcept, Users: []string{"carol"}}, map[string]bool{"alice": true, "carol": false}},
		{"except a group", Audience{Mode: AudienceExcept, Groups: []string{"oncall"}}, map[string]bool{"alice": false, "carol": true}},
		{"except nobody", Audience{Mode: AudienceExcept}, map[string]bool{"alice": true}},
		{"an unknown group matches nobody", Audience{Mode: AudienceOnly, Groups: []string{"gone"}}, map[string]bool{"alice": false}},
		{"an unknown mode refuses", Audience{Mode: "sometimes"}, map[string]bool{"alice": false}},
	}
	for _, c := range cases {
		for user, want := range c.want {
			acc, err := st.AccessFor(user)
			if err != nil {
				t.Fatal(err)
			}
			if got := acc.Allows(c.aud); got != want {
				t.Errorf("%s: %s allowed = %v, want %v", c.name, user, got, want)
			}
		}
	}
}

func TestGroupsAreReplacedWholeAndResolvedLive(t *testing.T) {
	st := openTest(t)
	if _, err := st.GetGroup("oncall"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown group: %v", err)
	}
	if err := st.PutGroup("oncall", []string{"bob", "alice", "bob"}); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.GetGroup("oncall"); !reflect.DeepEqual(got, []string{"alice", "bob"}) {
		t.Fatalf("members %v", got)
	}
	aud := Audience{Mode: AudienceOnly, Groups: []string{"oncall"}}
	allowed := func(user string) bool {
		t.Helper()
		acc, err := st.AccessFor(user) // a fresh read each time: nothing is cached
		if err != nil {
			t.Fatal(err)
		}
		return acc.Allows(aud)
	}
	if !allowed("alice") || allowed("carol") {
		t.Fatal("wrong members")
	}
	if err := st.PutGroup("oncall", []string{"carol"}); err != nil {
		t.Fatal(err)
	}
	if allowed("alice") || !allowed("carol") {
		t.Fatal("replace did not take effect at once")
	}
	if err := st.PutGroup("oncall", []string{}); err != nil {
		t.Fatal(err)
	}
	if got, err := st.GetGroup("oncall"); err != nil || len(got) != 0 {
		t.Fatalf("empty group: %v %v", got, err)
	}
	if err := st.PutGroup("oncall", []string{"carol"}); err != nil {
		t.Fatal(err)
	}
	if ok, err := st.DeleteGroup("oncall"); err != nil || !ok {
		t.Fatalf("delete: %v %v", ok, err)
	}
	if ok, _ := st.DeleteGroup("oncall"); ok {
		t.Fatal("second delete reported a change")
	}
	if allowed("carol") {
		t.Fatal("a deleted group still grants access")
	}
}

func TestChannelAudienceRoundTripAndAccess(t *testing.T) {
	st := openTest(t)
	ch := Channel{ID: "ops", Name: "Ops", DefaultLevel: "active", MaxLevel: "page"}
	if err := st.UpsertChannel(ch); err != nil {
		t.Fatal(err)
	}
	got, _ := st.GetChannel("ops")
	if got.Audience.Mode != AudienceEveryone || got.Audience.Users == nil || got.Audience.Groups == nil {
		t.Fatalf("default audience %+v", got.Audience)
	}
	ch.Audience = Audience{Mode: AudienceOnly, Users: []string{"alice", "not-paired-yet"}, Groups: []string{"oncall"}}
	if err := st.UpsertChannel(ch); err != nil {
		t.Fatal(err)
	}
	got, _ = st.GetChannel("ops")
	if !reflect.DeepEqual(got.Audience, ch.Audience) {
		t.Fatalf("audience %+v", got.Audience)
	}
	for user, want := range map[string]bool{"alice": true, "not-paired-yet": true, "bob": false} {
		if ok, err := st.CanAccessChannel(user, "ops"); err != nil || ok != want {
			t.Errorf("%s: %v %v, want %v", user, ok, err, want)
		}
	}
	// An upsert without an audience is everyone again.
	ch.Audience = Audience{}
	st.UpsertChannel(ch)
	if ok, _ := st.CanAccessChannel("bob", "ops"); !ok {
		t.Fatal("upsert without audience did not reset it")
	}
	if _, err := st.CanAccessChannel("bob", "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown channel: %v", err)
	}
}

func TestFeedInheritsChannelAudienceUnlessItSetsItsOwn(t *testing.T) {
	st := openTest(t)
	st.UpsertChannel(Channel{ID: "ops", Name: "Ops", DefaultLevel: "active", MaxLevel: "page",
		Audience: Audience{Mode: AudienceOnly, Users: []string{"alice"}}})
	put := func(id, channel string, aud *Audience) {
		t.Helper()
		if err := st.UpsertFeed(Feed{ID: id, ChannelID: channel, Audience: aud, DataJSON: "{}", UpdatedAt: t0}); err != nil {
			t.Fatal(err)
		}
	}
	put("inherits", "ops", nil)
	put("own", "ops", &Audience{Mode: AudienceExcept, Users: []string{"alice"}})
	put("open", "ops", &Audience{Mode: AudienceEveryone})
	put("free", "", nil) // no channel, no audience: everyone

	alice, _ := st.AccessFor("alice")
	bob, _ := st.AccessFor("bob")
	want := map[string][2]bool{ // feed -> {alice, bob}
		"inherits": {true, false}, "own": {false, true}, "open": {true, true}, "free": {true, true},
	}
	for id, w := range want {
		f, err := st.GetFeedForUser(id, "alice")
		if err != nil {
			t.Fatal(err)
		}
		if a, b := alice.Allows(f.Effective), bob.Allows(f.Effective); a != w[0] || b != w[1] {
			t.Errorf("%s: alice %v bob %v, want %v", id, a, b, w)
		}
	}
	if f, _ := st.GetFeed("inherits", ""); f.Audience != nil || f.ChannelID != "ops" {
		t.Fatalf("inherits: %+v", f)
	}
	if f, _ := st.GetFeed("own", ""); f.Audience == nil || f.Audience.Mode != AudienceExcept {
		t.Fatalf("own: %+v", f)
	}
	list, _ := st.ListFeedsForUser("bob")
	allowed := map[string]bool{}
	for _, l := range list {
		allowed[l.ID] = bob.Allows(l.Audience)
	}
	if !reflect.DeepEqual(allowed, map[string]bool{"inherits": false, "own": true, "open": true, "free": true}) {
		t.Fatalf("listing audiences: %v", allowed)
	}

	// The channel's audience changes at once for the feeds that inherit it.
	st.UpsertChannel(Channel{ID: "ops", Name: "Ops", DefaultLevel: "active", MaxLevel: "page"})
	f, _ := st.GetFeedForUser("inherits", "bob")
	if !bob.Allows(f.Effective) {
		t.Fatal("feed did not follow the channel's audience")
	}
}

func TestRevokePairingRecordsWho(t *testing.T) {
	st := openTest(t)
	st.CreatePairing(Pairing{ID: "p1", ExternalUserID: "alice", KeyWrapped: []byte("k"), CreatedAt: t0})
	st.CreatePairing(Pairing{ID: "p2", ExternalUserID: "alice", KeyWrapped: []byte("k"), CreatedAt: t0})
	if p, _ := st.GetPairing("p1"); p.RevokedBy != "" {
		t.Fatalf("active pairing has revoked_by %q", p.RevokedBy)
	}
	st.RevokePairing("p1", "sender")
	st.RevokePairing("p1", "device") // already revoked: the first one stays
	st.RevokePairing("p2", "device")
	if p, _ := st.GetPairing("p1"); p.RevokedBy != "sender" {
		t.Fatalf("p1 revoked by %q", p.RevokedBy)
	}
	if p, _ := st.GetPairing("p2"); p.RevokedBy != "device" {
		t.Fatalf("p2 revoked by %q", p.RevokedBy)
	}
}

func TestActiveUsers(t *testing.T) {
	st := openTest(t)
	for _, p := range []Pairing{{ID: "p1", ExternalUserID: "bob"}, {ID: "p2", ExternalUserID: "alice"}, {ID: "p3", ExternalUserID: "alice"}, {ID: "p4", ExternalUserID: "gone"}} {
		p.KeyWrapped, p.CreatedAt = []byte("k"), t0
		st.CreatePairing(p)
	}
	st.RevokePairing("p4", "device")
	if got, _ := st.ActiveUsers(); !reflect.DeepEqual(got, []string{"alice", "bob"}) {
		t.Fatalf("active users %v", got)
	}
}
