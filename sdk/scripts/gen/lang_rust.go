package main

import (
	"fmt"
	"regexp"
	"strings"
	"text/template"
)

func init() {
	langs = append(langs, lang{
		name: "rust", out: "rust/src/types_gen.rs", tmpl: "rust.tmpl", format: rsFormat,
		funcs: template.FuncMap{
			"rsDoc": rsDoc, "rsDerive": rsDerive, "rsField": rsField, "rsDefaultImpl": rsDefaultImpl,
			"rsTolerant": rsTolerant, "rsDefault": rsDefault, "rsVariant": pascal, "rsDecoded": rsDecoded,
		},
		// Only the [package] version starts in column 0; a dependency's "version = " is inline in a table.
		manifest: []manifestVersion{{"rust/Cargo.toml", regexp.MustCompile("(?m)^version = \"([^\"]+)\"$")}},
	})
}

// rsEnumDefault names the variant an enum's Default is when it is not the first one: a channel without a level
// uses the hub's default level, not the quietest.
var rsEnumDefault = map[string]string{"Level": "active"}

// rsKeywords are escaped as raw identifiers.
var rsKeywords = map[string]bool{"as": true, "break": true, "const": true, "continue": true, "crate": true, "else": true,
	"enum": true, "extern": true, "false": true, "fn": true, "for": true, "if": true, "impl": true, "in": true, "let": true,
	"loop": true, "match": true, "mod": true, "move": true, "mut": true, "pub": true, "ref": true, "return": true,
	"self": true, "static": true, "struct": true, "super": true, "trait": true, "true": true, "type": true, "unsafe": true,
	"use": true, "where": true, "while": true, "async": true, "await": true, "dyn": true}

// rsInfo says which way each type travels. A type reachable from a request body is sent (it derives Serialize and its
// optional fields are Option); a type reachable from an answer or a webhook is decoded (it derives Deserialize, and
// an enum in it keeps a value it does not know).
type rsInfo struct {
	types           map[string]*Type
	sent, decoded   map[string]bool
	hasAny          map[string]bool
	enumSent, enumD map[string]bool
}

var rsCache = map[*Spec]*rsInfo{}

func rsOf(s *Spec) *rsInfo {
	if i := rsCache[s]; i != nil {
		return i
	}
	i := &rsInfo{types: map[string]*Type{}, sent: map[string]bool{}, decoded: map[string]bool{}, hasAny: map[string]bool{},
		enumSent: map[string]bool{}, enumD: map[string]bool{}}
	for _, t := range s.Types {
		i.types[t.Name] = t
	}
	var walk func(t *TypeRef, types, enums map[string]bool)
	var walkType func(name string, types, enums map[string]bool)
	walk = func(t *TypeRef, types, enums map[string]bool) {
		if t == nil {
			return
		}
		switch t.Kind {
		case "array":
			walk(t.Elem, types, enums)
		case "enum":
			enums[t.Ref] = true
		case "object":
			walkType(t.Ref, types, enums)
		}
	}
	walkType = func(name string, types, enums map[string]bool) {
		if types[name] {
			return
		}
		types[name] = true
		for _, f := range i.types[name].Fields {
			walk(f.T, types, enums)
		}
	}
	for _, o := range s.Ops {
		if o.BodyRef != "" {
			walkType(o.BodyRef, i.sent, i.enumSent)
		}
		walk(o.Result, i.decoded, i.enumD)
	}
	for _, e := range s.Events {
		walkType(e.Type, i.decoded, i.enumD)
	}
	var any func(name string) bool
	any = func(name string) bool {
		if v, ok := i.hasAny[name]; ok {
			return v
		}
		i.hasAny[name] = false
		for _, f := range i.types[name].Fields {
			t := f.T
			for t.Kind == "array" {
				t = t.Elem
			}
			if t.Kind == "any" || (t.Kind == "object" && any(t.Ref)) {
				i.hasAny[name] = true
			}
		}
		return i.hasAny[name]
	}
	for n := range i.types {
		any(n)
	}
	rsCache[s] = i
	return i
}

func rsDecoded(s *Spec, name string) bool {
	i := rsOf(s)
	return i.decoded[name] || !i.sent[name]
}

// rsTolerant: an enum an answer carries keeps a value it does not know as Other(String); an enum only ever sent is strict.
func rsTolerant(s *Spec, name string) bool {
	i := rsOf(s)
	return i.enumD[name] || !i.enumSent[name]
}

func rsDefault(e *Enum) string {
	if v, ok := rsEnumDefault[e.Name]; ok {
		return v
	}
	return e.Values[0]
}

// rsDoc turns a description into doc lines, the name when there is none (rustc's missing_docs wants one).
func rsDoc(indent, desc, fallback string) string {
	if desc == "" {
		desc = "The `" + fallback + "`."
	}
	return wrap(indent+"/// ", desc)
}

func rsDerive(s *Spec, t *Type) string {
	i := rsOf(s)
	d := []string{"Debug", "Clone", "PartialEq"}
	if !i.hasAny[t.Name] {
		d = append(d, "Eq")
	}
	if !rsManualDefault(t) {
		d = append(d, "Default")
	}
	if i.sent[t.Name] {
		d = append(d, "Serialize")
	}
	if rsDecoded(s, t.Name) {
		d = append(d, "Deserialize")
	}
	return strings.Join(d, ", ")
}

