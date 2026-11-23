// Package openapi reads an OpenAPI 3 definition into what the interfaces
// pack judges: its operations, each with its parameters, its security, its
// error responses, and what its body and success response carry. It reads
// JSON or YAML and resolves local references; it validates nothing the
// rules do not ask about.
package openapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// ErrNotOpenAPI is a definition in another language: AsyncAPI, protobuf, a
// GraphQL schema. The pack's shape rules read OpenAPI only.
var ErrNotOpenAPI = errors.New("not an OpenAPI definition")

// Doc is one definition.
type Doc struct {
	// Version is the OpenAPI version it is written in, such as 3.1.0.
	Version string
	Title   string
	// Schemes are the security schemes it declares, by name.
	Schemes    []string
	Operations []Operation
	root       map[string]any
}

// Operation is one method on one path.
type Operation struct {
	Method, Path string
	ID           string
	Summary      string
	Description  string
	// Open is true when the operation needs no credential: it names an
	// empty security list, or inherits none.
	Open bool
	// OpenOnPurpose is true when the operation itself says security: [].
	OpenOnPurpose bool
	Params        []Param
	Body          Body
	// Errors are its 4xx, 5xx, and default responses, by status.
	Errors map[string]Response
	// Success is its first 2xx response.
	Success *Response
}

// Key is the operation with its path's parameter names left out, so a
// renamed parameter is the same operation.
func (o Operation) Key() string {
	return strings.ToUpper(o.Method) + " " + paramName.ReplaceAllString(o.Path, "{}")
}

// Name is how a person reads the operation: GET /v1/things.
func (o Operation) Name() string { return strings.ToUpper(o.Method) + " " + o.Path }

var paramName = regexp.MustCompile(`\{[^}]*\}`)

// Param is one parameter outside the path.
type Param struct {
	Name, In string
	Required bool
}

// Body is what the request body needs.
type Body struct {
	Present, Required bool
	// Fields are the top-level properties of its JSON schema that it
	// requires.
	Fields []string
}

// Response is one documented response.
type Response struct {
	// Shape names the schema its body follows: the schema's reference, or
	// the schema itself written out, or "no body".
	Shape string
	// Fields are the top-level properties of its JSON schema.
	Fields []string
}

// Parse reads a definition. One that is not OpenAPI 3 answers
// ErrNotOpenAPI, or an error saying what it is instead.
func Parse(data []byte) (*Doc, error) {
	var raw any
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("neither JSON nor YAML: %w", err)
	}
	root, ok := raw.(map[string]any)
	if !ok {
		return nil, ErrNotOpenAPI
	}
	if v, ok := root["swagger"]; ok {
		return nil, fmt.Errorf("it is Swagger %v; write it as OpenAPI 3", v)
	}
	raw, ok = root["openapi"]
	if !ok || raw == nil {
		return nil, ErrNotOpenAPI
	}
	version := fmt.Sprint(raw)
	if !strings.HasPrefix(version, "3.") {
		return nil, fmt.Errorf("OpenAPI %s is not a version 3 definition", version)
	}
	d := &Doc{Version: version, root: root}
	if info, ok := root["info"].(map[string]any); ok {
		d.Title, _ = info["title"].(string)
	}
	for name := range mapAt(root, "components", "securitySchemes") {
		d.Schemes = append(d.Schemes, name)
	}
	sort.Strings(d.Schemes)
	global, hasGlobal := root["security"].([]any)
	paths := mapAt(root, "paths")
	names := make([]string, 0, len(paths))
	for p := range paths {
		names = append(names, p)
	}
	sort.Strings(names)
	for _, p := range names {
		item, _ := d.resolve(paths[p]).(map[string]any)
		shared := d.params(item["parameters"])
		for _, method := range methods {
			raw, ok := item[method].(map[string]any)
			if !ok {
				continue
			}
			op := Operation{Method: method, Path: p, Errors: map[string]Response{}}
			op.ID, _ = raw["operationId"].(string)
			op.Summary, _ = raw["summary"].(string)
			op.Description, _ = raw["description"].(string)
			security, own := raw["security"].([]any)
			if own {
				op.OpenOnPurpose = len(security) == 0
			} else if hasGlobal {
				security = global
			}
			op.Open = open(security)
			op.Params = merge(shared, d.params(raw["parameters"]))
			op.Body = d.body(raw["requestBody"])
			d.responses(raw["responses"], &op)
			d.Operations = append(d.Operations, op)
		}
	}
	return d, nil
}

