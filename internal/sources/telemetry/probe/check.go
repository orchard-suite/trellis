// Package probe measures one service against the telemetry standard,
// reading Prometheus, Loki, and Tempo. Each clause is a measurement: what
// the stores hold, with the queries that were run and what came back. It
// never says whether the service passes; the telemetry pack's rules judge
// the measurements.
package probe

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/orchard-suite/trellis/internal/sources/telemetry/declaration"
	"github.com/orchard-suite/trellis/internal/sources/telemetry/stores"
)

// Evidence is one query and what it returned.
type Evidence struct {
	Source string `json:"source"`
	Query  string `json:"query"`
	Got    string `json:"got"`
}

// Read is what every measurement carries: the queries behind it, and why
// a store could not be read, when one could not. A measurement with an
// error says nothing about the service.
type Read struct {
	Error    string     `json:"error,omitempty"`
	Evidence []Evidence `json:"evidence"`
}

func (r *Read) add(ev Evidence) { r.Evidence = append(r.Evidence, ev) }

// fail records a store that could not be read.
func (r *Read) fail(err error) { r.Error = "could not read the store: " + err.Error() }

// Failed says the measurement could not be taken.
func (r Read) Failed() bool { return r.Error != "" }

// Sources are the three stores a run reads.
type Sources struct {
	Prometheus stores.Prometheus
	Loki       stores.Loki
	Tempo      stores.Tempo
}

// Labels say how the estate's stores name a service's signals, as
// templates: {service}, {namespace}, {app} (the pods' app label),
// {container}, {host}, {host_dashed}
// (the host with dots as dashes), and {web} (the service's browser app)
// are filled from the declaration. The edge ones describe the estate's
// edge access log; left empty, the measurements that ask the edge say it
// is not configured.
type Labels struct {
	// Job selects the service's metrics.
	Job string `yaml:"job"`
	// Logs selects the service's log stream.
	Logs string `yaml:"logs"`
	// Browser selects the browser app's log stream.
	Browser string `yaml:"browser"`
	// EdgeAccess selects the edge's access log for the host, a stream of
	// JSON lines.
	EdgeAccess string `yaml:"edge_access"`
	// EdgePath, EdgeMethod, and EdgeStatus name the access log's fields.
	EdgePath   string `yaml:"edge_path"`
	EdgeMethod string `yaml:"edge_method"`
	EdgeStatus string `yaml:"edge_status"`
	// BrowserAgent is a line filter that only a browser's request matches.
	BrowserAgent string `yaml:"browser_agent"`
	// Router is the edge router's request counter for the host, a
	// Prometheus selector.
	Router string `yaml:"router"`
	// ViewBeacon is the path a public page posts one view to per load.
	ViewBeacon string `yaml:"view_beacon"`
}

// DefaultLabels are OpenTelemetry's and the Kubernetes log collector's
// usual names. The edge is the estate's own and has no default.
var DefaultLabels = Labels{
	Job:     `{job="{service}"}`,
	Logs:    `{namespace="{namespace}", app="{app}", container="{container}"}`,
	Browser: `{app="{web}"}`,
}

func (l Labels) withDefaults() Labels {
	if l.Job == "" {
		l.Job = DefaultLabels.Job
	}
	if l.Logs == "" {
		l.Logs = DefaultLabels.Logs
	}
	if l.Browser == "" {
		l.Browser = DefaultLabels.Browser
	}
	return l
}

// Options tune a run.
type Options struct {
	Labels Labels
	// Window is how far back logs, browser sessions, traces, and served
	// routes are looked for. Other metrics are read at now.
	Window time.Duration
	// Sample is how many recent log lines the log measurement reads.
	Sample int
}

// DefaultOptions is a day back and a hundred lines.
var DefaultOptions = Options{Window: 24 * time.Hour, Sample: 100}

// Measures are everything one run saw of one service. A measurement the
// declaration makes meaningless is not taken and stays nil: event_loop for
// a runtime other than node, edge_router for an edge without Traefik,
// edge_views for a site that is not public, and the browser ones and
// page_loads for a service with neither a page nor a public site.
type Measures struct {
	Service string        `json:"-"`
	Window  time.Duration `json:"-"`

	BuildInfo     BuildInfo     `json:"build_info"`
	HTTPDuration  HTTPDuration  `json:"http_duration"`
	ProcessCPU    Presence      `json:"process_cpu"`
	ProcessMemory Presence      `json:"process_memory"`
	EventLoop     *EventLoop    `json:"event_loop,omitempty"`
	Logs          Logs          `json:"logs"`
	TraceIDs      TraceIDs      `json:"trace_ids"`
	Cardinality   Cardinality   `json:"cardinality"`
	EdgeRouter    *EdgeRouter   `json:"edge_router,omitempty"`
	PageLoads     *PageLoads    `json:"page_loads,omitempty"`
	EdgeViews     *EdgeViews    `json:"edge_views,omitempty"`
	BrowserLogs   *BrowserLogs  `json:"browser_logs,omitempty"`
	BrowserTrace  *BrowserTrace `json:"browser_trace,omitempty"`
}

