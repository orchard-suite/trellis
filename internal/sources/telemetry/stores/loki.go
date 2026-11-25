package stores

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"time"
)

type loki struct{ *Client }

// NewLoki reads a Loki at base.
func NewLoki(base string) Loki { return loki{New(base)} }

type lokiResponse struct {
	Status string `json:"status"`
	Data   struct {
		ResultType string          `json:"resultType"`
		Result     json.RawMessage `json:"result"`
	} `json:"data"`
}

type lokiStream struct {
	Stream map[string]string `json:"stream"`
	Values [][2]string       `json:"values"`
}

type lokiVector struct {
	Metric map[string]string `json:"metric"`
	Value  [2]any            `json:"value"`
}

func (l loki) Lines(ctx context.Context, logql string, window time.Duration, limit int) ([]Line, error) {
	now := time.Now()
	params := url.Values{
		"query":     {logql},
		"start":     {strconv.FormatInt(now.Add(-window).UnixNano(), 10)},
		"end":       {strconv.FormatInt(now.UnixNano(), 10)},
		"limit":     {strconv.Itoa(limit)},
		"direction": {"backward"},
	}
	var r lokiResponse
	if err := l.get(ctx, "/loki/api/v1/query_range", params, &r); err != nil {
		return nil, err
	}
	if r.Data.ResultType != "streams" {
		return nil, fmt.Errorf("loki: %q is not a log query", logql)
	}
	var streams []lokiStream
	if err := json.Unmarshal(r.Data.Result, &streams); err != nil {
		return nil, fmt.Errorf("loki: %w", err)
	}
	var out []Line
	for _, s := range streams {
		for _, v := range s.Values {
			ns, _ := strconv.ParseInt(v[0], 10, 64)
			out = append(out, Line{Labels: s.Stream, Time: time.Unix(0, ns), Text: v[1]})
		}
	}
	return out, nil
}

func (l loki) Count(ctx context.Context, logql string) (float64, error) {
	var r lokiResponse
	if err := l.get(ctx, "/loki/api/v1/query", url.Values{"query": {logql}}, &r); err != nil {
		return 0, err
	}
	if r.Data.ResultType != "vector" {
		return 0, fmt.Errorf("loki: %q is not a metric query", logql)
	}
	var vec []lokiVector
	if err := json.Unmarshal(r.Data.Result, &vec); err != nil {
		return 0, fmt.Errorf("loki: %w", err)
	}
	var total float64
	for _, v := range vec {
		s, _ := v.Value[1].(string)
		f, _ := strconv.ParseFloat(s, 64)
		total += f
	}
	return total, nil
}

func (l loki) Streams(ctx context.Context, selector string, window time.Duration) (int, error) {
	now := time.Now()
	params := url.Values{
		"match[]": {selector},
		"start":   {strconv.FormatInt(now.Add(-window).UnixNano(), 10)},
		"end":     {strconv.FormatInt(now.UnixNano(), 10)},
	}
	var r struct {
		Status string              `json:"status"`
		Data   []map[string]string `json:"data"`
	}
	if err := l.get(ctx, "/loki/api/v1/series", params, &r); err != nil {
		return 0, err
	}
	return len(r.Data), nil
}
