package stores

import (
	"context"
	"net/url"
	"strconv"
	"time"
)

type tempo struct{ *Client }

// NewTempo reads a Tempo at base.
func NewTempo(base string) Tempo { return tempo{New(base)} }

type tempoResponse struct {
	Traces []Trace `json:"traces"`
}

func (t tempo) Search(ctx context.Context, traceql string, window time.Duration, limit int) ([]Trace, error) {
	now := time.Now()
	params := url.Values{
		"q":     {traceql},
		"start": {strconv.FormatInt(now.Add(-window).Unix(), 10)},
		"end":   {strconv.FormatInt(now.Unix(), 10)},
		"limit": {strconv.Itoa(limit)},
	}
	var r tempoResponse
	if err := t.get(ctx, "/api/search", params, &r); err != nil {
		return nil, err
	}
	for i := range r.Traces {
		r.Traces[i].Services = map[string]int{}
		for svc, s := range r.Traces[i].Stats {
			r.Traces[i].Services[svc] = s.SpanCount
		}
	}
	return r.Traces, nil
}
