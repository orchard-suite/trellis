package probe

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/orchard-suite/trellis/internal/sources/telemetry/declaration"
	"github.com/orchard-suite/trellis/internal/sources/telemetry/stores"
)

// fixtures maps the expression a store is asked to the file that answers it.
// A query no fixture answers fails the test, so every query a clause runs is
// written down here.
type fixtures map[string]string

const (
	job    = `{job="myapp"}`
	stream = `{namespace="myapps", app="myapp", container="myapp"}`
)

func good() fixtures {
	return fixtures{
		"prometheus|max by (version) (myapp_build_info" + job + ")":                                                    "build_info_ok",
		"prometheus|count by (http_route) (max_over_time(http_server_request_duration_seconds_count" + job + "[24h]))": "routes_ok",
		"prometheus|sum by (process_cpu_state) (process_cpu_time_seconds_total" + job + ")":                            "cpu_ok",
		"prometheus|sum by (process_memory_kind) (process_memory_usage_bytes" + job + ")":                              "memory_ok",
		"prometheus|max(nodejs_eventloop_delay_p99_seconds" + job + ")":                                                "scalar_ok",
		"prometheus|max(nodejs_eventloop_delay_max_seconds" + job + ")":                                                "scalar_ok",
		"prometheus|series " + job: "series_ok",
		`prometheus|sum by (router) (traefik_router_requests_total{router=~"myapps-.*myapp-example-test.*"})`: "routers_ok",
		"loki|lines " + stream:                                          "lines_ok",
		"loki|series " + stream:                                         "streams_one",
		"loki|sum(count_over_time(" + stream + "[24h]))":                "count_100",
		"loki|sum(count_over_time(" + stream + ` |= "trace_id" [24h]))`: "count_60",
		`loki|sum(count_over_time({app="demarc", source="traefik-access", site="myapp.example.test"} |= "/api/view" [24h]))`: "count_60",
		`loki|sum(count_over_time({app="myapp-web"}[24h]))`:                                                                  "count_100",
		pageLoads:     "count_60",
		healthzAtEdge: "count_60",
		`tempo|{resource.service.name="myapp-web"} && {resource.service.name="myapp"}`: "traces_joined",
		`tempo|{resource.service.name="myapp-web"}`:                                    "traces_none",
	}
}

// healthzAtEdge is the count of requests the edge passed to /healthz.
const healthzAtEdge = `loki|sum(count_over_time({app="demarc", source="traefik-access", site="myapp.example.test"} | json RequestPath | RequestPath=~"^/healthz(\\?.*)?$" [24h]))`

// pageLoads is the count of pages browsers loaded through the edge.
const pageLoads = `loki|sum(count_over_time({app="demarc", source="traefik-access", site="myapp.example.test"} |= "\"request_User-Agent\":\"Mozilla/" | json RequestMethod, DownstreamStatus, RequestPath | RequestMethod="GET" | DownstreamStatus="200" | RequestPath!~"/(api|v1|static|assets|healthz|readyz|metrics|mcp|cli|install[.](sh|ps1)|telemetry[.]js)([/?].*)?" [24h]))`

func serve(t *testing.T, store string, fx fixtures) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		var key string
		switch {
		case r.URL.Path == "/api/v1/query", r.URL.Path == "/loki/api/v1/query":
			key = q.Get("query")
		case r.URL.Path == "/api/v1/series", r.URL.Path == "/loki/api/v1/series":
			key = "series " + q.Get("match[]")
		case r.URL.Path == "/loki/api/v1/query_range":
			key = "lines " + q.Get("query")
		case r.URL.Path == "/api/search":
			key = q.Get("q")
		default:
			t.Errorf("%s: unexpected path %s", store, r.URL.Path)
			http.NotFound(w, r)
			return
		}
		file, ok := fx[store+"|"+key]
		if !ok {
			t.Errorf("%s: no fixture for %q", store, key)
			http.Error(w, "no fixture", http.StatusInternalServerError)
			return
		}
		body, err := os.ReadFile(filepath.Join("testdata", store, file+".json"))
		if err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func sources(t *testing.T, fx fixtures) Sources {
	return Sources{
		Prometheus: stores.NewPrometheus(serve(t, "prometheus", fx)),
		Loki:       stores.NewLoki(serve(t, "loki", fx)),
		Tempo:      stores.NewTempo(serve(t, "tempo", fx)),
	}
}

