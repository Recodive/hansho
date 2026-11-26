package main

import (
	"fmt"
	"regexp"
	"strings"
	"text/template"
)

func init() {
	langs = append(langs, lang{
		name: "php", out: "php/generated/types.php", tmpl: "php.tmpl",
		funcs: template.FuncMap{"phpDoc": phpDoc, "phpParams": phpParams, "q": phpQuote},
		// The client's User-Agent constant stays where it is, so the version is rewritten there too.
		manifest: []manifestVersion{{"php/src/Hansho.php", regexp.MustCompile(`(?m)^    public const VERSION = '([^']+)'`)}},
	})
}

// phpQuote is a single-quoted PHP string.
func phpQuote(s string) string {
	return "'" + strings.NewReplacer(`\\`, `\\\\`, "'", `\'`).Replace(s) + "'"
}

// phpParam is one constructor parameter of a generated class, with how it reads from and writes to the wire.
type phpParam struct {
	Doc    string // docblock, indented, with a trailing newline
	Decl   string // public readonly ?string $x = null
	Prop   string // x
	Wire   string
	Decode string // PHP expression over $w
	Encode string // PHP expression over $this, "" when the field is not sent in the body
}

func phpParams(t *Type) []phpParam {
	var req, opt, ro []*Field
	for _, f := range t.Fields {
		switch {
		case f.ReadOnly:
			ro = append(ro, f)
		case f.Required:
			req = append(req, f)
		default:
			opt = append(opt, f)
		}
	}
	fields := append(append(req, opt...), ro...)
	// An optional parameter before a required one cannot have a default.
	lastRequired := -1
	for i, f := range fields {
		if f.Required && !f.ReadOnly {
			lastRequired = i
		}
	}
	var out []phpParam
	for i, f := range fields {
		prop := camel(f.Name)
		native, doc := phpNative(f.T)
		required := phpRequired(t, f)
		def := ""
		switch {
		case f.T.Kind == "any":
			if !required && i > lastRequired {
				def = " = null"
			}
		case !required:
			native = phpNullable(native)
			if doc != "" {
				doc += "|null"
			}
			if i > lastRequired {
				def = " = null"
			}
		}
		var tags []string
		if doc != "" {
			tags = append(tags, "@var "+doc)
		}
		p := phpParam{Prop: prop, Wire: f.Wire, Decl: "public readonly " + native + " $" + prop + def}
		p.Doc = phpDocBlock("        ", f.Desc, tags)
		if f.Header == "" {
			p.Decode = phpFrom(t, f, required)
		}
		if f.Header == "" && !f.ReadOnly {
			p.Encode = phpTo(t, f)
		}
		out = append(out, p)
	}
	return out
}

// phpRequired: the field is always there in a value the SDK holds (constructor, answer).
func phpRequired(t *Type, f *Field) bool {
	return f.Required && !f.ReadOnly
}

func phpNullable(native string) string {
	if native == "mixed" {
		return native
	}
	if strings.Contains(native, "|") {
		return native + "|null"
	}
	return "?" + native
}

// phpNative returns the native type and the docblock type ("" when the native one says it all).
func phpNative(t *TypeRef) (string, string) {
	switch t.Kind {
	case "string":
		return "string", ""
	case "int", "int64":
		if len(t.Values) > 0 {
			return "int", strings.Join(t.Values, "|")
		}
		return "int", ""
	case "bool":
		return "bool", ""
	case "time":
		return "\\DateTimeImmutable", ""
	case "any":
		return "mixed", ""
	case "enum":
		return t.Ref + "|string", ""
	case "object":
		return t.Ref, ""
	case "array":
		return "array", "list<" + phpElem(t.Elem) + ">"
	}
	panic("php: no type for " + t.Kind)
}

func phpElem(t *TypeRef) string {
	switch t.Kind {
	case "string":
		return "string"
	case "enum":
		return t.Ref + "|string"
	case "object":
		return t.Ref
	}
	panic("php: no list of " + t.Kind)
}

func phpKey(f *Field) string { return fmt.Sprintf("'%s'", f.Wire) }

func phpFrom(t *Type, f *Field, required bool) string {
	k := phpKey(f)
	tt := f.T
	switch tt.Kind {
	case "string", "enum":
		return fmt.Sprintf("Spec::%s($w, %s)", phpPick(required, "str", "optStr"), k)
	case "int", "int64":
		return fmt.Sprintf("Spec::%s($w, %s)", phpPick(required, "int", "optInt"), k)
	case "bool":
		return fmt.Sprintf("Spec::%s($w, %s)", phpPick(required, "bool", "optBool"), k)
	case "time":
		return fmt.Sprintf("Spec::%s($w, %s)", phpPick(required, "date", "optDate"), k)
	case "any":
		return fmt.Sprintf("$w[%s] ?? null", k)
	case "object":
		if required {
			return fmt.Sprintf("%s::fromWire($w[%s] ?? null)", tt.Ref, k)
		}
		return fmt.Sprintf("is_array($w[%s] ?? null) ? %s::fromWire($w[%s]) : null", k, tt.Ref, k)
	case "array":
		var inner string
		switch tt.Elem.Kind {
		case "string", "enum":
			inner = fmt.Sprintf("Spec::strings($w[%s] ?? null)", k)
		case "object":
			inner = fmt.Sprintf("Spec::objects($w[%s] ?? null, %s::fromWire(...))", k, tt.Elem.Ref)
		default:
			panic("php: no list of " + tt.Elem.Kind)
		}
		if required {
			return inner
		}
		return fmt.Sprintf("is_array($w[%s] ?? null) ? %s : null", k, inner)
	}
	panic("php: no decoder for " + tt.Kind)
}

func phpPick(required bool, a, b string) string {
	if required {
		return a
	}
	return b
}

func phpTo(t *Type, f *Field) string {
	p := "$this->" + camel(f.Name)
	required := phpRequired(t, f)
	switch f.T.Kind {
	case "string", "int", "int64", "bool", "any":
		return p
	case "enum":
		return "Spec::value(" + p + ")"
	case "time":
		return "Spec::iso(" + p + ")"
	case "object":
		if required {
			return p + "->toWire()"
		}
		return p + "?->toWire()"
	case "array":
		if f.T.Elem.Kind == "object" {
			return "Spec::wires(" + p + ")"
		}
		return "Spec::list(" + p + ")"
	}
	panic("php: no encoder for " + f.T.Kind)
}

// phpDocBlock renders a docblock; "" when there is nothing to say.
func phpDocBlock(indent, desc string, tags []string) string {
	if desc == "" && len(tags) == 0 {
		return ""
	}
	if len(tags) == 0 && len(indent)+len(desc)+7 < 116 {
		return indent + "/** " + desc + " */\n"
	}
	var b strings.Builder
	b.WriteString(indent + "/**\n")
	if desc != "" {
		b.WriteString(wrap(indent+" * ", desc) + "\n")
		if len(tags) > 0 {
			b.WriteString(indent + " *\n")
		}
	}
	for _, t := range tags {
		b.WriteString(indent + " * " + t + "\n")
	}
	b.WriteString(indent + " */\n")
	return b.String()
}

func phpDoc(indent, desc string) string { return phpDocBlock(indent, desc, nil) }
