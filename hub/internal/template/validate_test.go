package template

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func nested(depth int) string {
	s := `{"type":"text"}`
	for i := 1; i < depth; i++ {
		s = `{"type":"vstack","children":[` + s + `]}`
	}
	return s
}

func fieldOf(t *testing.T, err error) string {
	t.Helper()
	var fe *FieldError
	if !errors.As(err, &fe) {
		t.Fatalf("want *FieldError, got %v", err)
	}
	return fe.Path
}

func TestValidAccepted(t *testing.T) {
	ok := `{
	  "type":"vstack",
	  "tint":{"light":"#0A84FF","dark":"#64D2FF"},
	  "background":{"light":"#FFFFFF","dark":"#000000"},
	  "children":[
	    {"type":"hstack","children":[{"type":"symbol","name":"bell"},{"type":"text","style":"headline","text":"Hi"},{"type":"spacer"}]},
	    {"type":"image","url":"https://x/y.png"},
	    {"type":"progress","value":0.4},
	    {"type":"timer","mode":"countdown","to":1700000000},
	    {"type":"gauge","value":3,"max":10},
	    {"type":"badge","text":"NEW","color":{"light":"#FF0000","dark":"#ff453a"}}
	  ]}`
	if err := Validate(json.RawMessage(ok)); err != nil {
		t.Fatal(err)
	}
	for _, typ := range []string{"vstack", "hstack", "spacer", "text", "symbol", "image", "progress", "timer", "gauge", "badge"} {
		body := `{"type":"` + typ + `"`
		if typ == "vstack" || typ == "hstack" {
			body += `,"children":[]`
		}
		if err := Validate(json.RawMessage(body + `}`)); err != nil {
			t.Errorf("%s rejected: %v", typ, err)
		}
	}
}

func TestRejectsWithFieldPath(t *testing.T) {
	cases := []struct {
		name, body, path string
	}{
		{"unknown root type", `{"type":"carousel"}`, "type"},
		{"missing type", `{"children":[]}`, "type"},
		{"type not string", `{"type":4}`, "type"},
		{"unknown nested component", `{"type":"vstack","children":[{"type":"text"},{"type":"spacer"},{"type":"web"}]}`, "children[2].type"},
		{"deep unknown", `{"type":"vstack","children":[{"type":"hstack","children":[{"type":"text"},{"type":"x"}]}]}`, "children[0].children[1].type"},
		{"container without children", `{"type":"vstack"}`, "children"},
		{"nested container without children", `{"type":"vstack","children":[{"type":"hstack"}]}`, "children[0].children"},
		{"children not array", `{"type":"vstack","children":{}}`, "children"},
		{"leaf with children", `{"type":"text","children":[]}`, "children"},
		{"child not object", `{"type":"vstack","children":[{"type":"text"},"x"]}`, "children[1]"},
		{"bad text style", `{"type":"vstack","children":[{"type":"text","style":"huge"}]}`, "children[0].style"},
		{"bad timer mode", `{"type":"timer","mode":"stopwatch"}`, "mode"},
		{"bad color hex", `{"type":"text","color":{"light":"red","dark":"#000000"}}`, "color.light"},
		{"bad color short", `{"type":"text","color":{"light":"#FFF","dark":"#000000"}}`, "color.light"},
		{"color missing dark", `{"type":"text","color":{"light":"#000000"}}`, "color.dark"},
		{"color not object", `{"type":"text","color":"#000000"}`, "color"},
		{"bad tint", `{"type":"vstack","children":[],"tint":{"light":"#000000","dark":"nope"}}`, "tint.dark"},
		{"bad background", `{"type":"vstack","children":[],"background":"#000000"}`, "background"},
		{"tint on nested node", `{"type":"vstack","children":[{"type":"text","tint":{"light":"#000000","dark":"#000000"}}]}`, "children[0].tint"},
		{"not an object", `[1,2]`, ""},
		{"not json", `{`, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := Validate(json.RawMessage(tc.body))
			if err == nil {
				t.Fatal("accepted")
			}
			if got := fieldOf(t, err); got != tc.path {
				t.Fatalf("path %q, want %q (%v)", got, tc.path, err)
			}
		})
	}
}

