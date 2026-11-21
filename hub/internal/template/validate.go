// Package template validates sender-defined UI templates. A template is a tree
// of component nodes {"type": ..., ...}; there is no logic in v1 beyond
// "foreach", which repeats one body node per element of a data array.
package template

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"
)

const (
	maxDepth = 8
	maxNodes = 64

	maxTextChars      = 512
	maxPlaceholders   = 16
	maxImageDimension = 512
	rootSchemaDefault = 1

	needsSchema2 = `needs "schema": 2`
)

// FieldError locates a validation failure, e.g. Path "children[2].type".
type FieldError struct {
	Path string
	Msg  string
}

func (e *FieldError) Error() string {
	if e.Path == "" {
		return e.Msg
	}
	return e.Path + ": " + e.Msg
}

var (
	containers  = map[string]bool{"vstack": true, "hstack": true}
	components  = map[string]bool{"vstack": true, "hstack": true, "spacer": true, "text": true, "symbol": true, "image": true, "progress": true, "timer": true, "gauge": true, "badge": true, "foreach": true, "divider": true}
	textStyles  = map[string]bool{"largeTitle": true, "title": true, "headline": true, "body": true, "caption": true, "footnote": true}
	timerModes  = map[string]bool{"countdown": true, "countup": true}
	hexColor    = regexp.MustCompile(`^#[0-9A-Fa-f]{6}$`)
	rootOnly    = []string{"tint", "background"}
	anywhereClr = []string{"color"}

	// bindPath is the grammar of a data path, in {"bind":...} and {{...}}.
	bindPath = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)

	// ifPath is the grammar of an "if" condition: a data path, optionally negated.
	ifPath = regexp.MustCompile(`^!?[A-Za-z0-9_.-]+$`)

	weights = map[string]bool{"regular": true, "medium": true, "semibold": true, "bold": true}

	aligns = map[string]bool{"leading": true, "center": true, "trailing": true}
	fits   = map[string]bool{"fill": true, "fit": true}
	axes   = map[string]bool{"vertical": true, "horizontal": true}

	// props lists the properties each component accepts besides "type", the
	// root-only "tint"/"background"/"schema" and "children" (which is checked
	// structurally). Anything else is rejected at save time.
	props = map[string]map[string]propKind{
		"vstack":   {"spacing": numRange(0, 64), "align": enum(aligns, "leading, center, trailing"), "padding": numRange(0, 32).v2(), "radius": numRange(0, 32).v2()},
		"hstack":   {"spacing": numRange(0, 64), "align": enum(aligns, "leading, center, trailing"), "padding": numRange(0, 32).v2(), "radius": numRange(0, 32).v2()},
		"divider":  {"color": colorProp()},
		"spacer":   {"min": numRange(0, 256)},
		"text":     {"text": textProp(), "style": enum(textStyles, "largeTitle, title, headline, body, caption, footnote"), "color": colorProp(), "lines": intRange(1, 10), "align": enum(aligns, "leading, center, trailing"), "weight": enum(weights, "regular, medium, semibold, bold").v2()},
		"symbol":   {"name": str(), "color": colorProp(), "size": numRange(8, 96)},
		"image":    {"url": urlProp(), "width": numRange(0, maxImageDimension), "height": numRange(0, maxImageDimension), "fit": enum(fits, "fill, fit"), "radius": numRange(0, 64)},
		"progress": {"value": numRange(0, 1), "color": colorProp(), "label": str()},
		"gauge":    {"value": numAny(), "min": numAny(), "max": numAny(), "label": str(), "color": colorProp()},
		"timer":    {"to": numRange(0, math.MaxInt64), "mode": enum(timerModes, "countdown, countup"), "style": enum(textStyles, "largeTitle, title, headline, body, caption, footnote"), "color": colorProp()},
		"badge":    {"text": str(), "color": colorProp(), "symbol": str()},
		"foreach":  {"each": pathProp(), "max": intRange(1, 12), "axis": enum(axes, "vertical, horizontal"), "spacing": numRange(0, 64)},
	}
)

type kind int

const (
	kString kind = iota
	kURL
	kText
	kNumber
	kInt
	kEnum
	kColor
	kPath
)

type propKind struct {
	kind     kind
	min, max float64
	ranged   bool
	enum     map[string]bool
	enumDesc string
	needsV2  bool // only allowed when the template root says "schema": 2
}

func (p propKind) v2() propKind { p.needsV2 = true; return p }