// reads are every measurement taken, for counting the ones that failed.
func (m Measures) reads() []Read {
	out := []Read{m.BuildInfo.Read, m.HTTPDuration.Read, m.ProcessCPU.Read, m.ProcessMemory.Read, m.Logs.Read, m.TraceIDs.Read, m.Cardinality.Read}
	if m.EventLoop != nil {
		out = append(out, m.EventLoop.Read)
	}
	if m.EdgeRouter != nil {
		out = append(out, m.EdgeRouter.Read)
	}
	if m.PageLoads != nil {
		out = append(out, m.PageLoads.Read)
	}
	if m.EdgeViews != nil {
		out = append(out, m.EdgeViews.Read)
	}
	if m.BrowserLogs != nil {
		out = append(out, m.BrowserLogs.Read)
	}
	if m.BrowserTrace != nil {
		out = append(out, m.BrowserTrace.Read)
	}
	return out
}

// Unread is why no measurement could be taken, or "" when one could.
func (m Measures) Unread() string {
	for _, r := range m.reads() {
		if !r.Failed() {
			return ""
		}
	}
	return m.BuildInfo.Error
}

type run struct {
	d   *declaration.Declaration
	src Sources
	opt Options
}

// Run measures one service.
func Run(ctx context.Context, d *declaration.Declaration, src Sources, opt Options) Measures {
	if opt.Window <= 0 {
		opt.Window = DefaultOptions.Window
	}
	if opt.Sample <= 0 {
		opt.Sample = DefaultOptions.Sample
	}
	opt.Labels = opt.Labels.withDefaults()
	r := &run{d: d, src: src, opt: opt}
	m := Measures{Service: d.Service, Window: opt.Window}
	m.BuildInfo = r.buildInfo(ctx)
	m.HTTPDuration = r.httpDuration(ctx)
	m.ProcessCPU = r.present(ctx, "process_cpu_time_seconds_total", "process_cpu_state")
	m.ProcessMemory = r.present(ctx, "process_memory_usage_bytes", "process_memory_kind")
	if d.Runtime == declaration.Node {
		el := r.eventLoop(ctx)
		m.EventLoop = &el
	}
	m.Logs = r.logs(ctx)
	m.TraceIDs = r.traceIDs(ctx)
	m.Cardinality = r.cardinality(ctx)
	if d.HasTraefik() {
		er := r.edgeRouter(ctx)
		m.EdgeRouter = &er
	}
	if d.Page || d.Public {
		pl := r.pageLoads(ctx)
		m.PageLoads = &pl
	}
	if d.Public {
		v := r.edgeViews(ctx)
		m.EdgeViews = &v
	}
	if d.Page {
		bl := r.browserLogs(ctx)
		bt := r.browserTrace(ctx)
		m.BrowserLogs, m.BrowserTrace = &bl, &bt
	}
	return m
}

// fill expands a label template for the service.
func (r *run) fill(tpl string) string {
	return strings.NewReplacer(
		"{service}", r.d.Service, "{namespace}", r.d.Namespace, "{app}", r.d.App, "{container}", r.d.Container,
		"{host_dashed}", strings.ReplaceAll(r.d.Host, ".", "-"), "{host}", r.d.Host, "{web}", r.d.Web(),
	).Replace(tpl)
}

func (r *run) job() string    { return r.fill(r.opt.Labels.Job) }
func (r *run) stream() string { return r.fill(r.opt.Labels.Logs) }

// errNoEdge is a measurement that needs the edge's access log in an estate
// that named none.
var errNoEdge = errors.New("the edge's access log is not configured")

func (r *run) edge() (string, error) {
	if r.opt.Labels.EdgeAccess == "" {
		return "", errNoEdge
	}
	return r.fill(r.opt.Labels.EdgeAccess), nil
}

func (r *run) window() string {
	w := r.opt.Window.Round(time.Minute)
	if w%time.Hour == 0 {
		return fmt.Sprintf("%dh", int(w.Hours()))
	}
	return fmt.Sprintf("%dm", int(w.Minutes()))
}

func (r *run) prom(ctx context.Context, expr string) ([]stores.Sample, Evidence, error) {
	samples, err := r.src.Prometheus.Query(ctx, expr)
	ev := Evidence{Source: "prometheus", Query: expr}
	if err != nil {
		ev.Got = "error: " + err.Error()
		return nil, ev, err
	}
	ev.Got = describeSamples(samples)
	return samples, ev, nil
}

// count runs one LogQL count.
func (r *run) count(ctx context.Context, q, unit string) (int, Evidence, error) {
	n, err := r.src.Loki.Count(ctx, q)
	ev := Evidence{Source: "loki", Query: q, Got: fmt.Sprintf("%.0f %s", n, unit)}
	if err != nil {
		ev.Got = "error: " + err.Error()
	}
	return int(n), ev, err
}

func describeSamples(samples []stores.Sample) string {
	if len(samples) == 0 {
		return "no series"
	}
	parts := make([]string, 0, len(samples))
	for i, s := range samples {
		if i == 10 {
			parts = append(parts, fmt.Sprintf("and %d more", len(samples)-10))
			break
		}
		parts = append(parts, labelSet(s.Labels)+" = "+s.Value)
	}
	return fmt.Sprintf("%d series: %s", len(samples), strings.Join(parts, "; "))
}

func labelSet(m map[string]string) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		if k != "__name__" {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%q", k, m[k]))
	}
	return "{" + strings.Join(parts, ", ") + "}"
}
