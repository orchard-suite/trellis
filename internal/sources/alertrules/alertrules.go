// Package alertrules reads every alerting rule a Prometheus has loaded, so
// the alerts pack can judge the rules themselves: whether each series a
// rule selects exists, and which labels the alerts it raises carry. It
// measures and never judges.
package alertrules

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/prometheus/prometheus/model/labels"
	"github.com/prometheus/prometheus/promql/parser"
	"gopkg.in/yaml.v3"

	"github.com/orchard-suite/trellis/internal/estate"
	"github.com/orchard-suite/trellis/internal/obs"
	"github.com/orchard-suite/trellis/internal/sources"
)

// Kind is the subject kind the source writes, one per alerting rule.
const Kind = "alert_rule"

// DefaultLookback is how far back a selector may have had a series and
// still count as matching data.
const DefaultLookback = 7 * 24 * time.Hour

// Provider reads one Prometheus.
type Provider struct {
	base     string
	lookback time.Duration
	http     *http.Client
	log      *slog.Logger
	now      func() time.Time
}

// New makes the provider for a Prometheus base URL.
func New(base string, lookback time.Duration, log *slog.Logger) *Provider {
	if lookback <= 0 {
		lookback = DefaultLookback
	}
	return &Provider{base: strings.TrimRight(base, "/"), lookback: lookback, http: obs.Client(60 * time.Second), log: log, now: time.Now}
}

func init() {
	sources.Register("alertrules", func(node *yaml.Node, env sources.Env) (sources.Source, error) {
		var c struct {
			Prometheus string `yaml:"prometheus"`
			Lookback   string `yaml:"lookback"`
		}
		if err := sources.Decode(node, &c); err != nil {
			return nil, err
		}
		if c.Prometheus == "" {
			return nil, errors.New("the alertrules source needs prometheus")
		}
		var lookback time.Duration
		if c.Lookback != "" {
			d, err := time.ParseDuration(c.Lookback)
			if err != nil || d <= 0 {
				return nil, fmt.Errorf("the alertrules source's lookback %q is not a duration", c.Lookback)
			}
			lookback = d
		}
		return New(c.Prometheus, lookback, env.Log), nil
	})
}

// Facts are what the source writes.
func (p *Provider) Facts() []string { return []string{"alert"} }

// Rule is one alerting rule as Prometheus's rules API lists it.
type Rule struct {
	Name     string            `json:"name"`
	Query    string            `json:"query"`
	Duration float64           `json:"duration"`
	Labels   map[string]string `json:"labels"`
	Type     string            `json:"type"`
	Group    string
	File     string
}

// Collect lists the loaded alerting rules and, for each series selector
// their expressions use, whether it had a series in the lookback and which
// labels its series carry.
func (p *Provider) Collect(ctx context.Context, e *estate.Writer) error {
	rules, err := p.rules(ctx)
	if err != nil {
		return err
	}
	seen := map[string]Selector{}
	look := func(sel string) Selector {
		if s, ok := seen[sel]; ok {
			return s
		}
		s := p.selector(ctx, sel)
		seen[sel] = s
		return s
	}
	Shape(e, rules, look)
	p.log.InfoContext(ctx, "alert rules read", "rules", len(rules), "selectors", len(seen))
	return nil
}

// Selector is what Prometheus knows of one series selector over the
// lookback: how many series matched, and the label names they carry. When
// none matched, it also says how many series the metric has with no
// matcher but its name, and how many each matcher alone selects, so a
// pack can tell a filter on a value that never happened from a selector
// that names the wrong place.
type Selector struct {
	Series       int
	Labels       []string
	MetricSeries int
	Matchers     []MatcherCount
	Err          string
}

// MatcherCount is how many series of the metric one matcher selects alone.
type MatcherCount struct {
	Label  string
	Series int
}

func (p *Provider) rules(ctx context.Context) ([]Rule, error) {
	var body struct {
		Status string `json:"status"`
		Error  string `json:"error"`
		Data   struct {
			Groups []struct {
				Name  string `json:"name"`
				File  string `json:"file"`
				Rules []Rule `json:"rules"`
			} `json:"groups"`
		} `json:"data"`
	}
	if err := p.get(ctx, "/api/v1/rules", url.Values{"type": {"alert"}}, &body); err != nil {
		return nil, err
	}
	if body.Status != "success" {
		return nil, fmt.Errorf("prometheus rules: %s", body.Error)
	}
	var out []Rule
	for _, g := range body.Data.Groups {
		for _, r := range g.Rules {
			if r.Type != "" && r.Type != "alerting" {
				continue
			}
			r.Group, r.File = g.Name, g.File
			out = append(out, r)
		}
	}
	return out, nil
}

func (p *Provider) selector(ctx context.Context, sel string) Selector {
	end := p.now()
	start := end.Add(-p.lookback)
	n, err := p.count(ctx, sel, end)
	if err != nil {
		return Selector{Err: err.Error()}
	}
	out := Selector{Series: n}
	if out.Series == 0 {
		p.narrow(ctx, sel, end, &out)
		return out
	}
	var names struct {
		Status string   `json:"status"`
		Error  string   `json:"error"`
		Data   []string `json:"data"`
	}
	if err := p.get(ctx, "/api/v1/labels", url.Values{"match[]": {sel}, "start": {unix(start)}, "end": {unix(end)}}, &names); err != nil {
		out.Err = err.Error()
		return out
	}
	if names.Status != "success" {
		out.Err = names.Error
		return out
	}
	for _, n := range names.Data {
		if n != "__name__" {
			out.Labels = append(out.Labels, n)
		}
	}
	sort.Strings(out.Labels)
	return out
}