func TestDepthLimit(t *testing.T) {
	if err := Validate(json.RawMessage(nested(8))); err != nil {
		t.Fatalf("8 deep rejected: %v", err)
	}
	err := Validate(json.RawMessage(nested(9)))
	if err == nil {
		t.Fatal("9 deep accepted")
	}
	if p := fieldOf(t, err); !strings.HasSuffix(p, "children[0]") && !strings.Contains(p, "children[0]") {
		t.Fatalf("path %q", p)
	}
}

func TestNodeLimit(t *testing.T) {
	build := func(n int) string {
		kids := make([]string, n-1)
		for i := range kids {
			kids[i] = `{"type":"spacer"}`
		}
		return fmt.Sprintf(`{"type":"vstack","children":[%s]}`, strings.Join(kids, ","))
	}
	if err := Validate(json.RawMessage(build(64))); err != nil {
		t.Fatalf("64 nodes rejected: %v", err)
	}
	err := Validate(json.RawMessage(build(65)))
	if err == nil {
		t.Fatal("65 nodes accepted")
	}
	if p := fieldOf(t, err); p != "children[64]" && p != "children[63]" {
		t.Fatalf("path %q", p)
	}
}

func TestTestdataExamplesAreValid(t *testing.T) {
	files, err := filepath.Glob("testdata/*.json")
	if err != nil || len(files) == 0 {
		t.Fatalf("no testdata: %v", err)
	}
	for _, f := range files {
		if filepath.Base(f) == "cases.json" {
			continue
		}
		body, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if err := Validate(body); err != nil {
			t.Errorf("%s: %v", f, err)
		}
	}
}

// wrap puts a node inside a vstack at index 1 so the path is children[1].<prop>.
func wrap(node string) string {
	return `{"type":"vstack","children":[{"type":"spacer"},` + node + `]}`
}

