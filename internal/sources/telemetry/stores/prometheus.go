package stores

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"time"
)

type prometheus struct{ *Client }

// NewPrometheus reads a Prometheus at base.
func NewPrometheus(base string) Prometheus { return prometheus{New(base)} }

type promResponse struct {
	Status string `json:"status"`
	Error  string `json:"error"`
	Data   struct {
		ResultType string `json:"resultType"`
		Result     []struct {
			Metric map[string]string `json:"metric"`
			Value  []any             `json:"value"`
		} `json:"result"`
	} `json:"data"`
}

func (p prometheus) Query(ctx context.Context, expr string) ([]Sample, error) {
	var r promResponse
	if err := p.get(ctx, "/api/v1/query", url.Values{"query": {expr}}, &r); err != nil {
		return nil, err
	}
	if r.Status != "success" {
		return nil, fmt.Errorf("prometheus: %s", r.Error)
	}
	out := make([]Sample, 0, len(r.Data.Result))
	for _, s := range r.Data.Result {
		v := ""
		if len(s.Value) == 2 {
			v, _ = s.Value[1].(string)
		}
		out = append(out, Sample{Labels: s.Metric, Value: v})
	}
	return out, nil
}

type seriesResponse struct {
	Status string              `json:"status"`
	Error  string              `json:"error"`
	Data   []map[string]string `json:"data"`
}

func (p prometheus) Series(ctx context.Context, match string, window time.Duration) ([]map[string]string, error) {
	now := time.Now()
	params := url.Values{
		"match[]": {match},
		"start":   {strconv.FormatInt(now.Add(-window).Unix(), 10)},
		"end":     {strconv.FormatInt(now.Unix(), 10)},
	}
	var r seriesResponse
	if err := p.get(ctx, "/api/v1/series", params, &r); err != nil {
		return nil, err
	}
	if r.Status != "success" {
		return nil, fmt.Errorf("prometheus: %s", r.Error)
	}
	return r.Data, nil
}
