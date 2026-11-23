// Package declaration reads and validates trellis.yaml, the one short file
// that says what a service is, so the telemetry source can probe it.
package declaration

import (
	_ "embed"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
	"unicode"

	"gopkg.in/yaml.v3"
)

// Schema is the JSON Schema of trellis.yaml, for editors and `trellis schema`.
//
//go:embed trellis.schema.json
var Schema []byte

// Runtime is what the process runs on. It decides which process clauses apply.
type Runtime string

const (
	Go     Runtime = "go"
	Node   Runtime = "node"
	Rust   Runtime = "rust"
	Python Runtime = "python"
)

// Edge is what sits in front of the service and sees its requests.
type Edge string

const (
	Traefik    Edge = "traefik"
	Cloudflare Edge = "cloudflare"
	Both       Edge = "both"
)

// Declaration is one service's trellis.yaml.
type Declaration struct {
	Service   string   `yaml:"service" json:"service"`
	Title     string   `yaml:"title,omitempty" json:"title,omitempty"`
	Namespace string   `yaml:"namespace" json:"namespace"`
	Container string   `yaml:"container,omitempty" json:"container,omitempty"`
	Host      string   `yaml:"host" json:"host"`
	Routes    []string `yaml:"routes,omitempty" json:"routes,omitempty"`
	Page      bool     `yaml:"page" json:"page"`
	Runtime   Runtime  `yaml:"runtime" json:"runtime"`
	Edge      Edge     `yaml:"edge" json:"edge"`
	Public    bool     `yaml:"public" json:"public"`
	// MaxLabelValues is how many values one label may take on the service's
	// metrics before the cardinality clause fails. Zero means the default.
	MaxLabelValues int `yaml:"max_label_values,omitempty" json:"max_label_values,omitempty"`
}

// DefaultMaxLabelValues is the house's bar: a label with more than a few
// dozen values is a bug.
const DefaultMaxLabelValues = 50

var nameRE = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)
var hostRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$`)

// Load reads and validates one declaration file.
func Load(path string) (*Declaration, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	d, err := Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return d, nil
}

// Parse decodes and validates a declaration. Unknown keys are errors, so a
// typo never silently turns a clause off.
func Parse(raw []byte) (*Declaration, error) {
	var d Declaration
	dec := yaml.NewDecoder(strings.NewReader(string(raw)))
	dec.KnownFields(true)
	if err := dec.Decode(&d); err != nil {
		return nil, fmt.Errorf("not a valid declaration: %w", err)
	}
	if err := d.Validate(); err != nil {
		return nil, err
	}
	d.applyDefaults()
	return &d, nil
}

func (d *Declaration) applyDefaults() {
	if d.Container == "" {
		d.Container = d.Service
	}
	if d.Title == "" {
		d.Title = d.Service
	}
	if d.MaxLabelValues == 0 {
		d.MaxLabelValues = DefaultMaxLabelValues
	}
}

// Validate reports every problem at once, each naming the field and what it
// must be.
func (d *Declaration) Validate() error {
	var errs []error
	bad := func(field, msg string) { errs = append(errs, fmt.Errorf("%s: %s", field, msg)) }
	switch {
	case d.Service == "":
		bad("service", "required; the repository's name, such as beacon")
	case !nameRE.MatchString(d.Service):
		bad("service", fmt.Sprintf("%q must be lowercase letters, digits, and hyphens, starting with a letter", d.Service))
	}
	switch {
	case d.Namespace == "":
		bad("namespace", "required; the Kubernetes namespace the service runs in")
	case !nameRE.MatchString(d.Namespace):
		bad("namespace", fmt.Sprintf("%q must be lowercase letters, digits, and hyphens", d.Namespace))
	}
	if d.Container != "" && !nameRE.MatchString(d.Container) {
		bad("container", fmt.Sprintf("%q must be lowercase letters, digits, and hyphens", d.Container))
	}
	switch {
	case d.Host == "":
		bad("host", "required; the site's host, such as app.example.com")
	case !hostRE.MatchString(d.Host):
		bad("host", fmt.Sprintf("%q must be a bare host name with no scheme or path", d.Host))
	}
	for i, r := range d.Routes {
		if !strings.HasPrefix(r, "/") {
			bad(fmt.Sprintf("routes[%d]", i), fmt.Sprintf("%q must be a route pattern starting with /, such as /api/items/{id}", r))
		}
		for _, c := range r {
			if unicode.IsSpace(c) {
				bad(fmt.Sprintf("routes[%d]", i), fmt.Sprintf("%q must not contain spaces", r))
				break
			}
		}
	}
	switch d.Runtime {
	case Go, Node, Rust, Python:
	case "":
		bad("runtime", "required; one of go, node, rust, python")
	default:
		bad("runtime", fmt.Sprintf("%q is not one of go, node, rust, python", d.Runtime))
	}
	switch d.Edge {
	case Traefik, Cloudflare, Both:
	case "":
		bad("edge", "required; one of traefik, cloudflare, both")
	default:
		bad("edge", fmt.Sprintf("%q is not one of traefik, cloudflare, both", d.Edge))
	}
	if d.Public && d.Edge == Traefik {
		bad("public", "a public site is reached through Cloudflare; set edge to cloudflare or both")
	}
	if d.MaxLabelValues < 0 {
		bad("max_label_values", "must be a positive count")
	}
	return errors.Join(errs...)
}

// Web is the app name the service's page reports its browser telemetry as.
func (d *Declaration) Web() string { return d.Service + "-web" }

// HasTraefik says the cluster's Traefik router sees this service.
func (d *Declaration) HasTraefik() bool { return d.Edge == Traefik || d.Edge == Both }

// Metric is the Prometheus name of one of the service's own metrics, such as
// beacon_build_info for build_info.
func (d *Declaration) Metric(what string) string {
	return strings.ReplaceAll(d.Service, "-", "_") + "_" + what
}
