package main

import (
	"fmt"
	"regexp"
	"strings"
	"text/template"
)

func init() {
	langs = append(langs, lang{
		name: "dotnet", out: "dotnet/src/Hansho/Generated.cs", tmpl: "dotnet.tmpl",
		funcs: template.FuncMap{
			"csName": csName, "csProp": csProp, "csFieldType": csFieldType, "csRequired": csRequired, "csDoc": csDoc,
			"csDec": csDec, "csEnc": csEnc, "csReads": csReads, "csWrites": csWrites, "csHeaders": csHeaders,
			"csMember": csMember,
		},
		// The package version is in the csproj, where the release workflow checks it; HanshoClient.Version is
		// the generated HanshoSpec.Version.
		manifest: []manifestVersion{{"dotnet/src/Hansho/Hansho.csproj", regexp.MustCompile(`(?m)^\s*<Version>([^<]+)</Version>`)}},
	})
}

// csNames renames the spec types whose name would shadow a System type for every user of the namespace: Action
// is System.Action (and a "using System;" next to "using Hansho;" would make it ambiguous), so the button is
// MessageAction, as it has always been in this SDK.
var csNames = map[string]string{"Action": "MessageAction"}

func csName(n string) string {
	if r, ok := csNames[n]; ok {
		return r
	}
	return n
}

// csProp is the property name of a field: PascalCase of the spec name (x-name, else the wire name).
func csProp(f *Field) string { return pascal(f.Name) }

// csMember is the static member of an enum value: "time-sensitive" -> TimeSensitive.
func csMember(v string) string { return pascal(v) }

// csRequired: the caller must set it. A read-only field is only ever set by the hub, a header field is optional.
func csRequired(f *Field) bool {
	if f.Header != "" && f.Required {
		panic("dotnet: required header field " + f.Wire)
	}
	return f.Required && !f.ReadOnly
}

func csType(t *TypeRef) string {
	switch t.Kind {
	case "string":
		return "string"
	case "int":
		return "int"
	case "int64":
		return "long"
	case "bool":
		return "bool"
	case "time":
		return "DateTimeOffset"
	case "any":
		return "object"
	case "array":
		return "IReadOnlyList<" + csType(t.Elem) + ">"
	case "enum", "object":
		return csName(t.Ref)
	}
	panic("dotnet: no type for " + t.Kind)
}

func csFieldType(f *Field) string {
	if csRequired(f) {
		return csType(f.T)
	}
	return csType(f.T) + "?"
}

var (
	backticks = regexp.MustCompile("`([^`]*)`")
	xmlEscape = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")
)

// csDoc is the XML doc comment of a description: markdown code spans become <c>, indented by indent.
func csDoc(indent, desc string) string {
	if desc == "" {
		return ""
	}
	text := backticks.ReplaceAllString(xmlEscape.Replace(desc), "<c>$1</c>")
	if len(indent)+len("/// <summary></summary>")+len(text) <= 116 {
		return indent + "/// <summary>" + text + "</summary>\n"
	}
	return indent + "/// <summary>\n" + wrap(indent+"/// ", text) + "\n" + indent + "/// </summary>\n"
}

// csRefs lists the object types t refers to.
func csRefs(t *TypeRef) []string {
	switch t.Kind {
	case "object":
		return []string{t.Ref}
	case "array":
		return csRefs(t.Elem)
	}
	return nil
}

// csClosure returns the types reachable from roots through their fields, in the spec's order.
func csClosure(s *Spec, roots []string) []*Type {
	byName := map[string]*Type{}
	for _, t := range s.Types {
		byName[t.Name] = t
	}
	seen := map[string]bool{}
	var visit func(string)
	visit = func(n string) {
		if seen[n] || byName[n] == nil {
			return
		}
		seen[n] = true
		for _, f := range byName[n].Fields {
			for _, r := range csRefs(f.T) {
				visit(r)
			}
		}
	}
	for _, r := range roots {
		visit(r)
	}
	var out []*Type
	for _, t := range s.Types {
		if seen[t.Name] {
			out = append(out, t)
		}
	}
	return out
}

// csReads: the types the SDK reads, the answers of the operations and the webhook payloads, with what they hold.
func csReads(s *Spec) []*Type {
	var roots []string
	for _, o := range s.Ops {
		if o.Result != nil && o.Result.Kind != "inline" {
			roots = append(roots, csRefs(o.Result)...)
		}
	}
	for _, e := range s.Events {
		roots = append(roots, e.Type)
	}
	return csClosure(s, roots)
}