func str() propKind       { return propKind{kind: kString} }
func textProp() propKind  { return propKind{kind: kText} }
func urlProp() propKind   { return propKind{kind: kURL} }
func colorProp() propKind { return propKind{kind: kColor} }
func pathProp() propKind  { return propKind{kind: kPath} }
func numAny() propKind    { return propKind{kind: kNumber} }
func numRange(lo, hi float64) propKind {
	return propKind{kind: kNumber, min: lo, max: hi, ranged: true}
}
func intRange(lo, hi float64) propKind {
	return propKind{kind: kInt, min: lo, max: hi, ranged: true}
}
func enum(set map[string]bool, desc string) propKind {
	return propKind{kind: kEnum, enum: set, enumDesc: desc}
}

type validator struct {
	nodes  int
	schema int
}

// Validate checks a template body. Errors are *FieldError.
func Validate(body json.RawMessage) error {
	var root any
	if err := json.Unmarshal(body, &root); err != nil {
		return &FieldError{Msg: "body is not valid JSON"}
	}
	schema := rootSchemaDefault
	if obj, ok := root.(map[string]any); ok {
		if raw, present := obj["schema"]; present {
			n, ok := raw.(float64)
			if !ok || (n != 1 && n != 2) {
				return &FieldError{Path: "schema", Msg: "must be 1 or 2"}
			}
			schema = int(n)
		}
	}
	return (&validator{schema: schema}).node(root, "", 1, false)
}

func join(path, seg string) string {
	if path == "" {
		return seg
	}
	if seg[0] == '[' {
		return path + seg
	}
	return path + "." + seg
}

// node validates one node. inForeach is true inside a foreach body, where
// another foreach is rejected.
func (v *validator) node(raw any, path string, depth int, inForeach bool) error {
	obj, ok := raw.(map[string]any)
	if !ok {
		return &FieldError{Path: path, Msg: "node must be an object"}
	}
	typ, ok := obj["type"].(string)
	if !ok || !components[typ] {
		return &FieldError{Path: join(path, "type"), Msg: fmt.Sprintf("unknown component %v", obj["type"])}
	}
	if typ == "divider" && v.schema < 2 {
		return &FieldError{Path: join(path, "type"), Msg: needsSchema2}
	}
	if depth > maxDepth {
		return &FieldError{Path: path, Msg: fmt.Sprintf("nesting deeper than %d levels", maxDepth)}
	}
	if v.nodes++; v.nodes > maxNodes {
		return &FieldError{Path: path, Msg: fmt.Sprintf("more than %d nodes", maxNodes)}
	}

	for _, k := range anywhereClr {
		if err := checkColor(obj, k, path); err != nil {
			return err
		}
	}
	if _, present := obj["schema"]; present && depth > 1 {
		return &FieldError{Path: join(path, "schema"), Msg: "only allowed on the template root"}
	}
	for _, k := range rootOnly {
		// Schema 2 lets nested vstack/hstack carry a background.
		nestedOK := k == "background" && v.schema >= 2 && containers[typ]
		if _, present := obj[k]; present && depth > 1 && !nestedOK {
			return &FieldError{Path: join(path, k), Msg: "only allowed on the template root"}
		}
		if err := checkColor(obj, k, path); err != nil {
			return err
		}
	}
	if typ == "foreach" && inForeach {
		return &FieldError{Path: path, Msg: "foreach cannot be nested inside another foreach"}
	}
	if err := v.checkProps(obj, typ, path); err != nil {
		return err
	}

	if _, present := obj["each"]; typ == "foreach" && !present {
		return &FieldError{Path: join(path, "each"), Msg: "foreach requires an each path"}
	}

	children, hasChildren := obj["children"]
	switch {
	case containers[typ]:
		list, ok := children.([]any)
		if !hasChildren || !ok {
			return &FieldError{Path: join(path, "children"), Msg: "containers require a children array"}
		}
		for i, c := range list {
			if err := v.node(c, join(path, fmt.Sprintf("children[%d]", i)), depth+1, inForeach); err != nil {
				return err
			}
		}
	case typ == "foreach":
		if hasChildren {
			return &FieldError{Path: join(path, "children"), Msg: "foreach cannot have children"}
		}
		body, ok := obj["body"].(map[string]any)
		if !ok {
			return &FieldError{Path: join(path, "body"), Msg: "foreach requires a body object"}
		}
		// The body is validated (and counted) once, however many copies bind to.
		return v.node(body, join(path, "body"), depth+1, true)
	case hasChildren:
		return &FieldError{Path: join(path, "children"), Msg: typ + " cannot have children"}
	}
	return nil
}

