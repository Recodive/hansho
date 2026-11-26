package main

import (
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
)

// Spec is what the templates see.
type Spec struct {
	Version string
	Enums   []*Enum
	Types   []*Type
	Ops     []*Op
	Events  []*Event
}

type Enum struct {
	Name, Desc string
	Values     []string
}

type Type struct {
	Name, Desc string
	Fields     []*Field
	Event      string // the webhook event whose payload this is, if any
}

type Field struct {
	Wire     string // the JSON name
	Name     string // the SDK name, before casing (x-name, else the wire name)
	Desc     string
	Header   string // x-header: sent as this request header, never in the body
	T        *TypeRef
	Required bool
	ReadOnly bool
}

// TypeRef is a field type. Kind is string, int, int64, number, bool, time, any, array, enum or object; Ref names
// the enum or object; Elem is the array item; Values are the allowed values of an inline integer enum.
type TypeRef struct {
	Kind   string
	Ref    string
	Elem   *TypeRef
	Values []string
}

type Op struct {
	ID, Method, Path, Summary string
	PathParams                []string
	Query                     []string
	Status                    int      // the success status
	Result                    *TypeRef // nil without a body, Kind "inline" for an unnamed object
	BodyRef                   string   // the request schema when it is a named one
	Binary                    bool     // the request body is raw bytes
}

type Event struct {
	Name, Type string
}

// schema is the JSON Schema subset the generator understands. Anything else in the spec is an error, so a keyword
// the SDKs would silently ignore cannot slip in.
type schema struct {
	Ref         string             `json:"$ref"`
	Type        string             `json:"type"`
	Format      string             `json:"format"`
	Description string             `json:"description"`
	Properties  map[string]*schema `json:"properties"`
	Required    []string           `json:"required"`
	Items       *schema            `json:"items"`
	Enum        []any              `json:"enum"`
	ReadOnly    bool               `json:"readOnly"`
	XName       string             `json:"x-name"`
	XHeader     string             `json:"x-header"`
}

var known = map[string]bool{"$ref": true, "type": true, "format": true, "description": true, "properties": true,
	"required": true, "items": true, "enum": true, "readOnly": true, "x-name": true, "x-header": true}

func (s *schema) UnmarshalJSON(b []byte) error {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		return err
	}
	for k := range m {
		if !known[k] {
			return fmt.Errorf("unsupported schema keyword %q", k)
		}
	}
	type plain schema
	return json.Unmarshal(b, (*plain)(s))
}

type media struct {
	Schema *schema `json:"schema"`
}

type operation struct {
	OperationID string `json:"operationId"`
	Summary     string `json:"summary"`
	Parameters  []struct {
		Name string `json:"name"`
		In   string `json:"in"`
	} `json:"parameters"`
	RequestBody *struct {
		Content map[string]media `json:"content"`
	} `json:"requestBody"`
	Responses map[string]struct {
		Ref     string           `json:"$ref"`
		Content map[string]media `json:"content"`
	} `json:"responses"`
}

type document struct {
	Info struct {
		Version string `json:"version"`
	} `json:"info"`
	Paths      map[string]map[string]json.RawMessage `json:"paths"`
	Webhooks   map[string]map[string]operation       `json:"webhooks"`
	Components struct {
		Schemas map[string]*schema `json:"schemas"`
	} `json:"components"`
	Conformance struct {
		Operations []conformanceCase `json:"operations"`
	} `json:"x-conformance"`
}

type conformanceCase struct {
	Name      string `json:"name"`
	Operation string `json:"operation"`
	Method    string `json:"method"`
	Path      string `json:"path"`
	Request   *struct {
		ContentType string `json:"content_type"`
		Body        any    `json:"body"`
		RawBase64   string `json:"raw_base64"`
	} `json:"request"`
	Response struct {
		Status int `json:"status"`
		Body   any `json:"body"`
	} `json:"response"`
}

var methods = []string{"get", "put", "post", "patch", "delete"}