var methods = []string{"get", "put", "post", "delete", "patch", "head", "options", "trace"}

// open is a security list no credential is needed for: empty, or with an
// empty requirement among its choices.
func open(security []any) bool {
	if len(security) == 0 {
		return true
	}
	for _, req := range security {
		if m, ok := req.(map[string]any); ok && len(m) == 0 {
			return true
		}
	}
	return false
}

func mapAt(m map[string]any, keys ...string) map[string]any {
	for _, k := range keys {
		next, ok := m[k].(map[string]any)
		if !ok {
			return nil
		}
		m = next
	}
	return m
}

// resolve follows a local $ref, up to a few hops; anything else is
// returned as it is.
func (d *Doc) resolve(v any) any {
	for range 8 {
		m, ok := v.(map[string]any)
		if !ok {
			return v
		}
		ref, ok := m["$ref"].(string)
		if !ok || !strings.HasPrefix(ref, "#/") {
			return v
		}
		var at any = d.root
		for _, part := range strings.Split(ref[2:], "/") {
			part = strings.ReplaceAll(strings.ReplaceAll(part, "~1", "/"), "~0", "~")
			mm, ok := at.(map[string]any)
			if !ok {
				return v
			}
			at = mm[part]
		}
		if at == nil {
			return v
		}
		v = at
	}
	return v
}

func (d *Doc) params(raw any) []Param {
	list, _ := raw.([]any)
	var out []Param
	for _, p := range list {
		m, ok := d.resolve(p).(map[string]any)
		if !ok {
			continue
		}
		name, _ := m["name"].(string)
		in, _ := m["in"].(string)
		required, _ := m["required"].(bool)
		if name == "" || in == "" || in == "path" {
			continue
		}
		out = append(out, Param{Name: name, In: in, Required: required})
	}
	return out
}

// merge lays an operation's parameters over its path's.
func merge(shared, own []Param) []Param {
	out := append([]Param{}, own...)
	for _, s := range shared {
		found := false
		for _, o := range own {
			found = found || (o.Name == s.Name && o.In == s.In)
		}
		if !found {
			out = append(out, s)
		}
	}
	return out
}

func (d *Doc) body(raw any) Body {
	m, ok := d.resolve(raw).(map[string]any)
	if !ok {
		return Body{}
	}
	b := Body{Present: true}
	b.Required, _ = m["required"].(bool)
	if schema := d.schemaOf(m); schema != nil {
		b.Fields = stringList(schema["required"])
		sort.Strings(b.Fields)
	}
	return b
}

func (d *Doc) responses(raw any, op *Operation) {
	m, _ := raw.(map[string]any)
	codes := make([]string, 0, len(m))
	for code := range m {
		codes = append(codes, code)
	}
	sort.Strings(codes)
	for _, code := range codes {
		lower := strings.ToLower(code)
		isError := lower == "default" || strings.HasPrefix(lower, "4") || strings.HasPrefix(lower, "5")
		isSuccess := strings.HasPrefix(lower, "2")
		if !isError && !(isSuccess && op.Success == nil) {
			continue
		}
		r := d.response(m[code])
		if isError {
			op.Errors[code] = r
		} else {
			op.Success = &r
		}
	}
}

func (d *Doc) response(raw any) Response {
	m, ok := d.resolve(raw).(map[string]any)
	if !ok {
		return Response{Shape: "no body"}
	}
	content, _ := m["content"].(map[string]any)
	if len(content) == 0 {
		return Response{Shape: "no body"}
	}
	media := pickMedia(content)
	mt, _ := content[media].(map[string]any)
	schemaRaw := mt["schema"]
	if schemaRaw == nil {
		return Response{Shape: "no body"}
	}
	r := Response{Shape: shape(schemaRaw)}
	if schema, ok := d.resolve(schemaRaw).(map[string]any); ok {
		for f := range mapAt(schema, "properties") {
			r.Fields = append(r.Fields, f)
		}
		sort.Strings(r.Fields)
	}
	return r
}

// schemaOf is the JSON schema of a body or response, resolved.
func (d *Doc) schemaOf(m map[string]any) map[string]any {
	content, _ := m["content"].(map[string]any)
	if len(content) == 0 {
		return nil
	}
	mt, _ := content[pickMedia(content)].(map[string]any)
	schema, _ := d.resolve(mt["schema"]).(map[string]any)
	return schema
}