// rsManualDefault: SystemTime has no Default, so a type with a required timestamp writes its own.
func rsManualDefault(t *Type) bool {
	for _, f := range t.Fields {
		if f.T.Kind == "time" && f.Required && f.Header == "" {
			return true
		}
	}
	return false
}

func rsDefaultImpl(t *Type) string {
	if !rsManualDefault(t) {
		return ""
	}
	var parts []string
	for _, f := range t.Fields {
		v := "Default::default()"
		if f.T.Kind == "time" && f.Required {
			v = "SystemTime::UNIX_EPOCH"
		}
		parts = append(parts, rsName(f)+": "+v)
	}
	one := "        " + t.Name + " { " + strings.Join(parts, ", ") + " }"
	body := one
	if len(one) > 140 {
		body = "        " + t.Name + " {\n            " + strings.Join(parts, ",\n            ") + ",\n        }"
	}
	return "\nimpl Default for " + t.Name + " {\n    fn default() -> Self {\n" + body + "\n    }\n}\n"
}

func rsName(f *Field) string {
	n := snake(f.Name)
	if rsKeywords[n] {
		return "r#" + n
	}
	return n
}

func rsBase(t *TypeRef) string {
	switch t.Kind {
	case "string":
		return "String"
	case "int":
		if len(t.Values) > 0 {
			return "u8" // the inline integer enum (5 or 10)
		}
		return "u32"
	case "int64":
		return "i64"
	case "number":
		return "f64"
	case "bool":
		return "bool"
	case "time":
		return "SystemTime"
	case "any":
		return "Value"
	case "array":
		return "Vec<" + rsBase(t.Elem) + ">"
	case "enum", "object":
		return t.Ref
	}
	panic("rust: no type for " + t.Kind)
}

// rsField is one struct field: doc, serde attributes and declaration. A required field is its plain type. An
// optional one is an Option in a type you build (so an unset field is not sent) and, for a timestamp, an object or
// any JSON, in an answer; any other optional field of an answer reads as its zero value when the hub leaves it out.
func rsField(s *Spec, t *Type, f *Field) string {
	i := rsOf(s)
	sent, dec := i.sent[t.Name], rsDecoded(s, t.Name)
	ty := rsBase(f.T)
	var attrs []string
	name := rsName(f)
	if r := strings.TrimPrefix(name, "r#"); r != f.Wire {
		attrs = append(attrs, fmt.Sprintf("rename = %q", f.Wire))
	}
	skipNone := func() {
		if f.ReadOnly {
			attrs = append(attrs, "skip_serializing")
		} else if sent {
			attrs = append(attrs, "skip_serializing_if = \"Option::is_none\"")
		}
	}
	switch {
	case f.Header != "":
		// Sent as a request header, never in the body.
		ty = "Option<" + ty + ">"
		attrs = []string{"skip"}
	case f.T.Kind == "array":
		if !f.Required && sent {
			attrs = append(attrs, "skip_serializing_if = \"Vec::is_empty\"")
		}
		if dec {
			attrs = append(attrs, "deserialize_with = \"null_as_default\"")
		}
	case f.T.Kind == "time":
		if sent {
			panic("rust: " + t.Name + "." + f.Wire + ": a timestamp in a request is not supported")
		}
		if f.Required {
			attrs = append(attrs, "deserialize_with = \"time_required\"")
		} else {
			ty = "Option<" + ty + ">"
			attrs = append(attrs, "deserialize_with = \"time_optional\"")
		}
	case f.Required:
	case sent || f.T.Kind == "object" || f.T.Kind == "any":
		ty = "Option<" + ty + ">"
		skipNone()
		if dec && f.T.Kind == "string" {
			attrs = append(attrs, "deserialize_with = \"empty_as_none\"")
		}
	default:
		if dec {
			attrs = append(attrs, "deserialize_with = \"null_as_default\"")
		}
	}
	var b strings.Builder
	b.WriteString(rsDoc("    ", f.Desc, f.Wire))
	b.WriteString("\n")
	if len(attrs) > 0 {
		b.WriteString("    #[serde(" + strings.Join(attrs, ", ") + ")]\n")
	}
	b.WriteString("    pub " + name + ": " + ty + ",\n")
	return strings.TrimRight(b.String(), "\n")
}

var rsBlank = regexp.MustCompile("\n{3,}")

// rsFormat keeps the template's blank lines rustfmt-sized: one at most, none at the end of a block, one newline at the end.
func rsFormat(b []byte) ([]byte, error) {
	s := rsBlank.ReplaceAllString(string(b), "\n\n")
	s = strings.ReplaceAll(s, "{\n\n", "{\n")
	s = strings.ReplaceAll(s, "\n\n}", "\n}")
	lines := strings.Split(s, "\n")
	for k, l := range lines {
		lines[k] = strings.TrimRight(l, " \t")
	}
	return []byte(strings.TrimRight(strings.Join(lines, "\n"), "\n") + "\n"), nil
}
