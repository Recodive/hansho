package main

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"text/template"
	"unicode"
)

func init() {
	langs = append(langs, lang{
		name: "python", out: "python/hansho/_types_gen.py", tmpl: "python.tmpl",
		funcs: template.FuncMap{
			"pyType": pyType, "pyFields": pyFields, "pyName": pyName, "pyLine": pyLine, "pyFrom": pyFrom, "pyTo": pyTo,
			"pyDoc": pyDoc, "pyComment": pyComment, "pySnake": pySnake, "pyConst": pyConst, "pyEncodes": pyEncodes,
			"pyDecodes": pyDecodes,
		},
		// The version stays in _version.py, where the release workflow and hatch read it.
		manifest: []manifestVersion{{"python/hansho/_version.py", regexp.MustCompile(`(?m)^__version__ = "([^"]+)"`)}},
	})
}

var pyKeywords = map[string]bool{"from": true, "class": true, "import": true, "global": true, "pass": true, "lambda": true,
	"in": true, "is": true, "not": true, "and": true, "or": true, "def": true, "del": true, "if": true, "else": true,
	"for": true, "while": true, "with": true, "as": true, "try": true, "raise": true, "return": true, "yield": true,
	"None": true, "True": true, "False": true, "assert": true, "break": true, "continue": true, "except": true,
	"finally": true, "elif": true, "nonlocal": true, "async": true, "await": true}