// pickMedia prefers JSON, then problem JSON, then the first listed.
func pickMedia(content map[string]any) string {
	for _, want := range []string{"application/json", "application/problem+json"} {
		if _, ok := content[want]; ok {
			return want
		}
	}
	keys := make([]string, 0, len(content))
	for k := range content {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys[0]
}

// shape names a schema: its reference, or the schema written out with its
// keys sorted, so two copies of the same inline schema are one shape.
func shape(schema any) string {
	if m, ok := schema.(map[string]any); ok {
		if ref, ok := m["$ref"].(string); ok {
			return ref
		}
	}
	b, err := json.Marshal(schema)
	if err != nil {
		return "an inline schema"
	}
	return string(b)
}

func stringList(v any) []string {
	list, _ := v.([]any)
	var out []string
	for _, x := range list {
		if s, ok := x.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// majorVersion is the v<N> segment an operation's path sits under, or "".
func majorVersion(path string) string {
	for _, seg := range strings.Split(path, "/") {
		if versionSegment.MatchString(seg) {
			return seg
		}
	}
	return ""
}

var versionSegment = regexp.MustCompile(`^v[0-9]+$`)

// Versioned says whether the operation's path sits under a major version,
// /v1/ and the like, or the definition's servers all do.
func (d *Doc) Versioned(o Operation) bool {
	if majorVersion(o.Path) != "" {
		return true
	}
	servers, _ := d.root["servers"].([]any)
	if len(servers) == 0 {
		return false
	}
	for _, s := range servers {
		m, _ := s.(map[string]any)
		u, _ := m["url"].(string)
		if majorVersion(strings.SplitN(strings.TrimPrefix(strings.TrimPrefix(u, "https://"), "http://"), "?", 2)[0]) == "" {
			return false
		}
	}
	return true
}

// Facts is the definition as the interfaces pack reads it: the security
// schemes, every operation with what the shape rules ask of it, the error
// shapes across the definition, and the operation ids used twice.
type Facts struct {
	Version     string          `json:"version"`
	Schemes     []string        `json:"schemes"`
	Operations  []OperationFact `json:"operations"`
	ErrorShapes []string        `json:"error_shapes"`
	DuplicateID []string        `json:"duplicate_ids"`
}

// OperationFact is one operation as a rule reads it.
type OperationFact struct {
	Name          string   `json:"name"`
	ID            string   `json:"id"`
	Described     bool     `json:"described"`
	Versioned     bool     `json:"versioned"`
	Open          bool     `json:"open"`
	OpenOnPurpose bool     `json:"open_on_purpose"`
	Errors        []string `json:"errors"`
}

// Facts reads the definition into what the pack judges.
func (d *Doc) Facts() Facts {
	f := Facts{Version: d.Version, Schemes: nonNil(d.Schemes), Operations: []OperationFact{}, ErrorShapes: []string{}, DuplicateID: []string{}}
	shapes := map[string]bool{}
	ids := map[string]int{}
	for _, o := range d.Operations {
		of := OperationFact{
			Name: o.Name(), ID: o.ID, Versioned: d.Versioned(o), Open: o.Open, OpenOnPurpose: o.OpenOnPurpose,
			Described: strings.TrimSpace(o.Summary) != "" || strings.TrimSpace(o.Description) != "",
			Errors:    []string{},
		}
		for code, r := range o.Errors {
			of.Errors = append(of.Errors, code)
			shapes[shapeName(r.Shape)] = true
		}
		sort.Strings(of.Errors)
		if o.ID != "" {
			ids[o.ID]++
		}
		f.Operations = append(f.Operations, of)
	}
	for s := range shapes {
		f.ErrorShapes = append(f.ErrorShapes, s)
	}
	sort.Strings(f.ErrorShapes)
	for id, n := range ids {
		if n > 1 {
			f.DuplicateID = append(f.DuplicateID, id)
		}
	}
	sort.Strings(f.DuplicateID)
	return f
}

// shapeName is how a person reads an error shape: a reference by its last
// part, "no body", or an inline schema by a short digest of itself, so two
// copies of one inline schema are one shape.
func shapeName(shape string) string {
	switch {
	case strings.HasPrefix(shape, "#/"):
		return shape[strings.LastIndex(shape, "/")+1:]
	case shape == "no body":
		return shape
	}
	h := fnv.New32a()
	h.Write([]byte(shape))
	return fmt.Sprintf("an inline schema (%08x)", h.Sum32())
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