// decl is the node app with a page on the internet; a non-empty yaml
// replaces its last four lines.
func decl(t *testing.T, yaml string) *declaration.Declaration {
	if yaml == "" {
		yaml = "page: true\nruntime: node\nedge: both\npublic: true\n"
	}
	d, err := declaration.Parse([]byte("service: myapp\nnamespace: myapps\nhost: myapp.example.test\nroutes: [/healthz, \"/api/items/{id}\"]\n" + yaml))
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func measure(t *testing.T, yaml string, fx fixtures, opt Options) Measures {
	t.Helper()
	if opt.Labels == (Labels{}) {
		opt.Labels = edge
	}
	return Run(context.Background(), decl(t, yaml), sources(t, fx), opt)
}

func TestAGoodServiceMeasuresClean(t *testing.T) {
	m := measure(t, "", good(), Options{Window: 24 * time.Hour, Sample: 100})
	for _, r := range m.reads() {
		if r.Failed() || len(r.Evidence) == 0 || r.Evidence[0].Query == "" || r.Evidence[0].Got == "" {
			t.Errorf("a measurement failed or has no evidence: %+v", r)
		}
	}
	if m.Unread() != "" {
		t.Errorf("unread: %s", m.Unread())
	}
	if m.BuildInfo.Series == 0 || m.BuildInfo.Unversioned != 0 || len(m.BuildInfo.Versions) == 0 {
		t.Errorf("build_info: %+v", m.BuildInfo)
	}
	if !m.HTTPDuration.Reported || len(m.HTTPDuration.Routes) == 0 || len(m.HTTPDuration.EdgeHits) != 0 {
		t.Errorf("http_duration: %+v", m.HTTPDuration)
	}
	if len(m.ProcessCPU.Values) == 0 || len(m.ProcessMemory.Values) == 0 {
		t.Errorf("process: %+v %+v", m.ProcessCPU, m.ProcessMemory)
	}
	if m.EventLoop == nil || len(m.EventLoop.Missing) != 0 {
		t.Errorf("event_loop: %+v", m.EventLoop)
	}
	if m.Logs.Lines == 0 || m.Logs.NotJSON != 0 || m.Logs.NoTimestamp+m.Logs.NoLevel+m.Logs.NoMessage != 0 {
		t.Errorf("logs: %+v", m.Logs)
	}
	if m.TraceIDs.Lines != 100 || m.TraceIDs.Traced != 60 {
		t.Errorf("trace_ids: %+v", m.TraceIDs)
	}
	if m.Cardinality.Series == 0 || m.Cardinality.Widest[0].Values > 50 {
		t.Errorf("cardinality: %+v", m.Cardinality)
	}
	if m.EdgeRouter == nil || !m.EdgeRouter.Configured || m.EdgeRouter.Routers == 0 {
		t.Errorf("edge_router: %+v", m.EdgeRouter)
	}
	if m.PageLoads == nil || m.PageLoads.Loads != 60 || m.EdgeViews == nil || m.EdgeViews.Views != 60 {
		t.Errorf("page_loads %+v edge_views %+v", m.PageLoads, m.EdgeViews)
	}
	if m.BrowserLogs == nil || m.BrowserLogs.Lines != 100 || m.BrowserTrace == nil || len(m.BrowserTrace.Joined) == 0 {
		t.Errorf("browser: %+v %+v", m.BrowserLogs, m.BrowserTrace)
	}
}

func TestKubeletLinesIgnored(t *testing.T) {
	m := measure(t, "", good(), Options{})
	if m.Logs.Kubelet != 2 || !strings.Contains(m.Logs.Evidence[0].Got, "2 kubelet lines ignored") {
		t.Errorf("logs: %+v", m.Logs)
	}
}

func TestTheDeclarationRulesMeasurementsOut(t *testing.T) {
	m := measure(t, "page: false\npublic: false\nruntime: go\nedge: cloudflare\n", good(), Options{})
	if m.EventLoop != nil || m.EdgeRouter != nil || m.PageLoads != nil || m.EdgeViews != nil || m.BrowserLogs != nil || m.BrowserTrace != nil {
		t.Errorf("measured what the declaration rules out: %+v", m)
	}
}

func TestEachMeasurementSeesTrouble(t *testing.T) {
	routes := "prometheus|count by (http_route) (max_over_time(http_server_request_duration_seconds_count" + job + "[24h]))"
	cases := []struct {
		name, key, file string
		saw             func(Measures) bool
	}{
		{"build_info absent", "prometheus|max by (version) (myapp_build_info" + job + ")", "empty", func(m Measures) bool { return m.BuildInfo.Series == 0 }},
		{"build_info unversioned", "prometheus|max by (version) (myapp_build_info" + job + ")", "build_info_noversion", func(m Measures) bool { return m.BuildInfo.Unversioned == 1 }},
		{"no durations", routes, "empty", func(m Measures) bool { return !m.HTTPDuration.Reported }},
		{"durations unlabelled", routes, "routes_unlabelled", func(m Measures) bool {
			return m.HTTPDuration.Unlabelled && len(m.HTTPDuration.Routes) == 0
		}},
		{"declared route the edge served", routes, "routes_missing", func(m Measures) bool { return m.HTTPDuration.EdgeHits["/healthz"] == 60 }},
		{"cpu absent", "prometheus|sum by (process_cpu_state) (process_cpu_time_seconds_total" + job + ")", "empty", func(m Measures) bool { return m.ProcessCPU.Series == 0 }},
		{"cpu unlabelled", "prometheus|sum by (process_cpu_state) (process_cpu_time_seconds_total" + job + ")", "cpu_unlabelled", func(m Measures) bool {
			return m.ProcessCPU.Series > 0 && len(m.ProcessCPU.Values) == 0
		}},
		{"memory absent", "prometheus|sum by (process_memory_kind) (process_memory_usage_bytes" + job + ")", "empty", func(m Measures) bool { return m.ProcessMemory.Series == 0 }},
		{"event loop max absent", "prometheus|max(nodejs_eventloop_delay_max_seconds" + job + ")", "empty", func(m Measures) bool {
			return len(m.EventLoop.Missing) == 1 && m.EventLoop.Missing[0] == "nodejs_eventloop_delay_max_seconds"
		}},
		{"plain lines", "loki|lines " + stream, "lines_plain", func(m Measures) bool { return m.Logs.NotJSON == 1 && m.Logs.Lines == 2 && m.Logs.FirstNotJSON != "" }},
		{"no lines", "loki|lines " + stream, "lines_none", func(m Measures) bool { return m.Logs.Lines == 0 }},
		{"missing fields", "loki|lines " + stream, "lines_nofields", func(m Measures) bool { return m.Logs.NoTimestamp == 1 && m.Logs.NoMessage == 2 }},
		{"no trace ids", "loki|sum(count_over_time(" + stream + ` |= "trace_id" [24h]))`, "count_0", func(m Measures) bool { return m.TraceIDs.Traced == 0 }},
		{"a wide label", "prometheus|series " + job, "series_high", func(m Measures) bool {
			w := m.Cardinality.Widest[0]
			return w.Metric == "http_server_request_duration_seconds_count" && w.Label == "http_route" && w.Values == 60
		}},
		{"no series", "prometheus|series " + job, "series_empty", func(m Measures) bool { return m.Cardinality.Series == 0 && len(m.Cardinality.Widest) == 0 }},
		{"no router", `prometheus|sum by (router) (traefik_router_requests_total{router=~"myapps-.*myapp-example-test.*"})`, "empty", func(m Measures) bool { return m.EdgeRouter.Routers == 0 }},
		{"no views", `loki|sum(count_over_time({app="demarc", source="traefik-access", site="myapp.example.test"} |= "/api/view" [24h]))`, "count_0", func(m Measures) bool { return m.EdgeViews.Views == 0 }},
		{"no browser lines", `loki|sum(count_over_time({app="myapp-web"}[24h]))`, "count_0", func(m Measures) bool { return m.BrowserLogs.Lines == 0 }},
		{"browser only traces", `tempo|{resource.service.name="myapp-web"} && {resource.service.name="myapp"}`, "traces_none", func(m Measures) bool {
			return len(m.BrowserTrace.Joined) == 0 && m.BrowserTrace.Web == 0
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fx := good()
			fx[c.key] = c.file
			m := measure(t, "", fx, Options{})
			if !c.saw(m) {
				t.Errorf("not measured: %+v", m)
			}
			if m.Unread() != "" {
				t.Errorf("a store answered, so the service is read: %s", m.Unread())
			}
		})
	}
}

func TestBrowserTraceWebOnly(t *testing.T) {
	fx := good()
	fx[`tempo|{resource.service.name="myapp-web"} && {resource.service.name="myapp"}`] = "traces_none"
	fx[`tempo|{resource.service.name="myapp-web"}`] = "traces_web_only"
	m := measure(t, "", fx, Options{})
	if len(m.BrowserTrace.Joined) != 0 || m.BrowserTrace.Web == 0 || len(m.BrowserTrace.Evidence) != 2 {
		t.Errorf("browser_trace: %+v", m.BrowserTrace)
	}
}

func TestPageLoadsAreCounted(t *testing.T) {
	fx := good()
	fx[pageLoads] = "count_0"
	m := measure(t, "", fx, Options{})
	if !m.PageLoads.Configured || m.PageLoads.Loads != 0 || m.PageLoads.Failed() {
		t.Errorf("page_loads: %+v", m.PageLoads)
	}
}

func TestAQuietRouteHasNoEdgeHits(t *testing.T) {
	fx := good()
	fx["prometheus|count by (http_route) (max_over_time(http_server_request_duration_seconds_count"+job+"[24h]))"] = "routes_missing"
	fx[healthzAtEdge] = "count_0"
	m := measure(t, "", fx, Options{})
	if hits, asked := m.HTTPDuration.EdgeHits["/healthz"]; !asked || hits != 0 {
		t.Errorf("edge hits: %+v", m.HTTPDuration.EdgeHits)
	}
	if len(m.HTTPDuration.Evidence) != 2 {
		t.Errorf("the edge's count is evidence too: %+v", m.HTTPDuration.Evidence)
	}
}

func TestRouteRegexp(t *testing.T) {
	cases := map[string][]string{
		"/healthz":               {"/healthz", "/healthz?x=1"},
		"/api/items/{id}":        {"/api/items/7", "/api/items/7?full=1"},
		"/{id}/{name...}":        {"/abc/a/b/c.txt"},
		"/{$}":                   {"/", "/?q=1"},
		"/v1/drops/{id}/content": {"/v1/drops/x1/content"},
	}
	misses := map[string][]string{
		"/healthz":        {"/healthzz", "/x/healthz"},
		"/api/items/{id}": {"/api/items/", "/api/items/7/8"},
		"/{$}":            {"/drops"},
	}
	for route, paths := range cases {
		re := regexp.MustCompile(routeRegexp(route))
		for _, p := range paths {
			if !re.MatchString(p) {
				t.Errorf("%s should match %s (%s)", route, p, re)
			}
		}
		for _, p := range misses[route] {
			if re.MatchString(p) {
				t.Errorf("%s should not match %s (%s)", route, p, re)
			}
		}
	}
}

func TestStoreDown(t *testing.T) {
	src := sources(t, good())
	src.Prometheus = stores.NewPrometheus("http://127.0.0.1:1")
	m := Run(context.Background(), decl(t, ""), src, Options{Labels: edge})
	if !strings.Contains(m.BuildInfo.Error, "could not read the store") || m.BuildInfo.Series != 0 {
		t.Errorf("build_info: %+v", m.BuildInfo)
	}
	if m.Logs.Failed() || m.Unread() != "" {
		t.Errorf("the other stores still answer: %+v %q", m.Logs, m.Unread())
	}
}

func TestEveryStoreDownIsUnread(t *testing.T) {
	down := Sources{Prometheus: stores.NewPrometheus("http://127.0.0.1:1"), Loki: stores.NewLoki("http://127.0.0.1:1"), Tempo: stores.NewTempo("http://127.0.0.1:1")}
	m := Run(context.Background(), decl(t, ""), down, Options{Labels: edge})
	if !strings.Contains(m.Unread(), "could not read the store") {
		t.Errorf("unread: %q", m.Unread())
	}
}

// edge is an estate whose edge is Traefik writing JSON access lines to Loki.
var edge = Labels{
	EdgeAccess:   `{app="demarc", source="traefik-access", site="{host}"}`,
	EdgePath:     "RequestPath",
	EdgeMethod:   "RequestMethod",
	EdgeStatus:   "DownstreamStatus",
	BrowserAgent: `"request_User-Agent":"Mozilla/`,
	Router:       `traefik_router_requests_total{router=~"{namespace}-.*{host_dashed}.*"}`,
	ViewBeacon:   "/api/view",
}

func TestTheEdgeUnsetSaysSo(t *testing.T) {
	m := Run(context.Background(), decl(t, ""), sources(t, good()), Options{})
	if m.EdgeRouter.Configured || m.PageLoads.Configured || m.EdgeViews.Configured {
		t.Errorf("an edge nobody configured: %+v %+v %+v", m.EdgeRouter, m.PageLoads, m.EdgeViews)
	}
}

func TestTheAppLabelSelectsTheLogStream(t *testing.T) {
	own := `{namespace="myapps", app="house", container="house"}`
	fx := good()
	for k, v := range fx {
		if strings.Contains(k, stream) {
			delete(fx, k)
			fx[strings.ReplaceAll(k, stream, own)] = v
		}
	}
	m := measure(t, "app: house\npage: true\nruntime: node\nedge: both\npublic: true\n", fx, Options{})
	if m.Logs.Lines == 0 || !strings.HasPrefix(m.Logs.Evidence[0].Query, own) {
		t.Errorf("logs: %+v", m.Logs)
	}
	if m.TraceIDs.Lines != 100 || m.TraceIDs.Traced != 60 || !strings.Contains(m.TraceIDs.Evidence[1].Query, own) {
		t.Errorf("trace_ids: %+v", m.TraceIDs)
	}
	if m.BuildInfo.Series == 0 || m.BrowserLogs.Lines != 100 {
		t.Errorf("metrics and the browser stay the service's: %+v %+v", m.BuildInfo, m.BrowserLogs)
	}
}

func TestCatalogGaugesAreLeftOutOfTheCount(t *testing.T) {
	fx := good()
	fx["prometheus|series "+job] = "series_gauged"
	m := measure(t, "catalog_gauges: [myapp_field_value]\npage: true\nruntime: node\nedge: both\npublic: true\n", fx, Options{})
	c := m.Cardinality
	if c.Series != 62 || c.Metrics != 3 {
		t.Errorf("every series is counted: %+v", c)
	}
	for _, w := range c.Widest {
		if w.Metric == "myapp_field_value" {
			t.Errorf("a catalog gauge is among the widest: %+v", c.Widest)
		}
	}
	if len(c.CatalogGauges) == 0 || c.CatalogGauges[0] != (Width{Metric: "myapp_field_value", Label: "key", Values: 60}) {
		t.Errorf("catalog gauges: %+v", c.CatalogGauges)
	}
	if got := c.Evidence[0].Got; !strings.Contains(got, "not counted, as catalog gauges: myapp_field_value{key} with 60 values") {
		t.Errorf("evidence: %s", got)
	}
	// Undeclared, the same gauge is the widest label.
	m = measure(t, "", fx, Options{})
	if w := m.Cardinality.Widest[0]; w.Metric != "myapp_field_value" || w.Values != 60 || len(m.Cardinality.CatalogGauges) != 0 {
		t.Errorf("undeclared: %+v", m.Cardinality)
	}
}

func TestAQuietWindowLooksForTheStream(t *testing.T) {
	count := "loki|sum(count_over_time(" + stream + "[24h]))"
	for _, c := range []struct {
		name, streams string
		want          int
	}{{"quiet", "streams_one", 1}, {"never", "streams_none", 0}} {
		t.Run(c.name, func(t *testing.T) {
			fx := good()
			fx["loki|lines "+stream] = "lines_none"
			fx[count] = "count_0"
			fx["loki|sum(count_over_time("+stream+` |= "trace_id" [24h]))`] = "count_0"
			fx["loki|series "+stream] = c.streams
			m := measure(t, "", fx, Options{})
			for _, seen := range []*Seen{m.Logs.Seen, m.TraceIDs.Seen} {
				if seen == nil || seen.Streams != c.want || seen.Lookback != "30d" {
					t.Errorf("seen: %+v", seen)
				}
			}
			ev := m.Logs.Evidence[len(m.Logs.Evidence)-1]
			if ev.Query != "series match[]="+stream+" over 30d" {
				t.Errorf("evidence: %+v", ev)
			}
		})
	}
	// A window with lines does not look.
	m := measure(t, "", good(), Options{})
	if m.Logs.Seen != nil || m.TraceIDs.Seen != nil {
		t.Errorf("a window with lines looked for its stream: %+v %+v", m.Logs.Seen, m.TraceIDs.Seen)
	}
}

func TestTheStreamLookupFailingIsUnread(t *testing.T) {
	fx := good()
	fx["loki|lines "+stream] = "lines_none"
	delete(fx, "loki|series "+stream)
	src := sources(t, fx)
	src.Loki = streamsDown{src.Loki}
	m := Run(context.Background(), decl(t, ""), src, Options{Labels: edge})
	if !strings.Contains(m.Logs.Error, "could not read the store") || m.Logs.Seen != nil {
		t.Errorf("logs: %+v", m.Logs)
	}
}

type streamsDown struct{ stores.Loki }

func (streamsDown) Streams(context.Context, string, time.Duration) (int, error) {
	return 0, errors.New("refused")
}