func TestPropertyValidation(t *testing.T) {
	long := strings.Repeat("a", 513)
	ok512 := strings.Repeat("a", 512)
	many := strings.Repeat("{{a}}", 17)
	sixteen := strings.Repeat("{{a}}", 16)
	accepted := []string{
		`{"type":"vstack","spacing":0,"align":"leading","children":[]}`,
		`{"type":"hstack","spacing":64,"align":"trailing","children":[]}`,
		`{"type":"vstack","spacing":{"bind":"gap"},"children":[]}`,
		`{"type":"spacer","min":256}`,
		`{"type":"text","text":"` + ok512 + `"}`,
		`{"type":"text","text":"` + sixteen + `"}`,
		`{"type":"text","text":"a {{items.0.name}} b {{x_y-z}}","lines":10,"align":"center","style":"caption","color":{"light":"#000000","dark":"#FFFFFF"}}`,
		`{"type":"text","lines":{"bind":"n"}}`,
		`{"type":"symbol","name":"bell","size":8}`,
		`{"type":"symbol","name":"{{icon}}","size":96,"color":{"light":"#000000","dark":"#FFFFFF"}}`,
		`{"type":"image","url":"https://x/y.png","width":512,"height":0,"fit":"fit","radius":64}`,
		`{"type":"image","url":"{{img}}"}`,
		`{"type":"image","url":"https://x/{{img}}.png","width":{"bind":"w"}}`,
		`{"type":"progress","value":0}`,
		`{"type":"progress","value":1,"label":"{{l}}"}`,
		`{"type":"progress","value":{"bind":"p"}}`,
		`{"type":"gauge","value":-5,"min":-10,"max":1000.5,"label":"x"}`,
		`{"type":"gauge","value":{"bind":"v"},"min":{"bind":"a"},"max":{"bind":"b"}}`,
		`{"type":"timer","to":1700000000,"mode":"countup","style":"title"}`,
		`{"type":"timer","to":{"bind":"eta"}}`,
		`{"type":"badge","text":"NEW","symbol":"star"}`,
		`{"schema":1,"type":"spacer"}`,
		`{"schema":2,"type":"spacer"}`,
	}
	for _, body := range accepted {
		if err := Validate(json.RawMessage(body)); err != nil {
			t.Errorf("rejected %s: %v", body, err)
		}
	}

	rejected := []struct{ name, body, path string }{
		{"unknown on stack", wrap(`{"type":"vstack","children":[],"colour":"red"}`), "children[1].colour"},
		{"unknown on text", wrap(`{"type":"text","colour":"red"}`), "children[1].colour"},
		{"text.value is not a property", wrap(`{"type":"text","value":"Hi"}`), "children[1].value"},
		{"color on stack", wrap(`{"type":"vstack","children":[],"color":{"light":"#000000","dark":"#000000"}}`), "children[1].color"},
		{"unknown on spacer", wrap(`{"type":"spacer","size":3}`), "children[1].size"},
		{"unknown on symbol", wrap(`{"type":"symbol","url":"x"}`), "children[1].url"},
		{"unknown on image", wrap(`{"type":"image","src":"https://x"}`), "children[1].src"},
		{"unknown on progress", wrap(`{"type":"progress","max":1}`), "children[1].max"},
		{"unknown on gauge", wrap(`{"type":"gauge","lines":1}`), "children[1].lines"},
		{"unknown on timer", wrap(`{"type":"timer","text":"x"}`), "children[1].text"},
		{"unknown on badge", wrap(`{"type":"badge","style":"body"}`), "children[1].style"},
		{"unknown reported first alphabetically", wrap(`{"type":"text","zzz":1,"aaa":2}`), "children[1].aaa"},
		{"spacing too big", wrap(`{"type":"hstack","children":[],"spacing":65}`), "children[1].spacing"},
		{"spacing negative", wrap(`{"type":"hstack","children":[],"spacing":-1}`), "children[1].spacing"},
		{"spacing string", wrap(`{"type":"hstack","children":[],"spacing":"8"}`), "children[1].spacing"},
		{"align invalid", wrap(`{"type":"vstack","children":[],"align":"left"}`), "children[1].align"},
		{"text align invalid", wrap(`{"type":"text","align":"start"}`), "children[1].align"},
		{"spacer min too big", wrap(`{"type":"spacer","min":257}`), "children[1].min"},
		{"text too long", wrap(`{"type":"text","text":"` + long + `"}`), "children[1].text"},
		{"text not string", wrap(`{"type":"text","text":4}`), "children[1].text"},
		{"lines zero", wrap(`{"type":"text","lines":0}`), "children[1].lines"},
		{"lines eleven", wrap(`{"type":"text","lines":11}`), "children[1].lines"},
		{"lines fractional", wrap(`{"type":"text","lines":2.5}`), "children[1].lines"},
		{"symbol size small", wrap(`{"type":"symbol","size":7}`), "children[1].size"},
		{"symbol size big", wrap(`{"type":"symbol","size":97}`), "children[1].size"},
		{"symbol name number", wrap(`{"type":"symbol","name":1}`), "children[1].name"},
		{"image http", wrap(`{"type":"image","url":"http://x/y.png"}`), "children[1].url"},
		{"image no scheme", wrap(`{"type":"image","url":"x/y.png"}`), "children[1].url"},
		{"image data url", wrap(`{"type":"image","url":"data:image/png;base64,AA=="}`), "children[1].url"},
		{"image width big", wrap(`{"type":"image","url":"https://x","width":513}`), "children[1].width"},
		{"image height negative", wrap(`{"type":"image","url":"https://x","height":-1}`), "children[1].height"},
		{"image fit invalid", wrap(`{"type":"image","url":"https://x","fit":"stretch"}`), "children[1].fit"},
		{"image radius big", wrap(`{"type":"image","url":"https://x","radius":65}`), "children[1].radius"},
		{"progress above one", wrap(`{"type":"progress","value":1.01}`), "children[1].value"},
		{"progress negative", wrap(`{"type":"progress","value":-0.1}`), "children[1].value"},
		{"progress string", wrap(`{"type":"progress","value":"0.5"}`), "children[1].value"},
		{"progress label number", wrap(`{"type":"progress","label":3}`), "children[1].label"},
		{"gauge value string", wrap(`{"type":"gauge","value":"x"}`), "children[1].value"},
		{"gauge max bool", wrap(`{"type":"gauge","max":true}`), "children[1].max"},
		{"timer to negative", wrap(`{"type":"timer","to":-1}`), "children[1].to"},
		{"timer to string", wrap(`{"type":"timer","to":"soon"}`), "children[1].to"},
		{"timer style invalid", wrap(`{"type":"timer","style":"huge"}`), "children[1].style"},
		{"badge text number", wrap(`{"type":"badge","text":1}`), "children[1].text"},
		{"badge symbol number", wrap(`{"type":"badge","symbol":1}`), "children[1].symbol"},
		{"bind empty", wrap(`{"type":"progress","value":{"bind":""}}`), "children[1].value.bind"},
		{"bind with space", wrap(`{"type":"progress","value":{"bind":"a b"}}`), "children[1].value.bind"},
		{"bind with brace", wrap(`{"type":"progress","value":{"bind":"{{a}}"}}`), "children[1].value.bind"},
		{"bind with slash", wrap(`{"type":"progress","value":{"bind":"a/b"}}`), "children[1].value.bind"},
		{"bind not string", wrap(`{"type":"progress","value":{"bind":3}}`), "children[1].value"},
		{"bind extra key", wrap(`{"type":"progress","value":{"bind":"a","x":1}}`), "children[1].value"},
		{"empty object", wrap(`{"type":"progress","value":{}}`), "children[1].value"},
		{"bind on string prop", wrap(`{"type":"text","text":{"bind":"a"}}`), "children[1].text"},
		{"unclosed placeholder", wrap(`{"type":"text","text":"a {{b"}`), "children[1].text"},
		{"empty placeholder", wrap(`{"type":"text","text":"{{}}"}`), "children[1].text"},
		{"nested placeholder", wrap(`{"type":"text","text":"{{{{x}}}}"}`), "children[1].text"},
		{"nested placeholder inner", wrap(`{"type":"text","text":"{{a{{b}}}}"}`), "children[1].text"},
		{"placeholder with space", wrap(`{"type":"text","text":"{{ a }}"}`), "children[1].text"},
		{"placeholder with expression", wrap(`{"type":"text","text":"{{a|upper}}"}`), "children[1].text"},
		{"too many placeholders", wrap(`{"type":"text","text":"` + many + `"}`), "children[1].text"},
		{"placeholder in label", wrap(`{"type":"gauge","label":"{{"}`), "children[1].label"},
		{"schema zero", `{"schema":0,"type":"spacer"}`, "schema"},
		{"schema fractional", `{"schema":1.5,"type":"spacer"}`, "schema"},
		{"schema string", `{"schema":"1","type":"spacer"}`, "schema"},
		{"schema nested", wrap(`{"type":"spacer","schema":1}`), "children[1].schema"},
	}
	for _, tc := range rejected {
		t.Run(tc.name, func(t *testing.T) {
			err := Validate(json.RawMessage(tc.body))
			if err == nil {
				t.Fatal("accepted")
			}
			if got := fieldOf(t, err); got != tc.path {
				t.Fatalf("path %q, want %q (%v)", got, tc.path, err)
			}
		})
	}
}

