package main

import (
	"fmt"
	"regexp"
	"strings"
	"text/template"
	"unicode"
)

func init() {
	langs = append(langs, lang{
		name: "java", out: "java/src/main/java/app/hansho/sdk/Models.java", tmpl: "java.tmpl",
		funcs: template.FuncMap{
			"javaType": javaType, "javaName": javaName, "javaConst": javaConst, "javaDoc": javaDoc,
			"javaRecordDoc": javaRecordDoc, "javaDecode": javaDecode, "javaEncode": javaEncode, "javaIsList": javaIsList,
			"javaRequired": javaRequired, "javaSettable": javaSettable, "javaParams": javaParams, "javaCheck": javaCheck,
			"javaIsRequest": javaIsRequest, "javaIsResponse": javaIsResponse,
			"javaValues": javaValues, "javaValueCheck": javaValueCheck, "javaNeedsCtor": javaNeedsCtor,
		},
		manifest: []manifestVersion{
			// Only the project's own <version>, which sits at the two-space indent; a dependency's is deeper.
			{"java/pom.xml", regexp.MustCompile(`(?m)^  <version>([^<]+)</version>`)},
			// The example builds against the SDK it sits next to.
			{"examples/java/pom.xml", regexp.MustCompile(`(?s)<artifactId>hansho</artifactId>\s*<version>([^<]+)</version>`)},
			{"java/README.md", regexp.MustCompile(`(?s)<artifactId>hansho</artifactId>\s*<version>([^<]+)</version>`)},
			{"java/README.md", regexp.MustCompile(`implementation\("app\.hansho:hansho:([^"]+)"\)`)},
		},
	})
}

var javaKeywords = map[string]bool{"abstract": true, "assert": true, "boolean": true, "break": true, "byte": true,
	"case": true, "catch": true, "char": true, "class": true, "const": true, "continue": true, "default": true,
	"do": true, "double": true, "else": true, "enum": true, "extends": true, "final": true, "finally": true,
	"float": true, "for": true, "goto": true, "if": true, "implements": true, "import": true, "instanceof": true,
	"int": true, "interface": true, "long": true, "native": true, "new": true, "package": true, "private": true,
	"protected": true, "public": true, "return": true, "short": true, "static": true, "strictfp": true,
	"super": true, "switch": true, "synchronized": true, "this": true, "throw": true, "throws": true,
	"transient": true, "try": true, "void": true, "volatile": true, "while": true, "null": true, "true": true,
	"false": true, "record": true, "var": true, "yield": true, "o": true, "w": true, "arr": true, "item": true}

// javaName is the record component and setter name: camelCase of the spec name.
func javaName(f *Field) string {
	n := camel(f.Name)
	if javaKeywords[n] {
		panic("java: field " + n + " is a keyword or a name the generated code uses")
	}
	return n
}

// javaRef is the Java type of t; boxed for a value the caller may leave out.
func javaRef(t *TypeRef, boxed bool) string {
	switch t.Kind {
	case "string":
		return "String"
	case "int":
		if boxed {
			return "Integer"
		}
		return "int"
	case "int64":
		if boxed {
			return "Long"
		}
		return "long"
	case "bool":
		if boxed {
			return "Boolean"
		}
		return "boolean"
	case "time":
		return "Instant"
	case "any":
		return "Object"
	case "array":
		return "List<" + javaRef(t.Elem, true) + ">"
	case "enum", "object":
		return t.Ref
	}
	panic("java: no type for " + t.Kind)
}

// javaType: a field the spec lists as required is a primitive where there is one; the rest can be null.
func javaType(f *Field) string { return javaRef(f.T, !f.Required) }

func javaIsList(f *Field) bool { return f.T.Kind == "array" }

// javaConst names an enum constant: time-sensitive -> TIME_SENSITIVE.
func javaConst(v string) string {
	c := strings.ToUpper(snake(v))
	if c == "" || unicode.IsDigit(rune(c[0])) {
		panic("java: no constant name for enum value " + v)
	}
	return c
}

