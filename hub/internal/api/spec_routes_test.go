package api

import (
	"encoding/json"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// TestSenderRoutesMatchSpec fails when the sender API routes in api.go and the SDK contract in
// sdk/spec/openapi.json differ, in either direction, so the hub cannot grow a route the SDKs do not have.
func TestSenderRoutesMatchSpec(t *testing.T) {
	b, err := os.ReadFile("../../../sdk/spec/openapi.json")
	if err != nil {
		t.Fatal(err)
	}
	var spec struct {
		Paths map[string]map[string]json.RawMessage `json:"paths"`
	}
	if err := json.Unmarshal(b, &spec); err != nil {
		t.Fatal(err)
	}
	var want []string
	for p, item := range spec.Paths {
		for m := range item {
			if m != "parameters" {
				want = append(want, strings.ToUpper(m)+" "+p)
			}
		}
	}
	// ponytail: ServeMux cannot list its patterns, so this reads them from the source. A sender route registered
	// outside api.go, or from a computed string, would slip past; register them here as literals.
	src, err := os.ReadFile("api.go")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, m := range regexp.MustCompile(`"((?:GET|PUT|POST|PATCH|DELETE) /api/v1/[^"]*)"`).FindAllSubmatch(src, -1) {
		got = append(got, string(m[1]))
	}
	slices.Sort(want)
	slices.Sort(got)
	for _, r := range got {
		if !slices.Contains(want, r) {
			t.Errorf("route %s is not in sdk/spec/openapi.json: add it there and to every SDK", r)
		}
	}
	for _, r := range want {
		if !slices.Contains(got, r) {
			t.Errorf("sdk/spec/openapi.json has %s, which api.go does not register", r)
		}
	}
	if len(got) == 0 {
		t.Fatal("found no sender routes in api.go")
	}
}