func load(raw []byte) (*Spec, error) {
	var d document
	if err := json.Unmarshal(raw, &d); err != nil {
		return nil, err
	}
	if d.Info.Version == "" {
		return nil, fmt.Errorf("info.version is empty")
	}
	s := &Spec{Version: d.Info.Version}
	schemas := d.Components.Schemas

	names := make([]string, 0, len(schemas))
	for n := range schemas {
		names = append(names, n)
	}
	sort.Strings(names)
	events := map[string]string{}
	for ev, item := range d.Webhooks {
		op, ok := item["post"]
		if !ok || op.RequestBody == nil {
			return nil, fmt.Errorf("webhook %s: needs a post with a request body", ev)
		}
		ref := op.RequestBody.Content["application/json"].Schema
		if ref == nil || ref.Ref == "" {
			return nil, fmt.Errorf("webhook %s: the payload must be a named schema", ev)
		}
		t := refName(ref.Ref)
		events[t] = ev
		s.Events = append(s.Events, &Event{Name: ev, Type: t})
	}
	sort.Slice(s.Events, func(i, j int) bool { return s.Events[i].Name < s.Events[j].Name })

	for _, n := range names {
		sc := schemas[n]
		switch {
		case len(sc.Enum) > 0:
			if sc.Type != "string" {
				return nil, fmt.Errorf("schema %s: a named enum must be a string enum", n)
			}
			e := &Enum{Name: n, Desc: sc.Description}
			for _, v := range sc.Enum {
				e.Values = append(e.Values, fmt.Sprint(v))
			}
			s.Enums = append(s.Enums, e)
		case sc.Type == "object":
			t := &Type{Name: n, Desc: sc.Description, Event: events[n]}
			if t.Desc == "" && t.Event != "" {
				t.Desc = "The data of the " + t.Event + " event."
			}
			props := make([]string, 0, len(sc.Properties))
			for p := range sc.Properties {
				props = append(props, p)
			}
			// ponytail: Go maps lose the spec's field order, so fields are in the order of the raw JSON object.
			props = orderOf(raw, n, props)
			for _, p := range props {
				ps := sc.Properties[p]
				tr, err := typeRef(ps, schemas)
				if err != nil {
					return nil, fmt.Errorf("schema %s.%s: %w", n, p, err)
				}
				f := &Field{Wire: p, Name: p, Desc: ps.Description, Header: ps.XHeader, T: tr,
					Required: slices.Contains(sc.Required, p), ReadOnly: ps.ReadOnly}
				if ps.XName != "" {
					f.Name = ps.XName
				}
				t.Fields = append(t.Fields, f)
			}
			for _, r := range sc.Required {
				if sc.Properties[r] == nil {
					return nil, fmt.Errorf("schema %s: required %q is not a property", n, r)
				}
			}
			s.Types = append(s.Types, t)
		default:
			return nil, fmt.Errorf("schema %s: must be an object or a string enum", n)
		}
	}

	paths := make([]string, 0, len(d.Paths))
	for p := range d.Paths {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	ops := map[string]*Op{}
	for _, p := range paths {
		item := d.Paths[p]
		for _, m := range methods {
			rawOp, ok := item[m]
			if !ok {
				continue
			}
			var o operation
			if err := json.Unmarshal(rawOp, &o); err != nil {
				return nil, fmt.Errorf("%s %s: %w", m, p, err)
			}
			if o.OperationID == "" {
				return nil, fmt.Errorf("%s %s: no operationId", m, p)
			}
			if ops[o.OperationID] != nil {
				return nil, fmt.Errorf("operationId %s used twice", o.OperationID)
			}
			op := &Op{ID: o.OperationID, Method: strings.ToUpper(m), Path: p, Summary: o.Summary}
			for _, seg := range strings.Split(p, "/") {
				if strings.HasPrefix(seg, "{") {
					op.PathParams = append(op.PathParams, strings.Trim(seg, "{}"))
				}
			}
			for _, q := range o.Parameters {
				if q.In == "query" {
					op.Query = append(op.Query, q.Name)
				}
			}
			if o.RequestBody != nil {
				if mt, ok := o.RequestBody.Content["application/json"]; ok {
					if mt.Schema.Ref != "" {
						op.BodyRef = refName(mt.Schema.Ref)
					}
				} else {
					op.Binary = true
				}
			}
			for code, r := range o.Responses {
				if code == "default" {
					continue
				}
				if op.Status != 0 {
					return nil, fmt.Errorf("%s: more than one success response", op.ID)
				}
				if op.Status, _ = strconv.Atoi(code); op.Status == 0 {
					return nil, fmt.Errorf("%s: bad status %q", op.ID, code)
				}
				if mt, ok := r.Content["application/json"]; ok {
					if mt.Schema.Type == "object" && mt.Schema.Ref == "" {
						op.Result = &TypeRef{Kind: "inline"}
					} else {
						t, err := typeRef(mt.Schema, schemas)
						if err != nil {
							return nil, fmt.Errorf("%s: %w", op.ID, err)
						}
						op.Result = t
					}
				}
			}
			ops[op.ID] = op
			s.Ops = append(s.Ops, op)
		}
	}
	sort.Slice(s.Ops, func(i, j int) bool { return s.Ops[i].ID < s.Ops[j].ID })
	if err := conform(d, ops); err != nil {
		return nil, err
	}
	return s, nil
}

func refName(r string) string { return strings.TrimPrefix(r, "#/components/schemas/") }

func typeRef(sc *schema, schemas map[string]*schema) (*TypeRef, error) {
	if sc.Ref != "" {
		n := refName(sc.Ref)
		t := schemas[n]
		if t == nil {
			return nil, fmt.Errorf("unknown $ref %s", sc.Ref)
		}
		if len(t.Enum) > 0 {
			return &TypeRef{Kind: "enum", Ref: n}, nil
		}
		return &TypeRef{Kind: "object", Ref: n}, nil
	}
	switch sc.Type {
	case "":
		return &TypeRef{Kind: "any"}, nil
	case "string":
		switch sc.Format {
		case "":
			return &TypeRef{Kind: "string"}, nil
		case "date-time":
			return &TypeRef{Kind: "time"}, nil
		}
	case "integer":
		t := &TypeRef{Kind: "int"}
		switch sc.Format {
		case "":
		case "int64":
			t.Kind = "int64"
		default:
			return nil, fmt.Errorf("unsupported integer format %q", sc.Format)
		}
		for _, v := range sc.Enum {
			t.Values = append(t.Values, fmt.Sprint(v))
		}
		return t, nil
	case "boolean":
		return &TypeRef{Kind: "bool"}, nil
	case "array":
		if sc.Items == nil {
			return nil, fmt.Errorf("array without items")
		}
		e, err := typeRef(sc.Items, schemas)
		if err != nil {
			return nil, err
		}
		return &TypeRef{Kind: "array", Elem: e}, nil
	}
	if len(sc.Enum) > 0 {
		return nil, fmt.Errorf("an inline string enum must be a named schema")
	}
	return nil, fmt.Errorf("unsupported type %q format %q (name it in components.schemas)", sc.Type, sc.Format)
}

// orderOf returns props in the order the schema lists them in the raw document.
func orderOf(raw []byte, name string, props []string) []string {
	var doc struct {
		Components struct {
			Schemas map[string]struct {
				Properties json.RawMessage `json:"properties"`
			} `json:"schemas"`
		} `json:"components"`
	}
	json.Unmarshal(raw, &doc)
	dec := json.NewDecoder(strings.NewReader(string(doc.Components.Schemas[name].Properties)))
	var order []string
	depth := 0
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		switch v := tok.(type) {
		case json.Delim:
			if v == '{' || v == '[' {
				depth++
			} else {
				depth--
			}
		case string:
			if depth == 1 && slices.Contains(props, v) && !slices.Contains(order, v) {
				order = append(order, v)
				// skip the value
				var skip json.RawMessage
				dec.Decode(&skip)
			}
		}
	}
	return order
}
