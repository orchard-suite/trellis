// Package webhook posts each pass to a URL: the estate's level, each
// pack's, pillar's, and owner's, every open finding with its owner and
// deadline, and what changed since the pass before, each change with its
// owner. It is how an estate sends the result on to a chat, a ticketing
// system, or a store of its own, and routes each change to who answers
// for it.
package webhook

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/orchard-suite/trellis/internal/board"
	"github.com/orchard-suite/trellis/internal/estate"
	"github.com/orchard-suite/trellis/internal/judge"
	"github.com/orchard-suite/trellis/internal/meter"
	"github.com/orchard-suite/trellis/internal/obs"
)

// Sink posts to one URL, with a bearer token when one is set.
type Sink struct {
	url, token, estate string
	http               *http.Client
	metrics            *meter.Metrics
	log                *slog.Logger
}

// New makes the sink.
func New(url, token, estateName string, metrics *meter.Metrics, log *slog.Logger) *Sink {
	return &Sink{url: url, token: token, estate: estateName, http: obs.Client(30 * time.Second), metrics: metrics, log: log}
}

// Body is what each post carries.
type Body struct {
	Estate      string                `json:"estate"`
	RefreshedAt time.Time             `json:"refreshed_at"`
	Level       estate.Level          `json:"level"`
	Tally       judge.Tally           `json:"tally"`
	Packs       []judge.PackVerdict   `json:"packs"`
	Pillars     []judge.PillarVerdict `json:"pillars"`
	Owners      []judge.OwnerVerdict  `json:"owners"`
	Findings    []judge.Finding       `json:"findings"`
	Events      []board.Event         `json:"events"`
}

// Push posts the snapshot. It never fails the pass; it says when the post
// did not land.
func (s *Sink) Push(ctx context.Context, snap *board.Snapshot) error {
	ev := snap.Evaluation
	open := ev.Open()
	if open == nil {
		open = []judge.Finding{}
	}
	events := snap.Events
	if events == nil {
		events = []board.Event{}
	}
	body, err := json.Marshal(Body{Estate: s.estate, RefreshedAt: snap.RefreshedAt, Level: ev.Estate.Level, Tally: ev.Estate, Packs: ev.Packs, Pillars: ev.Pillars, Owners: ev.Owners, Findings: open, Events: events})
	if err != nil {
		s.log.ErrorContext(ctx, "webhook body failed", "error", err)
		return fmt.Errorf("webhook body: %w", err)
	}
	if err := s.post(ctx, body); err != nil {
		s.metrics.Push(ctx, "failed")
		s.log.ErrorContext(ctx, "webhook push failed", "error", err)
		return fmt.Errorf("webhook: %w", err)
	}
	s.metrics.Push(ctx, "ok")
	s.log.InfoContext(ctx, "webhook push done", "findings", len(open), "events", len(events))
	return nil
}

func (s *Sink) post(ctx context.Context, body []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if s.token != "" {
		req.Header.Set("Authorization", "Bearer "+s.token)
	}
	res, err := s.http.Do(req)
	if err != nil {
		return err
	}
	_, _ = io.Copy(io.Discard, res.Body)
	res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return fmt.Errorf("the webhook answered %s", res.Status)
	}
	return nil
}
