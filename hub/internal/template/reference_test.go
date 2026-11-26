package template

import (
	"strings"
	"testing"
)

// TestReferenceListsEverything fails when a component or property of the validator is missing from the reference, so
// the prompt the admin UI gives an AI cannot fall behind the rules.
func TestReferenceListsEverything(t *testing.T) {
	rows := Reference()
	listed := map[string]string{}
	for _, r := range rows {
		for _, n := range strings.Split(r[0], ", ") {
			listed[n] = r[1]
		}
	}
	for name := range components {
		text, ok := listed[name]
		if !ok {
			t.Errorf("component %s is not in the reference", name)
			continue
		}
		for prop := range props[name] {
			if !strings.Contains(", "+text, ", "+prop) {
				t.Errorf("%s: property %s is not in the reference %q", name, prop, text)
			}
		}
	}
	if len(listed) != len(components) {
		t.Errorf("the reference lists %d components, the validator knows %d", len(listed), len(components))
	}
}

func TestReferenceText(t *testing.T) {
	got := map[string]string{}
	for _, r := range Reference() {
		got[r[0]] = r[1]
	}
	for name, want := range map[string]string{
		"vstack, hstack": "children (required), align leading/center/trailing, padding 0-32, radius 0-32, spacing 0-64, background (nested stacks; the root's is the card background)",
		"spacer":         "min 0-256",
		"image":          "fit fill/fit, height 0-512, radius 0-64, url (https), width 0-512",
		"timer":          "color, mode countdown/countup, style largeTitle/title/headline/body/caption/footnote, to (unix seconds)",
		"foreach":        "axis vertical/horizontal, each (data path to an array, required), max 1-12, spacing 0-64, body (one node, required)",
	} {
		if got[name] != want {
			t.Errorf("%s:\n got %q\nwant %q", name, got[name], want)
		}
	}
}