// count is how many series a selector had in the lookback.
func (p *Provider) count(ctx context.Context, sel string, at time.Time) (int, error) {
	var res struct {
		Status string `json:"status"`
		Error  string `json:"error"`
		Data   struct {
			Result []struct {
				Value []any `json:"value"`
			} `json:"result"`
		} `json:"data"`
	}
	q := fmt.Sprintf("count(last_over_time(%s[%ds]))", sel, int(p.lookback.Seconds()))
	if err := p.get(ctx, "/api/v1/query", url.Values{"query": {q}, "time": {unix(at)}}, &res); err != nil {
		return 0, err
	}
	if res.Status != "success" {
		return 0, errors.New(res.Error)
	}
	if len(res.Data.Result) == 1 && len(res.Data.Result[0].Value) == 2 {
		v, _ := res.Data.Result[0].Value[1].(string)
		n, _ := strconv.Atoi(v)
		return n, nil
	}
	return 0, nil
}

// narrow fills in, for a selector that matched nothing, how many series
// its metric has and how many each of its matchers selects alone.
func (p *Provider) narrow(ctx context.Context, sel string, at time.Time, out *Selector) {
	matchers, err := parser.NewParser(parser.Options{}).ParseMetricSelector(sel)
	if err != nil {
		out.Err = err.Error()
		return
	}
	name := ""
	for _, m := range matchers {
		if m.Name == labels.MetricName && m.Type == labels.MatchEqual {
			name = m.Value
		}
	}
	if name == "" {
		return
	}
	if out.MetricSeries, err = p.count(ctx, name, at); err != nil {
		out.Err = err.Error()
		return
	}
	for _, m := range matchers {
		if m.Name == labels.MetricName {
			continue
		}
		n := 0
		if out.MetricSeries > 0 {
			if n, err = p.count(ctx, fmt.Sprintf("%s{%s}", name, m.String()), at); err != nil {
				out.Err = err.Error()
				return
			}
		}
		out.Matchers = append(out.Matchers, MatcherCount{Label: m.Name, Series: n})
	}
}

func (p *Provider) get(ctx context.Context, path string, q url.Values, into any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.base+path+"?"+q.Encode(), nil)
	if err != nil {
		return err
	}
	res, err := p.http.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, 32<<20))
	if err != nil {
		return err
	}
	if res.StatusCode != http.StatusOK && res.StatusCode != http.StatusBadRequest && res.StatusCode != http.StatusUnprocessableEntity {
		return fmt.Errorf("prometheus %s: %d %s", path, res.StatusCode, strings.TrimSpace(string(body[:min(len(body), 200)])))
	}
	return json.Unmarshal(body, into)
}

func unix(t time.Time) string { return strconv.FormatInt(t.Unix(), 10) }

// Shape adds one subject per rule. Its alert fact holds the rule as
// written (name, group, file, query, its wait in seconds, its own labels),
// each series selector with how many series it had in the lookback, and
// the labels the alerts it raises carry: keeps on every alert, may_keep
// on some. A query that does not parse keeps its parse_error and no
// selectors or label sets.
func Shape(e *estate.Writer, rules []Rule, look func(string) Selector) {
	keys := map[string]int{}
	for _, r := range rules {
		key := r.Group + "/" + r.Name
		keys[key]++
		if n := keys[key]; n > 1 {
			key = fmt.Sprintf("%s#%d", key, n)
		}
		labels := map[string]any{}
		for k, v := range r.Labels {
			labels[k] = v
		}
		fact := map[string]any{
			"name": r.Name, "group": r.Group, "file": r.File, "query": r.Query,
			"for_seconds": int(r.Duration), "labels": labels,
		}
		a, err := Analyze(r.Query, look)
		if err != nil {
			fact["parse_error"] = err.Error()
		} else {
			sels := make([]any, 0, len(a.Selectors))
			for _, s := range a.Selectors {
				m := map[string]any{"selector": s.Text, "series": s.Series, "optional": s.Optional}
				if s.Err != "" {
					m["error"] = s.Err
				}
				if s.Series == 0 && s.Err == "" {
					m["metric_series"] = s.MetricSeries
					counts := make([]any, 0, len(s.Matchers))
					for _, c := range s.Matchers {
						counts = append(counts, map[string]any{"label": c.Label, "series": c.Series})
					}
					m["matchers"] = counts
				}
				sels = append(sels, m)
			}
			fact["selectors"] = sels
			fact["keeps"] = strs(a.Keeps)
			fact["may_keep"] = strs(a.MayKeep)
		}
		_ = e.Subject(Kind, key, r.Name).Set("alert", fact)
	}
}

func strs(in []string) []any {
	out := make([]any, len(in))
	for i, s := range in {
		out[i] = s
	}
	return out
}
