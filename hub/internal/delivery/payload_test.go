package delivery

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/recodive/hansho/hub/internal/crypto"
	"github.com/recodive/hansho/hub/internal/store"
)

var fixedEnv = crypto.Envelope{V: 1, P: "pair_1", M: "msg_1", CT: "Y3Q", S: "c2ln"}

var opsChannel = store.Channel{ID: "ops", Name: "Ops", DefaultLevel: "active", MaxLevel: "page", Thread: "ops-thread"}

func ptr(s string) *string { return &s }

func TestEffectiveLevel(t *testing.T) {
	ch := func(def, max string) store.Channel { return store.Channel{DefaultLevel: def, MaxLevel: max} }
	cases := []struct {
		name string
		req  *string
		ch   store.Channel
		want string
	}{
		{"default used", nil, ch("active", "critical"), "active"},
		{"requested within max", ptr("time-sensitive"), ch("active", "critical"), "time-sensitive"},
		{"page clamped to active", ptr("page"), ch("active", "active"), "active"},
		{"critical clamped to passive", ptr("critical"), ch("passive", "passive"), "passive"},
		{"requested lower than default", ptr("passive"), ch("time-sensitive", "page"), "passive"},
		{"page allowed", ptr("page"), ch("active", "page"), "page"},
		{"default above max is clamped", nil, ch("page", "active"), "active"},
		{"unknown requested falls back to default", ptr("bogus"), ch("active", "critical"), "active"},
	}
	for _, tc := range cases {
		if got := EffectiveLevel(tc.req, tc.ch); got != tc.want {
			t.Errorf("%s: got %s, want %s", tc.name, got, tc.want)
		}
	}
}

func TestPriority(t *testing.T) {
	want := map[string]int{"passive": 5, "active": 10, "time-sensitive": 10, "critical": 10, "page": 10}
	for l, p := range want {
		if got := Priority(l); got != p {
			t.Errorf("%s: %d, want %d", l, got, p)
		}
	}
}

func TestPayloadGolden(t *testing.T) {
	for _, level := range []string{"passive", "active", "time-sensitive", "critical", "page"} {
		t.Run(level, func(t *testing.T) {
			got, err := BuildAlertPayload("Example Hub", opsChannel, level, fixedEnv, "ding.caf")
			if err != nil {
				t.Fatal(err)
			}
			want, err := os.ReadFile("testdata/payload_" + level + ".json")
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, bytes.TrimRight(want, "\n")) {
				t.Fatalf("payload mismatch\n got %s\nwant %s", got, want)
			}
		})
	}
}

func TestCancelPayloadGolden(t *testing.T) {
	got, err := BuildCancelPayload("Example Hub", opsChannel, fixedEnv)
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile("testdata/payload_cancel.json")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, bytes.TrimRight(want, "\n")) {
		t.Fatalf("payload mismatch\n got %s\nwant %s", got, want)
	}
	if strings.Contains(string(got), `"sound"`) {
		t.Fatalf("cancel payload must be silent: %s", got)
	}
	big := fixedEnv
	big.CT = strings.Repeat("A", 4096)
	if _, err := BuildCancelPayload("H", opsChannel, big); !errors.Is(err, ErrPayloadTooLarge) {
		t.Fatalf("got %v", err)
	}
}

func TestPayloadDefaultSound(t *testing.T) {
	p, err := BuildAlertPayload("H", opsChannel, "active", fixedEnv, "")
	if err != nil || !strings.Contains(string(p), `"sound":"default"`) {
		t.Fatalf("%v %s", err, p)
	}
	p, err = BuildAlertPayload("H", opsChannel, "critical", fixedEnv, "")
	if err != nil || !strings.Contains(string(p), `"sound":{"critical":1,"name":"default","volume":1}`) {
		t.Fatalf("%v %s", err, p)
	}
	p, _ = BuildAlertPayload("H", opsChannel, "passive", fixedEnv, "")
	if strings.Contains(string(p), `"sound"`) {
		t.Fatalf("passive must not carry a sound: %s", p)
	}
}

func TestPayloadRejectsUnknownLevel(t *testing.T) {
	if _, err := BuildAlertPayload("H", opsChannel, "urgent", fixedEnv, ""); err == nil {
		t.Fatal("unknown level accepted")
	}
}

func TestPayloadSizeLimit(t *testing.T) {
	big := fixedEnv
	big.CT = strings.Repeat("A", 4096)
	if _, err := BuildAlertPayload("H", opsChannel, "active", big, ""); !errors.Is(err, ErrPayloadTooLarge) {
		t.Fatalf("got %v", err)
	}
	// Exactly at the limit is fine.
	small := fixedEnv
	base, _ := BuildAlertPayload("H", opsChannel, "active", small, "")
	small.CT = strings.Repeat("A", MaxPayloadBytes-len(base)+len(fixedEnv.CT))
	p, err := BuildAlertPayload("H", opsChannel, "active", small, "")
	if err != nil || len(p) != MaxPayloadBytes {
		t.Fatalf("len %d err %v", len(p), err)
	}
	small.CT += "A"
	if _, err := BuildAlertPayload("H", opsChannel, "active", small, ""); !errors.Is(err, ErrPayloadTooLarge) {
		t.Fatalf("4097 bytes accepted: %v", err)
	}
}

func TestPlaintextShape(t *testing.T) {
	pt := Plaintext{Title: "T", Body: "B", Channel: "ops", Level: "page", Page: true, Data: json.RawMessage(`{"a":1}`)}
	b, err := json.Marshal(pt)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != `{"title":"T","body":"B","data":{"a":1},"channel":"ops","level":"page","page":true}` {
		t.Fatalf("%s", b)
	}
}
