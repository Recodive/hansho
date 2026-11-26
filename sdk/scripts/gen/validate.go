package main

import (
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"
)

// conform checks the x-conformance cases against the operations: each case names an operation with the same method
// and path, its bodies validate against that operation's schemas, and every operation has at least one case, so
// every SDK test suite exercises every operation.
func conform(d document, ops map[string]*Op) error {
	byID := map[string]operation{}
	for _, item := range d.Paths {
		for _, m := range methods {
			if rawOp, ok := item[m]; ok {
				var o operation
				if err := json.Unmarshal(rawOp, &o); err != nil {
					return err
				}
				byID[o.OperationID] = o
			}
		}
	}
	seen := map[string]bool{}
	names := map[string]bool{}
	var errs []string
	for _, c := range d.Conformance.Operations {
		bad := func(f string, a ...any) { errs = append(errs, "case "+c.Name+": "+fmt.Sprintf(f, a...)) }
		if names[c.Name] {
			bad("name used twice")
		}
		names[c.Name] = true
		op := ops[c.Operation]
		if op == nil {
			bad("unknown operation %q", c.Operation)
			continue
		}
		seen[op.ID] = true
		if c.Method != op.Method {
			bad("method %s, operation %s is %s", c.Method, op.ID, op.Method)
		}
		if !pathMatches(op.Path, c.Path) {
			bad("path %s does not match %s", c.Path, op.Path)
		}
		o := byID[op.ID]
		switch {
		case o.RequestBody == nil:
			if c.Request != nil {
				bad("operation %s takes no body", op.ID)
			}
		case c.Request == nil:
			bad("operation %s needs a body", op.ID)
		case op.Binary:
			if _, ok := o.RequestBody.Content[c.Request.ContentType]; !ok || c.Request.RawBase64 == "" {
				bad("needs raw_base64 and one of the operation's content types")
			}
		default:
			sc := o.RequestBody.Content["application/json"].Schema
			for _, e := range check(sc, c.Request.Body, "request", d.Components.Schemas, true) {
				bad("%s", e)
			}
		}
		if c.Response.Status != op.Status {
			bad("status %d, operation %s answers %d", c.Response.Status, op.ID, op.Status)
		}
		var sc *schema
		for code, r := range o.Responses {
			if code != "default" {
				if mt, ok := r.Content["application/json"]; ok {
					sc = mt.Schema
				}
			}
		}
		if sc == nil && c.Response.Body != nil {
			bad("operation %s answers without a body", op.ID)
		}
		if sc != nil {
			for _, e := range check(sc, c.Response.Body, "response", d.Components.Schemas, false) {
				bad("%s", e)
			}
		}
	}
	for id := range ops {
		if !seen[id] {
			errs = append(errs, "operation "+id+" has no x-conformance case")
		}
	}
	if len(errs) > 0 {
		slices.Sort(errs)
		return fmt.Errorf("spec does not hold together:\n  %s", strings.Join(errs, "\n  "))
	}
	return nil
}

func pathMatches(tmpl, path string) bool {
	path, _, _ = strings.Cut(path, "?")
	a, b := strings.Split(tmpl, "/"), strings.Split(path, "/")
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if strings.HasPrefix(a[i], "{") {
			if b[i] == "" {
				return false
			}
		} else if a[i] != b[i] {
			return false
		}
	}
	return true
}

// check validates v against sc. Unknown properties are allowed (the SDKs ignore them); null stands for an absent
// optional property. In a request, a readOnly or x-header property must not appear.
func check(sc *schema, v any, at string, schemas map[string]*schema, request bool) []string {
	if sc.Ref != "" {
		return check(schemas[refName(sc.Ref)], v, at, schemas, request)
	}
	if len(sc.Enum) > 0 && !slices.ContainsFunc(sc.Enum, func(e any) bool { return fmt.Sprint(e) == fmt.Sprint(v) }) {
		return []string{fmt.Sprintf("%s: %v is not one of %v", at, v, sc.Enum)}
	}
	switch sc.Type {
	case "":
		return nil
	case "object":
		m, ok := v.(map[string]any)
		if !ok {
			return []string{at + ": not an object"}
		}
		var errs []string
		for _, r := range sc.Required {
			if m[r] == nil {
				errs = append(errs, at+"."+r+": required")
			}
		}
		for k, pv := range m {
			ps := sc.Properties[k]
			if ps == nil || pv == nil {
				continue
			}
			if request && (ps.ReadOnly || ps.XHeader != "") {
				errs = append(errs, at+"."+k+": not sent in a request body")
			}
			errs = append(errs, check(ps, pv, at+"."+k, schemas, request)...)
		}
		return errs
	case "array":
		a, ok := v.([]any)
		if !ok {
			return []string{at + ": not an array"}
		}
		var errs []string
		for i, e := range a {
			errs = append(errs, check(sc.Items, e, fmt.Sprintf("%s[%d]", at, i), schemas, request)...)
		}
		return errs
	case "string":
		s, ok := v.(string)
		if !ok {
			return []string{at + ": not a string"}
		}
		if sc.Format == "date-time" {
			if _, err := time.Parse(time.RFC3339, s); err != nil {
				return []string{at + ": not an RFC 3339 time"}
			}
		}
	case "integer":
		if f, ok := v.(float64); !ok || f != math.Trunc(f) {
			return []string{at + ": not an integer"}
		}
	case "boolean":
		if _, ok := v.(bool); !ok {
			return []string{at + ": not a boolean"}
		}
	}
	return nil
}
