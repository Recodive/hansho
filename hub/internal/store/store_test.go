package store

import (
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

var t0 = time.Unix(1_700_000_000, 0)

func openTest(t *testing.T) *Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	st.Now = func() time.Time { return t0 }
	t.Cleanup(func() { st.Close() })
	return st
}

func TestMigrationsIdempotentOnReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hub.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateAPIKey("ci"); err != nil {
		t.Fatal(err)
	}
	st.Close()
	st, err = Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer st.Close()
	var n int
	if err := st.db.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&n); err != nil || n != 3 { // 001_init, 002_unique_active_device and 003_feed_title, each applied once
		t.Fatalf("migrations applied %d times (%v)", n, err)
	}
	var fk int
	st.db.QueryRow(`PRAGMA foreign_keys`).Scan(&fk)
	if fk != 1 {
		t.Fatal("foreign keys not enabled")
	}
	var mode string
	st.db.QueryRow(`PRAGMA journal_mode`).Scan(&mode)
	if mode != "wal" {
		t.Fatalf("journal mode %q", mode)
	}
	// Data survived.
	var keys int
	st.db.QueryRow(`SELECT COUNT(*) FROM api_keys`).Scan(&keys)
	if keys != 1 {
		t.Fatal("data lost on reopen")
	}
}

func TestTicketSingleUse(t *testing.T) {
	st := openTest(t)
	tk, err := st.CreateTicket("user-1", 10*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if len(tk) < 43 {
		t.Fatalf("ticket too short: %q", tk)
	}
	uid, err := st.ConsumeTicket(tk, t0.Add(time.Minute))
	if err != nil || uid != "user-1" {
		t.Fatalf("first consume: %q %v", uid, err)
	}
	if _, err := st.ConsumeTicket(tk, t0.Add(time.Minute)); !errors.Is(err, ErrTicketUsed) {
		t.Fatalf("second consume: %v", err)
	}
}

func TestTicketExpiredAndUnknown(t *testing.T) {
	st := openTest(t)
	tk, _ := st.CreateTicket("user-1", 10*time.Minute)
	if _, err := st.ConsumeTicket(tk, t0.Add(10*time.Minute+time.Second)); !errors.Is(err, ErrTicketExpired) {
		t.Fatalf("expired: %v", err)
	}
	// An expired ticket stays expired, it is not "used".
	if _, err := st.ConsumeTicket(tk, t0); !errors.Is(err, ErrTicketExpired) && err != nil {
		// consuming it at a valid time afterwards is allowed (it was never used)
		t.Fatalf("unexpected: %v", err)
	}
	if _, err := st.ConsumeTicket("nope", t0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown: %v", err)
	}
}

func TestTicketStoredHashed(t *testing.T) {
	st := openTest(t)
	tk, _ := st.CreateTicket("user-1", time.Minute)
	var n int
	st.db.QueryRow(`SELECT COUNT(*) FROM tickets WHERE ticket = ?`, tk).Scan(&n)
	if n != 0 {
		t.Fatal("ticket stored in plaintext")
	}
}

// Review focus 1: concurrent redemption yields exactly one winner.
func TestConsumeTicketConcurrent(t *testing.T) {
	st := openTest(t)
	tk, _ := st.CreateTicket("user-1", time.Hour)
	var ok, used, other atomic.Int32
	var wg sync.WaitGroup
	start := make(chan struct{})
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := st.ConsumeTicket(tk, t0)
			switch {
			case err == nil:
				ok.Add(1)
			case errors.Is(err, ErrTicketUsed):
				used.Add(1)
			default:
				t.Errorf("unexpected: %v", err)
				other.Add(1)
			}
		}()
	}
	close(start)
	wg.Wait()
	if ok.Load() != 1 || used.Load() != 19 {
		t.Fatalf("ok=%d used=%d other=%d", ok.Load(), used.Load(), other.Load())
	}
}

