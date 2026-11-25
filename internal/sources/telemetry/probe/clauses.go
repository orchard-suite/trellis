package probe

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// BuildInfo is the service's build_info gauge: how many series it has,
// the versions they name, and how many name none.
type BuildInfo struct {
	Read
	Metric      string   `json:"metric"`
	Series      int      `json:"series"`
	Versions    []string `json:"versions"`
	Unversioned int      `json:"unversioned"`
}

func (r *run) buildInfo(ctx context.Context) BuildInfo {
	m := BuildInfo{Metric: r.d.Metric("build_info"), Versions: []string{}}
	samples, ev, err := r.prom(ctx, fmt.Sprintf("max by (version) (%s%s)", m.Metric, r.job()))
	m.add(ev)
	if err != nil {
		m.fail(err)
		return m
	}
	m.Series = len(samples)
	for _, s := range samples {
		if v := s.Labels["version"]; v != "" {
			m.Versions = append(m.Versions, v)
		} else {
			m.Unversioned++
		}
	}
	sort.Strings(m.Versions)
	return m
}

// HTTPDuration is the request duration histogram across the window: the
// route patterns it carries, whether some requests carry none, the routes
// the declaration names, and, for each declared route with no series, how
// many requests the edge passed it in the window. A declared route the edge
// served that never reported is missing; one nobody called says nothing.
type HTTPDuration struct {
	Read
	Reported   bool           `json:"reported"`
	Routes     []string       `json:"routes"`
	Unlabelled bool           `json:"unlabelled"`
	Declared   []string       `json:"declared"`
	EdgeHits   map[string]int `json:"edge_hits"`
}

// httpDuration looks across the window, not at now: a counter series lives
// only as long as the process that serves it, so a route answered before the
// last restart still counts.
func (r *run) httpDuration(ctx context.Context) HTTPDuration {
	m := HTTPDuration{Routes: []string{}, Declared: append([]string{}, r.d.Routes...), EdgeHits: map[string]int{}}
	expr := fmt.Sprintf("count by (http_route) (max_over_time(http_server_request_duration_seconds_count%s[%s]))", r.job(), r.window())
	samples, ev, err := r.prom(ctx, expr)
	m.add(ev)
	if err != nil {
		m.fail(err)
		return m
	}
	m.Reported = len(samples) > 0
	seen := map[string]bool{}
	for _, s := range samples {
		route := s.Labels["http_route"]
		if route == "" {
			m.Unlabelled = true
			continue
		}
		if !seen[route] {
			seen[route] = true
			m.Routes = append(m.Routes, route)
		}
	}
	sort.Strings(m.Routes)
	// Durations with no route at all say nothing of any one route.
	if len(m.Routes) == 0 {
		return m
	}
	for _, want := range m.Declared {
		if seen[want] {
			continue
		}
		n, edge, err := r.edgeRequests(ctx, want)
		m.add(edge)
		if err != nil {
			m.fail(err)
			return m
		}
		m.EdgeHits[want] = n
	}
	return m
}

// edgeRequests counts the requests the edge passed the host on one route
// pattern in the window.
func (r *run) edgeRequests(ctx context.Context, route string) (int, Evidence, error) {
	edge, err := r.edge()
	if err != nil {
		return 0, Evidence{Source: "loki", Got: err.Error()}, err
	}
	path := r.opt.Labels.EdgePath
	q := fmt.Sprintf(`sum(count_over_time(%s | json %s | %s=~%q [%s]))`, edge, path, path, routeRegexp(route), r.window())
	return r.count(ctx, q, "requests")
}

// routeRegexp matches the request paths of a route pattern, query string
// allowed: {name} is one segment, {name...} the rest, {$} the end.
func routeRegexp(route string) string {
	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(route); {
		if route[i] == '{' {
			end := strings.IndexByte(route[i:], '}')
			if end > 0 {
				name := route[i+1 : i+end]
				switch {
				case name == "$":
				case strings.HasSuffix(name, "..."):
					b.WriteString(".*")
				default:
					b.WriteString("[^/?]+")
				}
				i += end + 1
				continue
			}
		}
		b.WriteString(regexp.QuoteMeta(route[i : i+1]))
		i++
	}
	b.WriteString(`(\?.*)?$`)
	return b.String()
}

// Presence is one process metric: how many series it has, and the values
// its attribute takes on them.
type Presence struct {
	Read
	Metric    string   `json:"metric"`
	Attribute string   `json:"attribute"`
	Series    int      `json:"series"`
	Values    []string `json:"values"`
}

