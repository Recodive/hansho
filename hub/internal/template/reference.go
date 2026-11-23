package template

import (
	"sort"
	"strconv"
	"strings"
)

// referenceOrder is how Reference lists components. A component missing here is still listed, after these, by name.
var referenceOrder = []string{"vstack", "hstack", "divider", "spacer", "text", "symbol", "image", "progress", "gauge", "timer", "badge", "foreach"}

// Reference describes each component and the properties the validator accepts for it, one row per component (name,
// properties), built from the validator's own tables so it cannot drift from them. Components with the same
// properties share a row ("vstack, hstack"). Properties are in name order. Every row also accepts "if", and the
// template root accepts "tint", "background" and "schema".
func Reference() [][2]string {
	names := append([]string{}, referenceOrder...)
	listed := map[string]bool{}
	for _, n := range names {
		listed[n] = true
	}
	var rest []string
	for n := range components {
		if !listed[n] {
			rest = append(rest, n)
		}
	}
	sort.Strings(rest)
	var rows [][2]string
	for _, name := range append(names, rest...) {
		if !components[name] {
			continue
		}
		text := referenceProps(name)
		if n := len(rows); n > 0 && rows[n-1][1] == text {
			rows[n-1][0] += ", " + name
			continue
		}
		rows = append(rows, [2]string{name, text})
	}
	return rows
}

func referenceProps(name string) string {
	hints := map[string]string{"symbol.name": "name (SF Symbol)", "timer.to": "to (unix seconds)", "divider.color": "color (a 1 pt hairline across the stack it sits in)"}
	var keys []string
	for k := range props[name] {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	if containers[name] {
		parts = append(parts, "children (required)")
	}
	for _, k := range keys {
		if h, ok := hints[name+"."+k]; ok {
			parts = append(parts, h)
		} else {
			parts = append(parts, referenceProp(k, props[name][k]))
		}
	}
	switch {
	case containers[name]:
		parts = append(parts, "background (nested stacks; the root's is the card background)")
	case name == "foreach":
		parts = append(parts, "body (one node, required)")
	}
	return strings.Join(parts, ", ")
}

func referenceProp(name string, p propKind) string {
	switch p.kind {
	case kEnum:
		return name + " " + strings.ReplaceAll(p.enumDesc, ", ", "/")
	case kText:
		return name + " (at most " + strconv.Itoa(maxTextChars) + " characters)"
	case kURL:
		return name + " (https)"
	case kPath:
		return name + " (data path to an array, required)"
	}
	if p.ranged && p.max < 1e9 {
		return name + " " + strconv.FormatFloat(p.min, 'f', -1, 64) + "-" + strconv.FormatFloat(p.max, 'f', -1, 64)
	}
	return name
}