func TestAPIKeyLookup(t *testing.T) {
	st := openTest(t)
	key, err := st.CreateAPIKey("backend")
	if err != nil {
		t.Fatal(err)
	}
	k, err := st.LookupAPIKey(key)
	if err != nil || k.Name != "backend" {
		t.Fatalf("lookup: %+v %v", k, err)
	}
	if _, err := st.LookupAPIKey(key + "x"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("bad key: %v", err)
	}
	if _, err := st.LookupAPIKey(""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("empty key: %v", err)
	}
	var stored string
	if err := st.db.QueryRow(`SELECT hash FROM api_keys`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored == key || stored == "" {
		t.Fatal("api key stored in plaintext")
	}
	if _, err := st.db.Exec(`UPDATE api_keys SET revoked_at = 1`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.LookupAPIKey(key); !errors.Is(err, ErrNotFound) {
		t.Fatalf("revoked key accepted: %v", err)
	}
}

func mkPairing(t *testing.T, st *Store, id, user string) {
	t.Helper()
	if err := st.CreatePairing(Pairing{ID: id, ExternalUserID: user, KeyWrapped: []byte("wrapped"), CreatedAt: t0}); err != nil {
		t.Fatal(err)
	}
}

func mkDevice(t *testing.T, st *Store, id, pairing string) {
	t.Helper()
	err := st.AddDevice(Device{ID: id, PairingID: pairing, SignPub: []byte("sign-" + id), Platform: "ios", Name: id,
		SealedAlert: "alert-" + id, SealedBackground: "bg-" + id, CreatedAt: t0})
	if err != nil {
		t.Fatal(err)
	}
}

func ids(ds []DeviceWithPairing) map[string]bool {
	m := map[string]bool{}
	for _, d := range ds {
		m[d.ID] = true
	}
	return m
}

func TestActiveDevicesForUserExcludesRevoked(t *testing.T) {
	st := openTest(t)
	mkPairing(t, st, "p1", "alice")
	mkPairing(t, st, "p2", "alice")
	mkPairing(t, st, "p3", "bob")
	mkDevice(t, st, "d1", "p1")
	mkDevice(t, st, "d2", "p1")
	mkDevice(t, st, "d3", "p2")
	mkDevice(t, st, "d4", "p3")

	ds, err := st.ActiveDevicesForUser("alice")
	if err != nil {
		t.Fatal(err)
	}
	if got := ids(ds); len(got) != 3 || !got["d1"] || !got["d2"] || !got["d3"] {
		t.Fatalf("active: %v", got)
	}
	for _, d := range ds {
		if d.Pairing.ID != d.PairingID || d.Pairing.ExternalUserID != "alice" || string(d.Pairing.KeyWrapped) != "wrapped" {
			t.Fatalf("pairing not joined: %+v", d)
		}
	}

	if err := st.RevokeDevice("d2"); err != nil {
		t.Fatal(err)
	}
	changed, err := st.RevokePairing("p2")
	if err != nil || !changed {
		t.Fatalf("revoke pairing: %v %v", changed, err)
	}
	if changed, _ := st.RevokePairing("p2"); changed {
		t.Fatal("second revoke reported a change")
	}
	ds, _ = st.ActiveDevicesForUser("alice")
	if got := ids(ds); len(got) != 1 || !got["d1"] {
		t.Fatalf("after revoke: %v", got)
	}
	if ds, _ := st.ActiveDevicesForUser("nobody"); len(ds) != 0 {
		t.Fatal("devices for unknown user")
	}
}

func TestDeviceTokens(t *testing.T) {
	st := openTest(t)
	mkPairing(t, st, "p1", "alice")
	mkDevice(t, st, "d1", "p1")
	err := st.UpdateDeviceTokens("d1", Tokens{Alert: "a2", Background: "b2", PushToStart: "p2", Widgets: "w2"})
	if err != nil {
		t.Fatal(err)
	}
	d, err := st.GetDevice("d1")
	if err != nil || d.SealedAlert != "a2" || d.SealedPushToStart != "p2" || d.SealedWidgets != "w2" || d.SealedBackground != "b2" {
		t.Fatalf("%+v %v", d, err)
	}
	if err := st.ClearDeviceToken("d1", TokenAlert); err != nil {
		t.Fatal(err)
	}
	d, _ = st.GetDevice("d1")
	if d.SealedAlert != "" || d.SealedWidgets != "w2" {
		t.Fatalf("clear touched wrong columns: %+v", d)
	}
	if err := st.ClearDeviceToken("d1", TokenKind("bogus")); err == nil {
		t.Fatal("bogus kind accepted")
	}
	if _, err := st.GetDevice("nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("%v", err)
	}
}

func TestSeenNonce(t *testing.T) {
	st := openTest(t)
	now := t0
	st.Now = func() time.Time { return now }
	seen, err := st.SeenNonce("n1", time.Minute)
	if err != nil || seen {
		t.Fatalf("first: %v %v", seen, err)
	}
	if seen, _ := st.SeenNonce("n1", time.Minute); !seen {
		t.Fatal("replay not detected")
	}
	if seen, _ := st.SeenNonce("n2", time.Minute); seen {
		t.Fatal("other nonce seen")
	}
	now = t0.Add(2 * time.Minute)
	if seen, _ := st.SeenNonce("n1", time.Minute); seen {
		t.Fatal("expired nonce still seen")
	}
}

// One active device per pairing and signing key, even when joins race.
func TestConcurrentUpsertOfTheSameDeviceCreatesOne(t *testing.T) {
	st := openTest(t)
	if err := st.CreatePairing(Pairing{ID: "pair_1", ExternalUserID: "u", KeyWrapped: []byte("k"), CreatedAt: t0}); err != nil {
		t.Fatal(err)
	}
	ids := make(chan string, 20)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			id, _, err := st.UpsertDevice(Device{
				ID: NewID("dev_"), PairingID: "pair_1", SignPub: []byte("same-signing-key"), Platform: "ios", Name: "iPhone",
				SealedAlert: "alert-" + string(rune('a'+i)), CreatedAt: t0,
			})
			if err != nil {
				t.Error(err)
				return
			}
			ids <- id
		}()
	}
	close(start)
	wg.Wait()
	close(ids)
	seen := map[string]bool{}
	for id := range ids {
		seen[id] = true
	}
	if len(seen) != 1 {
		t.Fatalf("%d distinct device ids, want 1: %v", len(seen), seen)
	}
	devs, err := st.ActiveDevicesForUser("u")
	if err != nil || len(devs) != 1 {
		t.Fatalf("active devices %d, err %v", len(devs), err)
	}
}