// pySnake turns CamelCase into snake_case: PageAck -> page_ack.
func pySnake(s string) string {
	var b strings.Builder
	for i, r := range s {
		if unicode.IsUpper(r) {
			if i > 0 && !unicode.IsUpper(rune(s[i-1])) && s[i-1] != '_' {
				b.WriteByte('_')
			}
			b.WriteRune(unicode.ToLower(r))
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// pyConst names the tuple of an enum's values: Level -> LEVEL_VALUES.
func pyConst(enum string) string { return strings.ToUpper(pySnake(enum)) + "_VALUES" }

func pyName(f *Field) string {
	n := snake(f.Name)
	if pyKeywords[n] {
		panic("python: field " + n + " is a keyword")
	}
	return n
}

func pyType(t *TypeRef) string {
	switch t.Kind {
	case "string":
		return "str"
	case "int", "int64":
		if len(t.Values) > 0 {
			return "Literal[" + strings.Join(t.Values, ", ") + "]"
		}
		return "int"
	case "bool":
		return "bool"
	case "time":
		return "datetime"
	case "any":
		return "Any"
	case "array":
		return "List[" + pyType(t.Elem) + "]"
	case "enum", "object":
		return t.Ref
	}
	panic("python: no type for " + t.Kind)
}

// pyLead keeps a type's positional arguments as they have always been: Message(external_user_id, channel, title, body, ...).
// A required field that follows an optional one gets an empty default, as Python wants.
var pyLead = map[string][]string{"Message": {"external_user_id", "channel", "title", "body", "level", "subtitle", "image_url", "template_id", "data",
	"sound", "actions", "collapse_id", "page", "idempotency_key"}}

// pyFields orders a dataclass's fields: the lead ones, then the required ones (Python wants fields without a default
// first), then the optional ones, then the ones only the hub sets, each group in spec order.
func pyFields(t *Type) []*Field {
	var lead, req, opt, ro []*Field
	for _, n := range pyLead[t.Name] {
		for _, f := range t.Fields {
			if f.Wire == n {
				lead = append(lead, f)
			}
		}
	}
	for _, f := range t.Fields {
		if slices.Contains(lead, f) {
			continue
		}
		switch {
		case f.Required:
			req = append(req, f)
		case f.ReadOnly:
			ro = append(ro, f)
		default:
			opt = append(opt, f)
		}
	}
	return slices.Concat(lead, req, opt, ro)
}

// pyLine is a field's declaration: an optional list defaults to an empty one, any other optional field to None.
func pyLine(s *Spec, t *Type, f *Field) string {
	n, ty := pyName(f), pyType(f.T)
	switch {
	case f.Required && pyAfterOptional(t, f):
		if f.T.Kind != "string" {
			panic("python: no default for required " + n)
		}
		return n + ": " + ty + ` = ""`
	case f.Required:
		return n + ": " + ty
	case f.T.Kind == "array":
		return n + ": " + ty + " = field(default_factory=list)"
	case f.T.Kind == "any":
		return n + ": Any = None"
	case pyZeroInt(s, t, f):
		return n + ": int = 0"
	}
	return n + ": Optional[" + ty + "] = None"
}

// pyRequests names the types the SDK sends: the request bodies and everything they contain. Every other type only
// comes back in answers and webhooks.
func pyRequests(s *Spec) map[string]bool {
	byName := map[string]*Type{}
	for _, t := range s.Types {
		byName[t.Name] = t
	}
	out := map[string]bool{}
	var add func(name string)
	add = func(name string) {
		t := byName[name]
		if t == nil || out[name] {
			return
		}
		out[name] = true
		for _, f := range t.Fields {
			r := f.T
			if r.Kind == "array" {
				r = r.Elem
			}
			if r.Kind == "object" {
				add(r.Ref)
			}
		}
	}
	for _, o := range s.Ops {
		if o.BodyRef != "" {
			add(o.BodyRef)
		}
	}
	return out
}

// pyLoose is the reading of an optional field of a type that only comes back in answers: an integer the hub leaves
// out is 0 and an empty string is None, as the SDK has always read them. A field that is also sent keeps what it is
// given, so that 0 and "" can be sent. Integers restricted to a few values (a priority) have no 0 and stay None.
func pyLoose(s *Spec, t *Type, f *Field) bool {
	return !f.Required && !pyRequests(s)[t.Name]
}

func pyZeroInt(s *Spec, t *Type, f *Field) bool {
	return pyLoose(s, t, f) && (f.T.Kind == "int" || f.T.Kind == "int64") && len(f.T.Values) == 0
}

// pyAfterOptional: f is a required field placed after one with a default.
func pyAfterOptional(t *Type, f *Field) bool {
	seen := false
	for _, o := range pyFields(t) {
		if o == f {
			return seen
		}
		if !o.Required {
			seen = true
		}
	}
	return false
}

// pyScalar names the helper in _wire.py that reads one value of kind t from an answer.
func pyScalar(t *TypeRef, required bool) string {
	opt := ""
	if !required {
		opt = "opt_"
	}
	switch t.Kind {
	case "string":
		return "_" + opt + "s"
	case "int", "int64":
		if len(t.Values) > 0 {
			return "_" + opt + "ie"
		}
		return "_" + opt + "i"
	case "bool":
		return "_" + opt + "b"
	case "time":
		return "_" + opt + "time"
	case "enum":
		return "_" + opt + "enum"
	}
	panic("python: no reader for " + t.Kind)
}

func pyReader(t *TypeRef) string {
	switch t.Kind {
	case "object":
		return "from_wire_" + pySnake(t.Ref)
	case "any":
		return "_any"
	}
	return pyScalar(t, true)
}

// pyFrom reads the wire value of f from the dict w.
func pyFrom(s *Spec, t *Type, f *Field) string {
	e := fmt.Sprintf("w.get(%q)", f.Wire)
	switch f.T.Kind {
	case "any":
		return e
	case "object":
		if f.Required {
			return pyReader(f.T) + "(" + e + ")"
		}
		return "_obj(" + e + ", " + pyReader(f.T) + ")"
	case "array":
		if f.T.Elem.Kind == "array" {
			panic("python: nested arrays")
		}
		return "_list(" + e + ", " + pyReader(f.T.Elem) + ")"
	}
	if pyLoose(s, t, f) && f.T.Kind == "string" {
		return "_opt_s(" + e + ") or None"
	}
	return pyScalar(f.T, f.Required || pyZeroInt(s, t, f)) + "(" + e + ")"
}

// pyTo turns the attribute of f on v into its wire value; None is dropped later.
func pyTo(s *Spec, t *Type, f *Field) string {
	e := "v." + pyName(f)
	switch f.T.Kind {
	case "time":
		return "_iso(" + e + ")"
	case "object":
		return "_enc(" + e + ", to_wire_" + pySnake(f.T.Ref) + ")"
	case "array":
		fn := "None"
		switch f.T.Elem.Kind {
		case "object":
			fn = "to_wire_" + pySnake(f.T.Elem.Ref)
		case "time":
			fn = "_iso"
		}
		if f.Required {
			return "_arr(" + e + ", " + fn + ", True)"
		}
		return "_arr(" + e + ", " + fn + ")"
	}
	if pyZeroInt(s, t, f) {
		return e + " or None"
	}
	return e
}

// pyDecodes lists the fields an answer carries; a header never comes back in the body.
func pyDecodes(t *Type) []*Field {
	var out []*Field
	for _, f := range t.Fields {
		if f.Header == "" {
			out = append(out, f)
		}
	}
	return out
}

// pyEncodes lists the fields that go in a request body.
func pyEncodes(t *Type) []*Field {
	var out []*Field
	for _, f := range t.Fields {
		if f.Header == "" && !f.ReadOnly {
			out = append(out, f)
		}
	}
	return out
}

func pyEscape(s string) string {
	return strings.NewReplacer("\\", "\\\\", `"""`, `\"\"\"`).Replace(s)
}

// pyDoc is a docstring at the indent.
func pyDoc(indent, desc string) string {
	if desc == "" {
		return ""
	}
	desc = pyEscape(desc)
	if len(indent)+len(desc)+6 < 110 && !strings.HasSuffix(desc, "\"") {
		return indent + `"""` + desc + `"""` + "\n"
	}
	return indent + `"""` + strings.TrimPrefix(wrap(indent, desc), indent) + "\n" + indent + `"""` + "\n"
}

// pyComment is a #: comment (Sphinx reads it as the doc of the line below).
func pyComment(indent, desc string) string {
	if desc == "" {
		return ""
	}
	return wrap(indent+"#: ", desc) + "\n"
}