// checkColor validates obj[key], if present, as {"light":"#RRGGBB","dark":"#RRGGBB"}.
func checkColor(obj map[string]any, key, path string) error {
	raw, present := obj[key]
	if !present {
		return nil
	}
	c, ok := raw.(map[string]any)
	if !ok {
		return &FieldError{Path: join(path, key), Msg: `must be {"light":"#RRGGBB","dark":"#RRGGBB"}`}
	}
	for _, mode := range []string{"light", "dark"} {
		s, ok := c[mode].(string)
		if !ok || !hexColor.MatchString(s) {
			return &FieldError{Path: join(join(path, key), mode), Msg: "must be a #RRGGBB color"}
		}
	}
	return nil
}

// checkProps rejects unknown properties (sorted, so the reported path is
// deterministic) and validates each known property's value.
func (v *validator) checkProps(obj map[string]any, typ, path string) error {
	known := props[typ]
	keys := make([]string, 0, len(obj))
	for k := range obj {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		switch k {
		case "type", "children", "tint", "background", "schema":
			continue
		case "body":
			if typ == "foreach" {
				continue // checked structurally
			}
		case "if":
			if v.schema < 2 {
				return &FieldError{Path: join(path, k), Msg: needsSchema2}
			}
			if s, ok := obj[k].(string); !ok || !ifPath.MatchString(s) {
				return &FieldError{Path: join(path, k), Msg: "must be a path of letters, digits, _ . -, optionally starting with !"}
			}
			continue
		}
		pk, ok := known[k]
		if !ok {
			return &FieldError{Path: join(path, k), Msg: "unknown property"}
		}
		if pk.needsV2 && v.schema < 2 {
			return &FieldError{Path: join(path, k), Msg: needsSchema2}
		}
		if err := checkValue(obj[k], pk, join(path, k)); err != nil {
			return err
		}
	}
	return nil
}

func checkValue(v any, pk propKind, path string) error {
	switch pk.kind {
	case kColor:
		return nil // validated by checkColor
	case kPath:
		s, ok := v.(string)
		if !ok || !bindPath.MatchString(s) {
			return &FieldError{Path: path, Msg: "must be a non-empty path of letters, digits, _ . -"}
		}
	case kString, kText, kURL:
		s, ok := v.(string)
		if !ok {
			return &FieldError{Path: path, Msg: "must be a string"}
		}
		if err := checkPlaceholders(s, path); err != nil {
			return err
		}
		if pk.kind == kText && len([]rune(s)) > maxTextChars {
			return &FieldError{Path: path, Msg: fmt.Sprintf("longer than %d characters", maxTextChars)}
		}
		if pk.kind == kURL && !strings.HasPrefix(s, "{{") && !strings.HasPrefix(s, "https://") {
			return &FieldError{Path: path, Msg: "url must use https"}
		}
	case kEnum:
		s, ok := v.(string)
		if !ok || !pk.enum[s] {
			return &FieldError{Path: path, Msg: "must be one of " + pk.enumDesc}
		}
	case kNumber, kInt:
		if m, isObj := v.(map[string]any); isObj {
			return checkBind(m, path)
		}
		n, ok := v.(float64)
		if !ok {
			return &FieldError{Path: path, Msg: `must be a number or {"bind":"path"}`}
		}
		if pk.kind == kInt && n != math.Trunc(n) {
			return &FieldError{Path: path, Msg: "must be an integer"}
		}
		if pk.ranged && (n < pk.min || n > pk.max) {
			return &FieldError{Path: path, Msg: fmt.Sprintf("must be between %v and %v", pk.min, pk.max)}
		}
	}
	return nil
}

// checkBind validates {"bind":"path"}.
func checkBind(m map[string]any, path string) error {
	p, ok := m["bind"].(string)
	if len(m) != 1 || !ok {
		return &FieldError{Path: path, Msg: `must be a number or {"bind":"path"}`}
	}
	if !bindPath.MatchString(p) {
		return &FieldError{Path: path + ".bind", Msg: "must be a non-empty path of letters, digits, _ . -"}
	}
	return nil
}

// checkPlaceholders requires every "{{" in s to open a well-formed {{path}}
// (no nesting, no spaces) and allows at most maxPlaceholders per string.
func checkPlaceholders(s, path string) error {
	count := 0
	for {
		i := strings.Index(s, "{{")
		if i < 0 {
			return nil
		}
		rest := s[i+2:]
		j := strings.Index(rest, "}}")
		if j < 0 || !bindPath.MatchString(rest[:j]) {
			return &FieldError{Path: path, Msg: "malformed placeholder, want {{path}}"}
		}
		if count++; count > maxPlaceholders {
			return &FieldError{Path: path, Msg: fmt.Sprintf("more than %d placeholders", maxPlaceholders)}
		}
		s = rest[j+2:]
	}
}