// javaEscape makes spec text safe inside a Javadoc comment: backtick spans become <code>, and no raw <, >, &, { or }
// is left for the doclet to read as markup or a tag.
func javaEscape(s string) string {
	esc := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", "{", "&#123;", "}", "&#125;", "@", "&#64;", "*/", "*&#47;")
	parts := strings.Split(s, "`")
	var b strings.Builder
	for i, p := range parts {
		switch {
		case i%2 == 1 && i < len(parts)-1:
			b.WriteString("<code>" + esc.Replace(p) + "</code>")
		case i%2 == 1: // an unpaired backtick: keep it
			b.WriteString("`" + esc.Replace(p))
		default:
			b.WriteString(esc.Replace(p))
		}
	}
	return b.String()
}

// javaDoc is a Javadoc comment at indent, or nothing for an empty description.
func javaDoc(indent, desc string) string {
	if desc == "" {
		return ""
	}
	text := javaEscape(desc)
	if len(indent)+len(text) < 100 {
		return indent + "/** " + text + " */\n"
	}
	return indent + "/**\n" + wrap(indent+" * ", text) + "\n" + indent + " */\n"
}

// javaRecordDoc documents a record: its description, then one @param per described component.
func javaRecordDoc(t *Type) string {
	var b strings.Builder
	b.WriteString("    /**\n")
	if t.Desc != "" {
		b.WriteString(wrap("     * ", javaEscape(t.Desc)) + "\n")
	} else {
		b.WriteString("     * " + t.Name + ".\n")
	}
	first := true
	for _, f := range t.Fields {
		if f.Desc == "" {
			continue
		}
		if first {
			b.WriteString("     *\n")
			first = false
		}
		b.WriteString(wrap("     * ", "@param "+javaName(f)+" "+javaEscape(f.Desc)) + "\n")
	}
	b.WriteString("     */\n")
	return b.String()
}

// javaRequired: what Type.builder takes. A read-only field is the hub's to set.
func javaRequired(t *Type) []*Field {
	var out []*Field
	for _, f := range t.Fields {
		if f.Required && !f.ReadOnly {
			out = append(out, f)
		}
	}
	return out
}

// javaSettable: the fields a builder has a setter for.
func javaSettable(t *Type) []*Field {
	var out []*Field
	for _, f := range t.Fields {
		if !f.ReadOnly {
			out = append(out, f)
		}
	}
	return out
}

func javaParams(fs []*Field) string {
	var p []string
	for _, f := range fs {
		p = append(p, javaType(f)+" "+javaName(f))
	}
	return strings.Join(p, ", ")
}

// javaCheck: a required reference is checked for null by Type.builder (a value of "any" may be null).
func javaCheck(f *Field) bool {
	switch f.T.Kind {
	case "int", "int64", "bool", "any":
		return false
	}
	return true
}

// javaNeedsCtor: the record has a compact constructor, to copy its lists and check an integer enum.
func javaNeedsCtor(t *Type) bool {
	for _, f := range t.Fields {
		if javaIsList(f) || len(f.T.Values) > 0 {
			return true
		}
	}
	return false
}

// javaValues is the allowed values of an inline integer enum, as Java int literals.
func javaValues(f *Field) []string { return f.T.Values }

// javaValueCheck is the condition that holds when an inline integer enum field has a value it does not allow.
func javaValueCheck(f *Field) string {
	n := javaName(f)
	var c []string
	for _, v := range f.T.Values {
		c = append(c, n+" != "+v)
	}
	return n + " != null && " + strings.Join(c, " && ")
}

// javaDecode is the expression that reads field f from the JsonNode w.
func javaDecode(f *Field) string {
	n := fmt.Sprintf("w.get(%q)", f.Wire)
	t := f.T
	switch t.Kind {
	case "string":
		if f.Required {
			return "Json.str(" + n + ")"
		}
		return "Json.optStr(" + n + ")"
	case "int":
		if len(t.Values) > 0 {
			return "Json.optInt(" + n + ", " + strings.Join(t.Values, ", ") + ")"
		}
		if f.Required {
			return "(int) Json.num(" + n + ")"
		}
		return "Json.optInt(" + n + ")"
	case "int64":
		if f.Required {
			return "Json.num(" + n + ")"
		}
		return "Json.optLong(" + n + ")"
	case "bool":
		if f.Required {
			return "Json.bool(" + n + ")"
		}
		return "Json.optBool(" + n + ")"
	case "time":
		return "Json.instant(" + n + ")"
	case "any":
		return "Json.any(" + n + ")"
	case "enum":
		return t.Ref + ".fromWire(Json.optStr(" + n + "))"
	case "object":
		return "Json.object(" + n + ", " + t.Ref + "::fromWire)"
	case "array":
		switch t.Elem.Kind {
		case "string":
			return "Json.strings(" + n + ")"
		case "object":
			return "Json.list(" + n + ", " + t.Elem.Ref + "::fromWire)"
		}
	}
	panic("java: no decoder for " + f.Wire + " of kind " + t.Kind)
}

// javaItem is the JSON value of one array item.
func javaItem(t *TypeRef) string {
	switch t.Kind {
	case "string":
		return "item"
	case "object":
		return "item.toWire()"
	case "enum":
		return "item.wire()"
	}
	panic("java: no encoder for array items of kind " + t.Kind)
}

// javaEncode is the statements that write field f to the ObjectNode o, each line starting with indent.
func javaEncode(indent string, f *Field) string {
	n := javaName(f)
	t := f.T
	var ls []string
	switch t.Kind {
	case "string":
		ls = []string{fmt.Sprintf("Json.put(o, %q, %s);", f.Wire, n)}
	case "int", "int64", "bool":
		if f.Required {
			ls = []string{fmt.Sprintf("o.put(%q, %s);", f.Wire, n)}
		} else {
			ls = []string{fmt.Sprintf("if (%s != null) {", n), fmt.Sprintf("    o.put(%q, %s);", f.Wire, n), "}"}
		}
	case "time":
		ls = []string{fmt.Sprintf("if (%s != null) {", n), fmt.Sprintf("    o.put(%q, %s.toString());", f.Wire, n), "}"}
	case "any":
		ls = []string{fmt.Sprintf("if (%s != null) {", n), fmt.Sprintf("    o.set(%q, Json.tree(%s));", f.Wire, n), "}"}
	case "enum":
		if f.Required {
			ls = []string{fmt.Sprintf("o.put(%q, Objects.requireNonNull(%s, %q).wire());", f.Wire, n, "hansho: "+f.Wire+" is required")}
		} else {
			ls = []string{fmt.Sprintf("if (%s != null) {", n), fmt.Sprintf("    o.put(%q, %s.wire());", f.Wire, n), "}"}
		}
	case "object":
		ls = []string{fmt.Sprintf("if (%s != null) {", n), fmt.Sprintf("    o.set(%q, %s.toWire());", f.Wire, n), "}"}
	case "array":
		// Like the other SDKs, an optional list that is empty is left out; a required one is always sent.
		guard := fmt.Sprintf("if (%s != null && !%s.isEmpty()) {", n, n)
		if f.Required {
			guard = fmt.Sprintf("if (%s != null) {", n)
		}
		ls = []string{
			guard,
			fmt.Sprintf("    ArrayNode arr = o.putArray(%q);", f.Wire),
			fmt.Sprintf("    for (%s item : %s) {", javaRef(t.Elem, true), n),
			"        arr.add(" + javaItem(t.Elem) + ");",
			"    }",
			"}",
		}
	default:
		panic("java: no encoder for " + f.Wire + " of kind " + t.Kind)
	}
	for i := range ls {
		ls[i] = indent + ls[i]
	}
	return strings.Join(ls, "\n")
}

// javaSets caches which types are sent and which are read, so the template can ask per type.
var javaSets = map[*Spec]struct{ req, resp map[string]bool }{}

func javaSetsOf(s *Spec) struct{ req, resp map[string]bool } {
	if v, ok := javaSets[s]; ok {
		return v
	}
	types := map[string]*Type{}
	for _, t := range s.Types {
		types[t.Name] = t
	}
	var walk func(set map[string]bool, name string)
	walk = func(set map[string]bool, name string) {
		t := types[name]
		if t == nil || set[name] {
			return
		}
		set[name] = true
		for _, f := range t.Fields {
			switch {
			case f.T.Kind == "object":
				walk(set, f.T.Ref)
			case f.T.Kind == "array" && f.T.Elem.Kind == "object":
				walk(set, f.T.Elem.Ref)
			}
		}
	}
	v := struct{ req, resp map[string]bool }{map[string]bool{}, map[string]bool{}}
	for _, o := range s.Ops {
		if o.BodyRef != "" {
			walk(v.req, o.BodyRef)
		}
		if r := o.Result; r != nil {
			switch {
			case r.Kind == "object":
				walk(v.resp, r.Ref)
			case r.Kind == "array" && r.Elem.Kind == "object":
				walk(v.resp, r.Elem.Ref)
			}
		}
	}
	for _, e := range s.Events {
		walk(v.resp, e.Type)
	}
	javaSets[s] = v
	return v
}

// javaIsRequest: the type is sent, so it gets a builder and toWire.
func javaIsRequest(s *Spec, name string) bool { return javaSetsOf(s).req[name] }

// javaIsResponse: the type is read, so it gets fromWire.
func javaIsResponse(s *Spec, name string) bool { return javaSetsOf(s).resp[name] }
