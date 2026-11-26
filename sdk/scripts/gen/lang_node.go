package main

import (
	"regexp"
	"strings"
	"text/template"
)

func init() {
	langs = append(langs, lang{
		name: "node", out: "node/src/types_gen.ts", tmpl: "node.tmpl",
		funcs:    template.FuncMap{"tsType": tsType, "tsFrom": tsFrom, "tsTo": tsTo, "tsDoc": tsDoc, "tsOptional": tsOptional},
		manifest: []manifestVersion{{"node/package.json", regexp.MustCompile(`(?m)^  "version": "([^"]+)"`)}},
	})
}

func tsType(t *TypeRef) string {
	switch t.Kind {
	case "string":
		return "string"
	case "int", "int64":
		if len(t.Values) > 0 {
			return strings.Join(t.Values, " | ")
		}
		return "number"
	case "bool":
		return "boolean"
	case "time":
		return "Date"
	case "any":
		return "unknown"
	case "array":
		e := tsType(t.Elem)
		if strings.Contains(e, " ") {
			e = "(" + e + ")"
		}
		return e + "[]"
	case "enum", "object":
		return t.Ref
	}
	panic("node: no type for " + t.Kind)
}

// tsOptional: a field the caller may leave out. Read-only fields are only ever set by the hub.
func tsOptional(f *Field) bool { return !f.Required || f.ReadOnly }

// tsFrom converts the wire value e (unknown) to the SDK type.
func tsFrom(t *TypeRef, e string) string {
	switch t.Kind {
	case "time":
		return "new Date(" + e + " as string)"
	case "object":
		return "fromWire" + t.Ref + "(" + e + ")"
	case "array":
		if t.Elem.Kind == "object" || t.Elem.Kind == "time" {
			return "(" + e + " as unknown[]).map((x) => " + tsFrom(t.Elem, "x") + ")"
		}
	case "any":
		return e
	}
	return e + " as " + tsType(t)
}

// tsTo converts the SDK value e to its wire form.
func tsTo(t *TypeRef, e string) string {
	switch t.Kind {
	case "time":
		return e + ".toISOString()"
	case "object":
		return "toWire" + t.Ref + "(" + e + ")"
	case "array":
		if t.Elem.Kind == "object" || t.Elem.Kind == "time" {
			return e + ".map((x) => " + tsTo(t.Elem, "x") + ")"
		}
	}
	return e
}

func tsDoc(indent, desc string) string {
	if desc == "" {
		return ""
	}
	if len(indent)+len(desc) < 110 {
		return indent + "/** " + desc + " */\n"
	}
	return indent + "/**\n" + wrap(indent+" * ", desc) + "\n" + indent + " */\n"
}
