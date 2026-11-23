package declaration

import (
	"strings"
	"testing"
)

func TestParseFull(t *testing.T) {
	d, err := Parse([]byte(`
service: my-app
namespace: myapps
host: myapp.example.test
routes: [/healthz, "/api/items/{id}"]
page: true
runtime: node
edge: both
public: true
`))
	if err != nil {
		t.Fatal(err)
	}
	if d.Container != "my-app" || d.Title != "my-app" || d.MaxLabelValues != DefaultMaxLabelValues {
		t.Errorf("defaults not applied: %+v", d)
	}
	if d.Metric("build_info") != "my_app_build_info" || d.Web() != "my-app-web" {
		t.Errorf("derived names wrong: %s %s", d.Metric("build_info"), d.Web())
	}
}

func TestParseErrors(t *testing.T) {
	cases := []struct{ yaml, want string }{
		{"service: Bad\nnamespace: x\nhost: a.b\nruntime: go\nedge: traefik\n", "service: \"Bad\" must be lowercase"},
		{"namespace: x\nhost: a.b\nruntime: go\nedge: traefik\n", "service: required"},
		{"service: a\nnamespace: x\nhost: https://a.b\nruntime: go\nedge: traefik\n", "host: \"https://a.b\" must be a bare host"},
		{"service: a\nnamespace: x\nhost: a.b\nruntime: java\nedge: traefik\n", "runtime: \"java\" is not one of"},
		{"service: a\nnamespace: x\nhost: a.b\nruntime: go\nedge: nginx\n", "edge: \"nginx\" is not one of"},
		{"service: a\nnamespace: x\nhost: a.b\nruntime: go\nedge: traefik\npublic: true\n", "public: a public site is reached through Cloudflare"},
		{"service: a\nnamespace: x\nhost: a.b\nruntime: go\nedge: traefik\nroutes: [items]\n", "routes[0]: \"items\" must be a route pattern"},
		{"service: a\nnamespace: x\nhost: a.b\nruntime: go\nedge: traefik\nslo: {availability: 0.99}\n", "field slo not found"},
		{"service: a\nnamespace: x\nhost: a.b\nruntime: go\nedge: traefik\npages: true\n", "field pages not found"},
	}
	for _, c := range cases {
		_, err := Parse([]byte(c.yaml))
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("want error containing %q, got %v", c.want, err)
		}
	}
}

func TestAllErrorsAtOnce(t *testing.T) {
	_, err := Parse([]byte("service: ''\n"))
	if err == nil {
		t.Fatal("expected error")
	}
	for _, want := range []string{"service: required", "namespace: required", "host: required", "runtime: required", "edge: required"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("missing %q in %v", want, err)
		}
	}
}