func (r *run) present(ctx context.Context, metric, label string) Presence {
	m := Presence{Metric: metric, Attribute: label, Values: []string{}}
	samples, ev, err := r.prom(ctx, fmt.Sprintf("sum by (%s) (%s%s)", label, metric, r.job()))
	m.add(ev)
	if err != nil {
		m.fail(err)
		return m
	}
	m.Series = len(samples)
	for _, s := range samples {
		if v := s.Labels[label]; v != "" {
			m.Values = append(m.Values, v)
		}
	}
	sort.Strings(m.Values)
	return m
}

// EventLoop is which of Node's event loop delay gauges are reported.
type EventLoop struct {
	Read
	Missing []string `json:"missing"`
}

func (r *run) eventLoop(ctx context.Context) EventLoop {
	m := EventLoop{Missing: []string{}}
	for _, metric := range []string{"nodejs_eventloop_delay_p99_seconds", "nodejs_eventloop_delay_max_seconds"} {
		samples, ev, err := r.prom(ctx, "max("+metric+r.job()+")")
		m.add(ev)
		if err != nil {
			m.fail(err)
			return m
		}
		if len(samples) == 0 {
			m.Missing = append(m.Missing, metric)
		}
	}
	return m
}

// Logs is a sample of the newest log lines in the window: how many there
// are, and how many are not JSON or lack each standard field. Lines the
// kubelet wrote into the stream are counted apart and judged as nothing.
type Logs struct {
	Read
	Lines        int    `json:"lines"`
	NotJSON      int    `json:"not_json"`
	NoTimestamp  int    `json:"no_timestamp"`
	NoLevel      int    `json:"no_level"`
	NoMessage    int    `json:"no_message"`
	Kubelet      int    `json:"kubelet"`
	FirstNotJSON string `json:"first_not_json,omitempty"`
}

// kubeletLines are what the kubelet writes into a container's log stream
// when it cannot read it; they carry no information about the app.
var kubeletLines = []string{
	"unable to retrieve container logs for ",
	"failed to create fsnotify watcher: ",
	"failed to try resolving symlinks in path ",
}

func fromKubelet(text string) bool {
	for _, p := range kubeletLines {
		if strings.HasPrefix(text, p) {
			return true
		}
	}
	return false
}

func (r *run) logs(ctx context.Context) Logs {
	var m Logs
	q := r.stream()
	lines, err := r.src.Loki.Lines(ctx, q, r.opt.Window, r.opt.Sample)
	ev := Evidence{Source: "loki", Query: fmt.Sprintf("%s, newest %d lines in %s", q, r.opt.Sample, r.window())}
	if err != nil {
		ev.Got = "error: " + err.Error()
		m.add(ev)
		m.fail(err)
		return m
	}
	for _, l := range lines {
		if fromKubelet(l.Text) {
			m.Kubelet++
			continue
		}
		m.Lines++
		var obj map[string]json.RawMessage
		if err := json.Unmarshal([]byte(l.Text), &obj); err != nil || obj == nil {
			m.NotJSON++
			if m.FirstNotJSON == "" {
				m.FirstNotJSON = truncate(l.Text, 160)
			}
			continue
		}
		if _, ok := obj["timestamp"]; !ok {
			m.NoTimestamp++
		}
		if _, ok := obj["level"]; !ok {
			m.NoLevel++
		}
		if _, ok := obj["msg"]; !ok {
			if _, ok := obj["message"]; !ok {
				m.NoMessage++
			}
		}
	}
	ev.Got = fmt.Sprintf("%d lines, %d not JSON, %d without timestamp, %d without level, %d without msg or message", m.Lines, m.NotJSON, m.NoTimestamp, m.NoLevel, m.NoMessage)
	if m.Kubelet > 0 {
		ev.Got += fmt.Sprintf("; %d kubelet lines ignored", m.Kubelet)
	}
	if m.FirstNotJSON != "" {
		ev.Got += "; first non-JSON line: " + m.FirstNotJSON
	}
	m.add(ev)
	return m
}

// TraceIDs counts the window's log lines, and the ones that carry trace_id.
type TraceIDs struct {
	Read
	Lines  int `json:"lines"`
	Traced int `json:"traced"`
}

func (r *run) traceIDs(ctx context.Context) TraceIDs {
	var m TraceIDs
	var ev Evidence
	var err error
	m.Lines, ev, err = r.count(ctx, fmt.Sprintf("sum(count_over_time(%s[%s]))", r.stream(), r.window()), "lines")
	m.add(ev)
	if err != nil {
		m.fail(err)
		return m
	}
	m.Traced, ev, err = r.count(ctx, fmt.Sprintf(`sum(count_over_time(%s |= "trace_id" [%s]))`, r.stream(), r.window()), "lines")
	m.add(ev)
	if err != nil {
		m.fail(err)
	}
	return m
}