// Existing duplicates (from before the rule existed) are retired by the migration, keeping the newest of each.
func TestMigrationRetiresDuplicateActiveDevices(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hub.db")
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	init1, _ := migrationFS.ReadFile("migrations/001_init.sql")
	if _, err := raw.Exec(string(init1)); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`CREATE TABLE schema_migrations (version TEXT PRIMARY KEY, applied_at INTEGER NOT NULL);
		INSERT INTO schema_migrations VALUES ('001_init.sql', 1);
		INSERT INTO pairings(id, external_user_id, key_wrapped, created_at) VALUES ('p', 'u', x'00', 1);
		INSERT INTO devices(id, pairing_id, sign_pub, platform, name, created_at) VALUES
			('old', 'p', x'AA', 'ios', 'iPhone', 1), ('mid', 'p', x'AA', 'ios', 'iPhone', 2), ('new', 'p', x'AA', 'ios', 'iPhone', 3),
			('other', 'p', x'BB', 'ios', 'Watch', 1);`); err != nil {
		t.Fatal(err)
	}
	raw.Close()

	st, err := Open(path) // applies 002
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	devs, err := st.ActiveDevicesForUser("u")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, d := range devs {
		got[d.ID] = true
	}
	if len(devs) != 2 || !got["new"] || !got["other"] {
		t.Fatalf("active devices after migration: %v", got)
	}
}

func TestListFeedsForUser(t *testing.T) {
	st := openTest(t)
	put := func(id, user, title string) {
		t.Helper()
		if err := st.UpsertFeed(Feed{ID: id, ExternalUserID: user, Title: title, DataJSON: "{}", UpdatedAt: t0}); err != nil {
			t.Fatal(err)
		}
	}
	put("zeta", "", "Global zeta")
	put("status", "", "Global status")
	put("status", "alice", "Alice status") // her own wins over the global feed with the same id
	put("mine", "alice", "")
	put("secret", "bob", "Bob only") // another person's feed is never listed

	got, err := st.ListFeedsForUser("alice")
	if err != nil {
		t.Fatal(err)
	}
	var line []string
	for _, l := range got {
		line = append(line, l.ID+"="+l.Title)
	}
	if want := "mine=,status=Alice status,zeta=Global zeta"; strings.Join(line, ",") != want {
		t.Fatalf("alice sees %q, want %q", strings.Join(line, ","), want)
	}
	if got[0].UpdatedAt != t0 {
		t.Fatalf("updated_at %v", got[0].UpdatedAt)
	}

	// Someone without feeds of their own still gets the global ones.
	got, err = st.ListFeedsForUser("carol")
	if err != nil || len(got) != 2 || got[0].ID != "status" || got[0].Title != "Global status" || got[1].ID != "zeta" {
		t.Fatalf("carol sees %+v (%v)", got, err)
	}

	// An upsert stores what is sent: without a title the old one is gone.
	put("zeta", "", "")
	got, _ = st.ListFeedsForUser("carol")
	if got[1].Title != "" {
		t.Fatalf("title kept after an upsert without one: %+v", got)
	}
}