func TestUnknownPropertyMessage(t *testing.T) {
	err := Validate(json.RawMessage(wrap(`{"type":"text","colour":"red"}`)))
	var fe *FieldError
	if !errors.As(err, &fe) || fe.Path != "children[1].colour" || fe.Msg != "unknown property" {
		t.Fatalf("got %#v", err)
	}
}

func TestForeach(t *testing.T) {
	row := `{"type":"hstack","children":[{"type":"text","text":"{{item.title}}"},{"type":"spacer"},{"type":"text","text":"{{index}}","style":"caption"},{"type":"gauge","value":{"bind":"item.value"}}]}`
	accepted := []string{
		`{"type":"foreach","each":"items","body":` + row + `}`,
		`{"type":"foreach","each":"a.b-c_d.0","max":1,"axis":"horizontal","spacing":0,"body":{"type":"text"}}`,
		`{"type":"foreach","each":"items","max":12,"axis":"vertical","spacing":64,"body":{"type":"text"}}`,
		`{"type":"foreach","each":"items","spacing":{"bind":"gap"},"max":{"bind":"n"},"body":{"type":"spacer"}}`,
		`{"type":"vstack","children":[{"type":"text","text":"{{title}}"},{"type":"foreach","each":"items","body":` + row + `}]}`,
		`{"type":"foreach","tint":{"light":"#000000","dark":"#FFFFFF"},"each":"items","body":{"type":"text"}}`,
	}
	for _, body := range accepted {
		if err := Validate(json.RawMessage(body)); err != nil {
			t.Errorf("rejected %s: %v", body, err)
		}
	}

	rejected := []struct{ name, body, path string }{
		{"missing each", wrap(`{"type":"foreach","body":{"type":"text"}}`), "children[1].each"},
		{"empty each", wrap(`{"type":"foreach","each":"","body":{"type":"text"}}`), "children[1].each"},
		{"each not string", wrap(`{"type":"foreach","each":3,"body":{"type":"text"}}`), "children[1].each"},
		{"each placeholder", wrap(`{"type":"foreach","each":"{{items}}","body":{"type":"text"}}`), "children[1].each"},
		{"each with space", wrap(`{"type":"foreach","each":"a b","body":{"type":"text"}}`), "children[1].each"},
		{"each with slash", wrap(`{"type":"foreach","each":"a/b","body":{"type":"text"}}`), "children[1].each"},
		{"each bind object", wrap(`{"type":"foreach","each":{"bind":"items"},"body":{"type":"text"}}`), "children[1].each"},
		{"missing body", wrap(`{"type":"foreach","each":"items"}`), "children[1].body"},
		{"body not object", wrap(`{"type":"foreach","each":"items","body":[]}`), "children[1].body"},
		{"body is string", wrap(`{"type":"foreach","each":"items","body":"x"}`), "children[1].body"},
		{"body unknown type", wrap(`{"type":"foreach","each":"items","body":{"type":"web"}}`), "children[1].body.type"},
		{"body bad prop", wrap(`{"type":"foreach","each":"items","body":{"type":"text","colour":"red"}}`), "children[1].body.colour"},
		{"body nested bad child", wrap(`{"type":"foreach","each":"items","body":{"type":"vstack","children":[{"type":"text"},{"type":"x"}]}}`), "children[1].body.children[1].type"},
		{"body is foreach", wrap(`{"type":"foreach","each":"items","body":{"type":"foreach","each":"x","body":{"type":"text"}}}`), "children[1].body"},
		{"foreach nested in body container", wrap(`{"type":"foreach","each":"items","body":{"type":"vstack","children":[{"type":"text"},{"type":"foreach","each":"x","body":{"type":"text"}}]}}`), "children[1].body.children[1]"},
		{"foreach deep in body", wrap(`{"type":"foreach","each":"items","body":{"type":"vstack","children":[{"type":"hstack","children":[{"type":"foreach","each":"x","body":{"type":"text"}}]}]}}`), "children[1].body.children[0].children[0]"},
		{"max zero", wrap(`{"type":"foreach","each":"items","max":0,"body":{"type":"text"}}`), "children[1].max"},
		{"max thirteen", wrap(`{"type":"foreach","each":"items","max":13,"body":{"type":"text"}}`), "children[1].max"},
		{"max fractional", wrap(`{"type":"foreach","each":"items","max":2.5,"body":{"type":"text"}}`), "children[1].max"},
		{"max string", wrap(`{"type":"foreach","each":"items","max":"3","body":{"type":"text"}}`), "children[1].max"},
		{"axis invalid", wrap(`{"type":"foreach","each":"items","axis":"diagonal","body":{"type":"text"}}`), "children[1].axis"},
		{"spacing too big", wrap(`{"type":"foreach","each":"items","spacing":65,"body":{"type":"text"}}`), "children[1].spacing"},
		{"spacing negative", wrap(`{"type":"foreach","each":"items","spacing":-1,"body":{"type":"text"}}`), "children[1].spacing"},
		{"unknown prop", wrap(`{"type":"foreach","each":"items","limit":3,"body":{"type":"text"}}`), "children[1].limit"},
		{"children not allowed", wrap(`{"type":"foreach","each":"items","body":{"type":"text"},"children":[]}`), "children[1].children"},
		{"align not allowed", wrap(`{"type":"foreach","each":"items","align":"leading","body":{"type":"text"}}`), "children[1].align"},
		{"body on stack", wrap(`{"type":"vstack","children":[],"body":{"type":"text"}}`), "children[1].body"},
		{"body on text", wrap(`{"type":"text","body":{"type":"text"}}`), "children[1].body"},
		{"each on stack", wrap(`{"type":"vstack","children":[],"each":"items"}`), "children[1].each"},
		{"malformed placeholder in body", wrap(`{"type":"foreach","each":"items","body":{"type":"text","text":"{{item."}}`), "children[1].body.text"},
		{"body color bad", wrap(`{"type":"foreach","each":"items","body":{"type":"text","color":{"light":"x","dark":"#000000"}}}`), "children[1].body.color.light"},
		{"nested tint in body", wrap(`{"type":"foreach","each":"items","body":{"type":"text","tint":{"light":"#000000","dark":"#000000"}}}`), "children[1].body.tint"},
	}
	for _, tc := range rejected {
		t.Run(tc.name, func(t *testing.T) {
			err := Validate(json.RawMessage(tc.body))
			if err == nil {
				t.Fatal("accepted")
			}
			if got := fieldOf(t, err); got != tc.path {
				t.Fatalf("path %q, want %q (%v)", got, tc.path, err)
			}
		})
	}
}