// Width is how many values one label takes on one metric.
type Width struct {
	Metric string `json:"metric"`
	Label  string `json:"label"`
	Values int    `json:"values"`
}

// Cardinality is the service's series over the window, and its widest
// labels, widest first. Widest leaves out the metrics the declaration names
// as catalog gauges; their labels are in CatalogGauges, widest first, so
// what was left out of the count is on the record.
type Cardinality struct {
	Read
	Series        int     `json:"series"`
	Metrics       int     `json:"metrics"`
	Widest        []Width `json:"widest"`
	CatalogGauges []Width `json:"catalog_gauges"`
}

// widest is how many of the widest labels a measurement keeps.
const widest = 20

func (r *run) cardinality(ctx context.Context) Cardinality {
	m := Cardinality{Widest: []Width{}, CatalogGauges: []Width{}}
	match := r.job()
	series, err := r.src.Prometheus.Series(ctx, match, r.opt.Window)
	ev := Evidence{Source: "prometheus", Query: fmt.Sprintf("series match[]=%s over %s", match, r.window())}
	if err != nil {
		ev.Got = "error: " + err.Error()
		m.add(ev)
		m.fail(err)
		return m
	}
	gauge := map[string]bool{}
	for _, g := range r.d.CatalogGauges {
		gauge[g] = true
	}
	// metric -> label -> distinct values
	values := map[string]map[string]map[string]bool{}
	for _, s := range series {
		name := s["__name__"]
		for k, v := range s {
			if k == "__name__" || k == "le" {
				continue
			}
			if values[name] == nil {
				values[name] = map[string]map[string]bool{}
			}
			if values[name][k] == nil {
				values[name][k] = map[string]bool{}
			}
			values[name][k][v] = true
		}
	}
	m.Series, m.Metrics = len(series), len(values)
	for metric, labels := range values {
		for l, vs := range labels {
			w := Width{Metric: metric, Label: l, Values: len(vs)}
			if gauge[metric] {
				m.CatalogGauges = append(m.CatalogGauges, w)
			} else {
				m.Widest = append(m.Widest, w)
			}
		}
	}
	byWidth(m.Widest)
	byWidth(m.CatalogGauges)
	if len(m.Widest) > widest {
		m.Widest = m.Widest[:widest]
	}
	if len(m.CatalogGauges) > widest {
		m.CatalogGauges = m.CatalogGauges[:widest]
	}
	ev.Got = "no series"
	if len(series) > 0 {
		ev.Got = fmt.Sprintf("%d series across %d metrics", len(series), len(values))
		if len(m.Widest) > 0 {
			w := m.Widest[0]
			ev.Got += fmt.Sprintf("; widest label %s on %s with %d values", w.Label, w.Metric, w.Values)
		}
		if len(m.CatalogGauges) > 0 {
			ev.Got += "; not counted, as catalog gauges: " + strings.Join(widestPerMetric(m.CatalogGauges), ", ")
		}
	}
	m.add(ev)
	return m
}

// byWidth sorts widths widest first, then by metric and label.
func byWidth(ws []Width) {
	sort.Slice(ws, func(i, j int) bool {
		a, b := ws[i], ws[j]
		if a.Values != b.Values {
			return a.Values > b.Values
		}
		return a.Metric+a.Label < b.Metric+b.Label
	})
}

// widestPerMetric names each metric's widest label, from widths sorted
// widest first.
func widestPerMetric(ws []Width) []string {
	seen := map[string]bool{}
	var out []string
	for _, w := range ws {
		if seen[w.Metric] {
			continue
		}
		seen[w.Metric] = true
		out = append(out, fmt.Sprintf("%s{%s} with %d values", w.Metric, w.Label, w.Values))
	}
	return out
}

// EdgeRouter is the routers the edge's counter has for the host.
type EdgeRouter struct {
	Read
	// Configured is false when the estate names no router counter.
	Configured bool `json:"configured"`
	Routers    int  `json:"routers"`
}

func (r *run) edgeRouter(ctx context.Context) EdgeRouter {
	var m EdgeRouter
	if r.opt.Labels.Router == "" {
		return m
	}
	m.Configured = true
	samples, ev, err := r.prom(ctx, fmt.Sprintf(`sum by (router) (%s)`, r.fill(r.opt.Labels.Router)))
	m.add(ev)
	if err != nil {
		m.fail(err)
		return m
	}
	m.Routers = len(samples)
	return m
}

