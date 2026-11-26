package main

import (
	"fmt"
	"regexp"
	"strings"
	"text/template"
)

func init() {
	langs = append(langs, lang{
		name: "ruby", out: "ruby/lib/hansho/types_gen.rb", tmpl: "ruby.tmpl",
		funcs: template.FuncMap{
			"rbName": rbName, "rbConst": rbConst, "rbDec": rbDec, "rbEnc": rbEnc, "rbDoc": rbDoc, "rbTypeDoc": rbTypeDoc,
			"rbWhat": rbWhat, "rbWired": rbWired, "rbMembers": rbMembers,
		},
		// The version stays in lib/hansho/version.rb, where the gemspec and the release workflow read it.
		manifest: []manifestVersion{{"ruby/lib/hansho/version.rb", regexp.MustCompile(`(?m)^\s*VERSION = "([^"]+)"`)}},
	})
}

// rbName is the member name of a field: snake_case of the spec name (x-name, else the wire name).
func rbName(s string) string { return snake(s) }

// rbConst is the constant of an enum value: "time-sensitive" -> TIME_SENSITIVE.
func rbConst(v string) string { return strings.ToUpper(snake(v)) }

// rbWhat names a type in an error message: PageOptions -> "page options".
func rbWhat(name string) string {
	var b strings.Builder
	for i, r := range name {
		if i > 0 && r >= 'A' && r <= 'Z' {
			b.WriteByte(' ')
		}
		b.WriteRune(r)
	}
	return strings.ToLower(b.String())
}

// rbWired reports whether a field travels in the JSON body (not a header, not set by the hub).
func rbWired(f *Field) bool { return f.Header == "" && !f.ReadOnly }

func rbMembers(t *Type) string {
	var m []string
	for _, f := range t.Fields {
		m = append(m, ":"+rbName(f.Name))
	}
	return strings.Join(m, ", ")
}

// rbDoc is a comment block of the description, indented by indent.
func rbDoc(indent, desc string) string {
	if desc == "" {
		return ""
	}
	return wrap(indent+"# ", desc) + "\n"
}

// rbTypeDoc is the comment of a type: its description, then one line per documented member.
func rbTypeDoc(t *Type) string {
	var b strings.Builder
	b.WriteString(rbDoc("  ", t.Desc))
	first := true
	for _, f := range t.Fields {
		if f.Desc == "" {
			continue
		}
		if first && t.Desc != "" {
			b.WriteString("  #\n")
		}
		first = false
		b.WriteString(wrap("  # ", fmt.Sprintf("+%s+:: %s", rbName(f.Name), f.Desc)) + "\n")
	}
	return b.String()
}

// rbDec is the Ruby expression that reads the wire value e as t. It never raises: a value of the wrong type is
// dropped, a missing required scalar is "" or 0, and an unknown enum value is kept as the string it is.
func rbDec(t *TypeRef, e string, required bool) string {
	req := fmt.Sprint(required)
	switch t.Kind {
	case "string", "enum":
		return "Wire.string(" + e + ", " + req + ")"
	case "int", "int64":
		return "Wire.integer(" + e + ", " + req + ")"
	case "bool":
		return "Wire.boolean(" + e + ", " + req + ")"
	case "time":
		return "Wire.time(" + e + ")"
	case "any":
		return e
	case "object":
		return "Wire.object(" + t.Ref + ", " + e + ")"
	case "array":
		return "Wire.list(" + e + ") { |x| " + rbDec(t.Elem, "x", false) + " }"
	}
	panic("ruby: no decoder for " + t.Kind)
}

// rbEnc is the Ruby expression that turns the member value e into its wire form. what names the member in errors.
func rbEnc(t *TypeRef, e, what string, optional bool) string {
	switch t.Kind {
	case "string", "int", "int64", "bool", "any":
		return e
	case "time":
		return "Wire.iso8601(" + e + ")"
	case "enum":
		return t.Ref + ".coerce(" + e + ")"
	case "object":
		return "Wire.input(" + t.Ref + ", " + e + ", " + fmt.Sprintf("%q", rbWhat(t.Ref)) + ")&.to_wire"
	case "array":
		return "Wire.items(" + e + ", " + fmt.Sprintf("%q", what) + ", " + fmt.Sprint(optional) + ") { |x| " + rbEnc(t.Elem, "x", what, false) + " }"
	}
	panic("ruby: no encoder for " + t.Kind)
}
