package main

import (
	"go/format"
	"regexp"
	"strings"
	"text/template"
)

func init() {
	langs = append(langs, lang{
		name: "go", out: "go/types_gen.go", tmpl: "go.tmpl", format: format.Source,
		funcs: template.FuncMap{"goName": goName, "goType": goType, "goTag": goTag, "goDoc": goDoc},
	})
	_ = regexp.MustCompile // Go's version lives in the generated file, not in go.mod
}

// goInitialisms are the words Go spells in capitals.
var goInitialisms = map[string]string{"id": "ID", "ids": "IDs", "url": "URL", "qr": "QR", "sha256": "SHA256"}

func goName(s string) string {
	var b strings.Builder
	for _, w := range words(s) {
		if v, ok := goInitialisms[w]; ok {
			b.WriteString(v)
		} else {
			b.WriteString(strings.ToUpper(w[:1]) + w[1:])
		}
	}
	return b.String()
}

func goType(t *TypeRef, required bool) string {
	switch t.Kind {
	case "string":
		return "string"
	case "int":
		return "int"
	case "int64":
		return "int64"
	case "bool":
		return "bool"
	case "time":
		return "time.Time"
	case "any":
		return "any"
	case "array":
		return "[]" + goType(t.Elem, true)
	case "enum":
		return t.Ref
	case "object":
		if required {
			return t.Ref
		}
		return "*" + t.Ref
	}
	panic("go: no type for " + t.Kind)
}

// goTag omits an optional field at its zero value, so an unset field is not sent.
func goTag(f *Field) string {
	if f.Header != "" {
		return "\x60json:\"-\"\x60"
	}
	opt := ""
	if !f.Required || f.ReadOnly {
		opt = ",omitempty"
		if f.T.Kind == "time" {
			opt = ",omitzero"
		}
	}
	return "\x60json:\"" + f.Wire + opt + "\"\x60"
}

// goDoc starts a declaration's comment with its name, as Go wants: "Channel groups ...", "Message is a ...".
func goDoc(name, desc string) string {
	if desc == "" {
		return ""
	}
	first, rest, _ := strings.Cut(desc, " ")
	// ponytail: a description starting with a verb ("Groups", "Starts") reads on from the name; any other
	// starts after "is". Phrase descriptions to fit.
	if strings.HasSuffix(first, "s") && first != "Its" && first != "This" {
		return wrap("// ", name+" "+strings.ToLower(first[:1])+first[1:]+" "+rest)
	}
	return wrap("// ", name+" is "+strings.ToLower(desc[:1])+desc[1:])
}