// pagePaths are what a browser asks for that is not a page: the APIs, the
// scripts, the probes, and the downloads.
const pagePaths = `/(api|v1|static|assets|healthz|readyz|metrics|mcp|cli|install[.](sh|ps1)|telemetry[.]js)([/?].*)?`

// PageLoads counts the pages browsers loaded through the edge in the
// window. A page nobody opened sends no telemetry, so its absence proves
// nothing. A browser is told apart by the edge's browser agent filter:
// health checks and pushers name themselves, so their requests are not
// loads.
type PageLoads struct {
	Read
	// Configured is false when the estate names no edge access log or
	// browser agent filter.
	Configured bool `json:"configured"`
	Loads      int  `json:"loads"`
}

func (r *run) pageLoads(ctx context.Context) PageLoads {
	var m PageLoads
	edge, err := r.edge()
	l := r.opt.Labels
	if err != nil || l.BrowserAgent == "" {
		return m
	}
	m.Configured = true
	q := fmt.Sprintf(`sum(count_over_time(%s |= %q | json %s, %s, %s | %s="GET" | %s="200" | %s!~%q [%s]))`, edge, l.BrowserAgent, l.EdgeMethod, l.EdgeStatus, l.EdgePath, l.EdgeMethod, l.EdgeStatus, l.EdgePath, pagePaths, r.window())
	n, ev, err := r.count(ctx, q, "page loads")
	m.add(ev)
	if err != nil {
		m.fail(err)
		return m
	}
	m.Loads = n
	return m
}

// EdgeViews counts the view reports a public page posted to the edge.
type EdgeViews struct {
	Read
	// Configured is false when the estate names no edge access log or view
	// beacon.
	Configured bool   `json:"configured"`
	Beacon     string `json:"beacon"`
	Views      int    `json:"views"`
}

func (r *run) edgeViews(ctx context.Context) EdgeViews {
	m := EdgeViews{Beacon: r.opt.Labels.ViewBeacon}
	edge, err := r.edge()
	if err != nil || m.Beacon == "" {
		return m
	}
	m.Configured = true
	n, ev, err := r.count(ctx, fmt.Sprintf(`sum(count_over_time(%s |= %q [%s]))`, edge, m.Beacon, r.window()), "views")
	m.add(ev)
	if err != nil {
		m.fail(err)
		return m
	}
	m.Views = n
	return m
}

// BrowserLogs counts the browser app's log lines in the window.
type BrowserLogs struct {
	Read
	App   string `json:"app"`
	Lines int    `json:"lines"`
}

func (r *run) browserLogs(ctx context.Context) BrowserLogs {
	m := BrowserLogs{App: r.d.Web()}
	n, ev, err := r.count(ctx, fmt.Sprintf(`sum(count_over_time(%s[%s]))`, r.fill(r.opt.Labels.Browser), r.window()), "lines")
	m.add(ev)
	if err != nil {
		m.fail(err)
		return m
	}
	m.Lines = n
	return m
}

// BrowserTrace is the traces in the window that span the browser app and
// the server, and the traces the browser app sent at all.
type BrowserTrace struct {
	Read
	Joined []string `json:"joined"`
	Web    int      `json:"web"`
}

func (r *run) browserTrace(ctx context.Context) BrowserTrace {
	m := BrowserTrace{Joined: []string{}}
	joined := fmt.Sprintf(`{resource.service.name=%q} && {resource.service.name=%q}`, r.d.Web(), r.d.Service)
	traces, err := r.src.Tempo.Search(ctx, joined, r.opt.Window, 5)
	ev := Evidence{Source: "tempo", Query: joined}
	if err != nil {
		ev.Got = "error: " + err.Error()
		m.add(ev)
		m.fail(err)
		return m
	}
	for _, t := range traces {
		m.Joined = append(m.Joined, t.ID)
	}
	ev.Got = fmt.Sprintf("%d traces", len(traces))
	if len(traces) > 0 {
		ev.Got += ": " + strings.Join(m.Joined, ", ")
	}
	m.add(ev)
	only := fmt.Sprintf(`{resource.service.name=%q}`, r.d.Web())
	web, err := r.src.Tempo.Search(ctx, only, r.opt.Window, 5)
	ev = Evidence{Source: "tempo", Query: only, Got: fmt.Sprintf("%d traces", len(web))}
	if err != nil {
		ev.Got = "error: " + err.Error()
		m.add(ev)
		m.fail(err)
		return m
	}
	m.add(ev)
	m.Web = len(web)
	return m
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
