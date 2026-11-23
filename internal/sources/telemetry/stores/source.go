// Package stores talks to the three stores trellis reads: Prometheus, Loki,
// and Tempo, over their HTTP APIs. Each client is small and returns the
// shapes the clauses need, so a test can stand one up from fixture files.
package stores

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Sample is one series from a Prometheus instant query with its labels.
type Sample struct {
	Labels map[string]string
	Value  string
}

// Line is one log line from Loki with its stream labels.
type Line struct {
	Labels map[string]string
	Time   time.Time
	Text   string
}

// Trace is one trace Tempo's search found.
type Trace struct {
	ID       string                             `json:"traceID"`
	RootName string                             `json:"rootTraceName"`
	Root     string                             `json:"rootServiceName"`
	Services map[string]int                     `json:"-"`
	Stats    map[string]struct{ SpanCount int } `json:"serviceStats"`
}

// Prometheus is what the clauses ask of Prometheus.
type Prometheus interface {
	// Query runs an instant query at now.
	Query(ctx context.Context, expr string) ([]Sample, error)
	// Series lists the label sets matching a selector over the window.
	Series(ctx context.Context, match string, window time.Duration) ([]map[string]string, error)
}

// Loki is what the clauses ask of Loki.
type Loki interface {
	// Lines returns the newest lines matching a stream selector in the window.
	Lines(ctx context.Context, logql string, window time.Duration, limit int) ([]Line, error)
	// Count runs an instant metric query, such as sum(count_over_time(...[1h])).
	Count(ctx context.Context, logql string) (float64, error)
}

// Tempo is what the clauses ask of Tempo.
type Tempo interface {
	// Search runs a TraceQL query over the window.
	Search(ctx context.Context, traceql string, window time.Duration, limit int) ([]Trace, error)
}

// Client holds the base URL and HTTP client one store is read with.
type Client struct {
	Base string
	HTTP *http.Client
}

// New makes a client for a store at base, such as http://localhost:9090.
func New(base string) *Client {
	return &Client{Base: strings.TrimRight(base, "/"), HTTP: &http.Client{Timeout: 30 * time.Second}}
}

func (c *Client) get(ctx context.Context, path string, params url.Values, out any) error {
	u := c.Base + path
	if len(params) > 0 {
		u += "?" + params.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("%s: %s: %s", c.Base, resp.Status, strings.TrimSpace(firstLine(string(body))))
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("%s%s: not JSON: %w", c.Base, path, err)
	}
	return nil
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	if len(s) > 300 {
		return s[:300]
	}
	return s
}