// csWrites: the types the SDK sends, the request bodies of the operations, with what they hold.
func csWrites(s *Spec) []*Type {
	var roots []string
	for _, o := range s.Ops {
		if o.BodyRef != "" {
			roots = append(roots, o.BodyRef)
		}
	}
	return csClosure(s, roots)
}

// csHeaders: the header fields of a type.
func csHeaders(t *Type) []*Field {
	var out []*Field
	for _, f := range t.Fields {
		if f.Header != "" {
			if f.T.Kind != "string" {
				panic("dotnet: header field " + f.Wire + " must be a string")
			}
			out = append(out, f)
		}
	}
	return out
}

// csItem is how Wire.Items reads one array element: the JSON kind it accepts and the function that converts it.
func csItem(t *TypeRef) (kind, fn string) {
	switch t.Kind {
	case "string":
		return "String", "static e => e.GetString()!"
	case "enum":
		return "String", "static e => new " + csName(t.Ref) + "(e.GetString()!)"
	case "int":
		return "Number", "static e => AsInt(e)"
	case "int64":
		return "Number", "static e => AsLong(e)"
	case "object":
		return "Object", "Read" + csName(t.Ref)
	case "any":
		return "Undefined", "static e => (object)e.Clone()"
	}
	panic("dotnet: no array reader for " + t.Kind)
}

// csDec is the C# expression that reads field f from the wire object w. It never throws: a value of the wrong
// type reads as absent, a missing required scalar as "" or 0, and an unknown enum value is kept as sent.
func csDec(f *Field) string {
	key := fmt.Sprintf("%q", f.Wire)
	req := csRequired(f)
	t := f.T
	switch t.Kind {
	case "string":
		if req {
			return "Str(w, " + key + ")"
		}
		return "OptStr(w, " + key + ")"
	case "enum":
		if req {
			return "new " + csName(t.Ref) + "(Str(w, " + key + "))"
		}
		return "OptEnum(w, " + key + ", static s => new " + csName(t.Ref) + "(s))"
	case "int", "int64", "bool":
		name := map[string]string{"int": "Int", "int64": "Long", "bool": "Bool"}[t.Kind]
		if req {
			return name + "(w, " + key + ")"
		}
		return "Opt" + name + "(w, " + key + ")"
	case "time":
		if req {
			return "Time(w, " + key + ") ?? default"
		}
		return "Time(w, " + key + ")"
	case "any":
		if req {
			return "Any(w, " + key + ") ?? default(JsonElement)"
		}
		return "Any(w, " + key + ")"
	case "object":
		if req {
			return "Read" + csName(t.Ref) + "(Sub(w, " + key + "))"
		}
		return "OptObj(w, " + key + ", Read" + csName(t.Ref) + ")"
	case "array":
		kind, fn := csItem(t.Elem)
		items := "Items(w, " + key + ", JsonValueKind." + kind + ", " + fn + ")"
		if req {
			return items + " ?? Array.Empty<" + csType(t.Elem) + ">()"
		}
		return items
	}
	panic("dotnet: no decoder for " + t.Kind)
}

// csEncValue is the JsonNode expression for the value e of type t.
func csEncValue(t *TypeRef, e string) string {
	switch t.Kind {
	case "string", "int", "int64", "bool":
		return e
	case "time":
		return "Iso(" + e + ")"
	case "enum":
		return e + ".ToString()"
	case "any":
		return "ToNode(" + e + ")"
	case "object":
		return "Write" + csName(t.Ref) + "(" + e + ")"
	case "array":
		var fn string
		switch t.Elem.Kind {
		case "string", "int", "int64":
			fn = "static e => JsonValue.Create(e)"
		case "enum":
			fn = "static e => JsonValue.Create(e.ToString())"
		case "object":
			fn = "Write" + csName(t.Elem.Ref)
		case "any":
			fn = "static e => ToNode(e)"
		default:
			panic("dotnet: no array writer for " + t.Elem.Kind)
		}
		return "Arr(" + e + ", " + fn + ")"
	}
	panic("dotnet: no encoder for " + t.Kind)
}

// csEnc is the statement that writes field f of the C# value v into the JSON object o. A field the caller left
// out is not sent.
func csEnc(f *Field, i int) string {
	key := fmt.Sprintf("%q", f.Wire)
	prop := "v." + csProp(f)
	if f.T.Kind == "string" {
		return "Put(o, " + key + ", " + prop + ");"
	}
	if csRequired(f) {
		return "o[" + key + "] = " + csEncValue(f.T, prop) + ";"
	}
	x := fmt.Sprintf("x%d", i) // a pattern variable lives to the end of the method, so each one has its own name
	return "if (" + prop + " is { } " + x + ") o[" + key + "] = " + csEncValue(f.T, x) + ";"
}