func TestForeachDepthAndNodeAccounting(t *testing.T) {
	// The body counts once: foreach (1) + 63 spacers inside one vstack body (1+62) = 64.
	bodyOf := func(n int) string {
		kids := make([]string, n-1)
		for i := range kids {
			kids[i] = `{"type":"spacer"}`
		}
		return fmt.Sprintf(`{"type":"vstack","children":[%s]}`, strings.Join(kids, ","))
	}
	if err := Validate(json.RawMessage(`{"type":"foreach","each":"items","max":12,"body":` + bodyOf(63) + `}`)); err != nil {
		t.Fatalf("64 nodes (body counted once) rejected: %v", err)
	}
	err := Validate(json.RawMessage(`{"type":"foreach","each":"items","max":12,"body":` + bodyOf(64) + `}`))
	if err == nil {
		t.Fatal("65 nodes accepted")
	}
	if p := fieldOf(t, err); p != "body.children[62]" {
		t.Fatalf("path %q", p)
	}

	// The body is one level deeper than the foreach: foreach at depth 1, body chain to depth 8.
	if err := Validate(json.RawMessage(`{"type":"foreach","each":"items","body":` + nested(7) + `}`)); err != nil {
		t.Fatalf("8 deep rejected: %v", err)
	}
	err = Validate(json.RawMessage(`{"type":"foreach","each":"items","body":` + nested(8) + `}`))
	if err == nil {
		t.Fatal("9 deep accepted")
	}
	if p := fieldOf(t, err); !strings.HasPrefix(p, "body.children[0]") {
		t.Fatalf("path %q", p)
	}
}

// TestSharedInvalidCases checks every case in testdata/cases.json, the list the
// website's validator is also tested against.
func TestSharedInvalidCases(t *testing.T) {
	raw, err := os.ReadFile("testdata/cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Name string          `json:"name"`
		Body json.RawMessage `json:"body"`
		Path string          `json:"path"`
		Msg  string          `json:"msg"`
	}
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	if len(cases) < 30 {
		t.Fatalf("only %d cases", len(cases))
	}
	for _, c := range cases {
		err := Validate(c.Body)
		var fe *FieldError
		if !errors.As(err, &fe) {
			t.Errorf("%s: want a FieldError, got %v", c.Name, err)
			continue
		}
		if fe.Path != c.Path || fe.Msg != c.Msg {
			t.Errorf("%s: got %q / %q, want %q / %q", c.Name, fe.Path, fe.Msg, c.Path, c.Msg)
		}
	}
}
