package openapi

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

const base = `{
  "openapi": "3.1.0",
  "info": { "title": "Things", "version": "1" },
  "components": {
    "securitySchemes": { "bearer": { "type": "http", "scheme": "bearer" } },
    "parameters": { "Limit": { "name": "limit", "in": "query" } },
    "responses": { "Problem": { "description": "an error", "content": { "application/problem+json": { "schema": { "$ref": "#/components/schemas/Error" } } } } },
    "schemas": {
      "Error": { "type": "object", "properties": { "title": { "type": "string" } } },
      "Thing": { "type": "object", "required": ["name"], "properties": { "name": { "type": "string" }, "size": { "type": "number" } } }
    }
  },
  "security": [{ "bearer": [] }],
  "paths": {
    "/v1/things": {
      "parameters": [{ "$ref": "#/components/parameters/Limit" }],
      "get": {
        "operationId": "listThings",
        "responses": {
          "200": { "description": "ok", "content": { "application/json": { "schema": { "$ref": "#/components/schemas/Thing" } } } },
          "4XX": { "$ref": "#/components/responses/Problem" }
        }
      },
      "post": {
        "operationId": "addThing",
        "security": [],
        "requestBody": { "content": { "application/json": { "schema": { "$ref": "#/components/schemas/Thing" } } } },
        "responses": { "201": { "description": "made" } }
      }
    },
    "/v1/things/{id}": {
      "delete": { "operationId": "dropThing", "security": [{}], "responses": { "204": { "description": "gone" } } }
    }
  }
}`

func TestParseReadsWhatTheRulesJudge(t *testing.T) {
	d, err := Parse([]byte(base))
	if err != nil {
		t.Fatal(err)
	}
	if d.Version != "3.1.0" || d.Title != "Things" || !reflect.DeepEqual(d.Schemes, []string{"bearer"}) || len(d.Operations) != 3 {
		t.Fatalf("doc: %+v", d)
	}
	list, add, drop := d.Operations[0], d.Operations[1], d.Operations[2]
	if list.Name() != "GET /v1/things" || list.Open || len(list.Params) != 1 || list.Params[0].Name != "limit" {
		t.Errorf("list takes the path's parameter and the global security: %+v", list)
	}
	if list.Errors["4XX"].Shape != "#/components/schemas/Error" || !reflect.DeepEqual(list.Success.Fields, []string{"name", "size"}) {
		t.Errorf("list's responses resolve through references: %+v %+v", list.Errors, list.Success)
	}
	if !add.Open || !add.OpenOnPurpose || !reflect.DeepEqual(add.Body.Fields, []string{"name"}) || add.Body.Required {
		t.Errorf("add is open on purpose and requires name in an optional body: %+v", add)
	}
	if !drop.Open || drop.OpenOnPurpose || drop.Key() != "DELETE /v1/things/{}" {
		t.Errorf("drop takes an empty requirement, so no credential: %+v", drop)
	}
}

func TestParseTellsOtherLanguagesApart(t *testing.T) {
	if _, err := Parse([]byte("asyncapi: 3.0.0\n")); !errors.Is(err, ErrNotOpenAPI) {
		t.Errorf("asyncapi: %v", err)
	}
	if _, err := Parse([]byte(`{"swagger":"2.0"}`)); err == nil || !strings.Contains(err.Error(), "Swagger") {
		t.Errorf("swagger: %v", err)
	}
	if _, err := Parse([]byte("openapi: 2.9\n")); err == nil || errors.Is(err, ErrNotOpenAPI) {
		t.Errorf("a version other than 3: %v", err)
	}
	if _, err := Parse([]byte("{ not: [json")); err == nil {
		t.Error("garbage parsed")
	}
}

func TestVersionedReadsThePathOrEveryServer(t *testing.T) {
	d, _ := Parse([]byte("openapi: 3.0.3\nservers: [{url: 'https://api.example.test/v2'}]\npaths: {/things: {get: {}}}\n"))
	if !d.Versioned(d.Operations[0]) {
		t.Error("a server under /v2 versions its paths")
	}
	d, _ = Parse([]byte("openapi: 3.0.3\nservers: [{url: /}]\npaths: {/api/things: {get: {}}}\n"))
	if d.Versioned(d.Operations[0]) {
		t.Error("/api/things carries no version")
	}
}

func TestBreaking(t *testing.T) {
	was, _ := Parse([]byte(base))
	change := func(swaps ...string) *Doc {
		doc := base
		for i := 0; i+1 < len(swaps); i += 2 {
			if !strings.Contains(doc, swaps[i]) {
				t.Fatalf("no %q to swap", swaps[i])
			}
			doc = strings.Replace(doc, swaps[i], swaps[i+1], 1)
		}
		d, err := Parse([]byte(doc))
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	cases := map[string]struct {
		head *Doc
		want []string
	}{
		"unchanged":           {was, nil},
		"an operation added":  {change(`"delete":`, `"put": { "operationId": "putThing", "responses": {} }, "delete":`), nil},
		"a parameter renamed": {change(`"/v1/things/{id}"`, `"/v1/things/{thing}"`), nil},
		"an operation gone": {change(`"delete": { "operationId": "dropThing", "security": [{}], "responses": { "204": { "description": "gone" } } }`, `"patch": { "responses": {} }`),
			[]string{"DELETE /v1/things/{id} is gone"}},
		"a parameter now required": {change(`"name": "limit", "in": "query"`, `"name": "limit", "in": "query", "required": true`),
			[]string{"GET /v1/things now requires the query parameter limit", "POST /v1/things now requires the query parameter limit"}},
		"a body now required": {change(`"requestBody": {`, `"requestBody": { "required": true,`),
			[]string{"POST /v1/things now requires a body"}},
		"a field now required": {change(`"required": ["name"]`, `"required": ["name", "size"]`),
			[]string{"POST /v1/things now requires size in its body"}},
		"a field no longer returned": {change(`, "size": { "type": "number" }`, ``),
			[]string{"GET /v1/things no longer returns size"}},
	}
	for name, c := range cases {
		if got := Breaking(was, c.head); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: got %q, want %q", name, got, c.want)
		}
	}
}

func TestRetiringAVersionBesideANewerOneIsNoBreak(t *testing.T) {
	v1, _ := Parse([]byte("openapi: 3.1.0\npaths: {/v1/a: {get: {}}, /v2/a: {get: {}}}\n"))
	v2, _ := Parse([]byte("openapi: 3.1.0\npaths: {/v2/a: {get: {}}}\n"))
	if got := Breaking(v1, v2); len(got) != 0 {
		t.Errorf("v1 retired once v2 shipped beside it: %q", got)
	}
	gone, _ := Parse([]byte("openapi: 3.1.0\npaths: {/v1/b: {get: {}}}\n"))
	if got := Breaking(v1, gone); len(got) != 2 {
		t.Errorf("dropping both versions breaks both: %q", got)
	}
}

func TestMovingUnderAVersionIsNoBreak(t *testing.T) {
	old, _ := Parse([]byte("openapi: 3.1.0\npaths: {/api/things: {get: {}}, /ingest/runs: {post: {}}, /api/gone: {get: {}}}\n"))
	now, _ := Parse([]byte("openapi: 3.1.0\npaths: {/v1/things: {get: {parameters: [{name: q, in: query, required: true}]}}, /v1/ingest/runs: {post: {}}}\n"))
	got := Breaking(old, now)
	if len(got) != 2 || got[0] != "GET /api/gone is gone" || got[1] != "GET /v1/things now requires the query parameter q" {
		t.Errorf("a moved route is compared as itself, and a dropped one is still gone: %q", got)
	}
}
