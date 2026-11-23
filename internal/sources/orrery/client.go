package orrery

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/orchard-suite/trellis/internal/obs"
)

// Client is the few calls trellis makes to an Orrery hub: name its lists,
// list entities, offer blueprints, run a snapshot, write one entity, and
// put an icon.
type Client struct {
	base, token string
	http        *http.Client
	log         *slog.Logger
	retries     int
	// kept is the last answer to each page of a list, by its address.
	mu   sync.Mutex
	kept map[string]keptPage
}

// NewClient talks to the hub at base (its ingest service, without /api)
// with token.
func NewClient(base, token string, log *slog.Logger) *Client {
	return &Client{base: strings.TrimRight(base, "/"), token: token, http: obs.Client(60 * time.Second), log: log, retries: 3, kept: map[string]keptPage{}}
}

// Record is one entity as the API lists it.
type Record struct {
	Blueprint string                     `json:"blueprint"`
	Key       string                     `json:"key"`
	Title     string                     `json:"title"`
	Fields    map[string]json.RawMessage `json:"fields"`
	Relations map[string][]Ref           `json:"relations"`
}

// Ref is a relation's target.
type Ref struct {
	Blueprint string `json:"blueprint"`
	Key       string `json:"key"`
	Title     string `json:"title"`
}

// Field reads one field as plain JSON data; nil when absent or null.
func (r Record) Field(name string) any {
	var v any
	_ = json.Unmarshal(r.Fields[name], &v)
	return v
}

// String reads a string field, "" when absent.
func (r Record) String(name string) string {
	s, _ := r.Field(name).(string)
	return s
}

// Bool reads a boolean field and whether it was set.
func (r Record) Bool(name string) (bool, bool) {
	b, ok := r.Field(name).(bool)
	return b, ok
}

// Related is the first target of a relation.
func (r Record) Related(name string) (Ref, bool) {
	if refs := r.Relations[name]; len(refs) > 0 {
		return refs[0], true
	}
	return Ref{}, false
}

// Entity is one write.
type Entity struct {
	Blueprint string         `json:"blueprint"`
	Key       string         `json:"key"`
	Title     string         `json:"title,omitempty"`
	Fields    map[string]any `json:"fields,omitempty"`
	Relations map[string]any `json:"relations,omitempty"`
}

const pageSize = 2000

// Blueprints are the ids of the lists the hub has, as the token sees them:
// one the token may not read is not among them.
func (c *Client) Blueprints(ctx context.Context) (map[string]bool, error) {
	var res struct {
		Blueprints []struct {
			ID string `json:"id"`
		} `json:"blueprints"`
	}
	if err := c.do(ctx, http.MethodGet, "/v1/blueprints", nil, &res); err != nil {
		return nil, fmt.Errorf("list the hub's blueprints: %w", err)
	}
	out := make(map[string]bool, len(res.Blueprints))
	for _, bp := range res.Blueprints {
		out[bp.ID] = true
	}
	return out, nil
}

// Active lists every active entity of a blueprint, page by page, cut to
// what reads asks for. A page the hub says has not moved since it was last
// read is the copy kept from then.
func (c *Client) Active(ctx context.Context, blueprint string, reads Reads) ([]Record, error) {
	var all []Record
	pages, kept := 0, 0
	for offset := 0; ; offset += pageSize {
		v := url.Values{"blueprint": {blueprint}, "state": {"active"}, "limit": {strconv.Itoa(pageSize)}, "offset": {strconv.Itoa(offset)}}
		if f := reads.FieldNames(); f != nil {
			v.Set("fields", strings.Join(f, ","))
		}
		if r := reads.RelationNames(); r != nil {
			v.Set("relations", strings.Join(r, ","))
		}
		page, same, err := c.listPage(ctx, "/v1/entities?"+v.Encode())
		if err != nil {
			return nil, fmt.Errorf("list %s: %w", blueprint, err)
		}
		pages++
		if same {
			kept++
		}
		for _, r := range page.Entities {
			if r.Blueprint == "" {
				r.Blueprint = blueprint
			}
			all = append(all, r)
		}
		if len(page.Entities) < pageSize || len(all) >= page.Total {
			c.log.InfoContext(ctx, "orrery list read", "blueprint", blueprint, "entities", len(all), "pages", pages, "unchanged", kept)
			return all, nil
		}
	}
}

// listing is one page of a list as the hub answers it.
type listing struct {
	Total    int      `json:"total"`
	Entities []Record `json:"entities"`
}

// keptPage is a page and the tag the hub named it by.
type keptPage struct {
	etag string
	page listing
}

// listPage reads one page, sending back the tag of the copy kept of the
// same address. The hub answers 304 when nothing the page reads has moved,
// and then the kept copy is the page; a 200 replaces it. A tag the hub no
// longer knows, after it restarts or from another of its pods, is a 200.
func (c *Client) listPage(ctx context.Context, path string) (listing, bool, error) {
	c.mu.Lock()
	have, ok := c.kept[path]
	c.mu.Unlock()
	header := http.Header{}
	if ok {
		header.Set("If-None-Match", have.etag)
	}
	res, err := c.send(ctx, http.MethodGet, path, "", nil, header)
	if err != nil {
		return listing{}, false, err
	}
	if res.status == http.StatusNotModified {
		if !ok {
			return listing{}, false, errors.New("orrery answered 304 to a page trellis holds no copy of")
		}
		return have.page, true, nil
	}
	var page listing
	if err := json.Unmarshal(res.body, &page); err != nil {
		return listing{}, false, err
	}
	c.mu.Lock()
	if tag := res.header.Get("ETag"); tag != "" {
		c.kept[path] = keptPage{etag: tag, page: page}
	} else {
		delete(c.kept, path)
	}
	c.mu.Unlock()
	return page, false, nil
}

// Offer brings blueprints to the hub: it creates the missing ones and adds
// what an existing one lacks. A refused blueprint is an error.
func (c *Client) Offer(ctx context.Context, blueprints []map[string]any) error {
	var res struct {
		Created  []string `json:"created"`
		Extended []string `json:"extended"`
		Rejected []struct {
			Blueprint string `json:"blueprint"`
			Reason    string `json:"reason"`
		} `json:"rejected"`
	}
	if err := c.do(ctx, http.MethodPut, "/v1/ingest/blueprints", map[string]any{"blueprints": blueprints}, &res); err != nil {
		return err
	}
	c.log.Info("orrery blueprints offered", "created", res.Created, "extended", res.Extended, "rejected", len(res.Rejected))
	if len(res.Rejected) > 0 {
		return fmt.Errorf("blueprint %s refused: %s", res.Rejected[0].Blueprint, res.Rejected[0].Reason)
	}
	return nil
}

// RunCounts is what the hub did with a snapshot, and how trellis sent it:
// Sent in full, or Sighted by its digest alone.
type RunCounts struct {
	Created   int `json:"created"`
	Updated   int `json:"updated"`
	Unchanged int `json:"unchanged"`
	Rejected  int `json:"rejected"`
	Deleted   int `json:"deleted"`
	Sent      int `json:"-"`
	Sighted   int `json:"-"`
}

const batchSize = 200

// whole is an entity as a batch carries it in full, with the digest the hub
// keeps for it where it takes sightings.
type whole struct {
	Entity
	Digest string `json:"digest,omitempty"`
}

// sighting names an entity by the digest of what was last sent of it.
type sighting struct {
	Blueprint string `json:"blueprint"`
	Key       string `json:"key"`
	Digest    string `json:"digest"`
}

// Digest is the fingerprint of what one item sends of an entity: the same
// entity always gives the same digest, and any change to it gives another.
// Its JSON is stable because map keys are written sorted.
func Digest(e Entity) string {
	b, _ := json.Marshal(e)
	sum := sha256.Sum256(b)
	return base64.RawURLEncoding.EncodeToString(sum[:16])
}

// Snapshot writes entities as one snapshot run of scope: the hub removes
// every entity of the scope the run does not name. Where the hub takes
// sightings, each batch names its entities by digest first and sends in
// full only the ones the hub wants, so a pass costs what changed. A failed
// write fails the run, so nothing is removed.
func (c *Client) Snapshot(ctx context.Context, scope string, entities []Entity) (RunCounts, error) {
	var run struct {
		ID        string `json:"id"`
		Sightings bool   `json:"sightings"`
	}
	if err := c.do(ctx, http.MethodPost, "/v1/ingest/runs", map[string]string{"mode": "snapshot", "scope": scope, "write": "upsert"}, &run); err != nil {
		return RunCounts{}, err
	}
	path := "/v1/ingest/runs/" + url.PathEscape(run.ID)
	fail := func(err error) (RunCounts, error) {
		_ = c.do(context.WithoutCancel(ctx), http.MethodPost, path+"/fail", map[string]string{"reason": err.Error()}, nil)
		return RunCounts{}, err
	}
	sent, sighted := 0, 0
	for start := 0; start < len(entities); start += batchSize {
		batch := entities[start:min(start+batchSize, len(entities))]
		full := batch
		if run.Sightings {
			named := make([]sighting, len(batch))
			for i, e := range batch {
				named[i] = sighting{Blueprint: e.Blueprint, Key: e.Key, Digest: Digest(e)}
			}
			wanted, err := c.putEntities(ctx, path, named)
			if err != nil {
				return fail(err)
			}
			want := make(map[Ref]bool, len(wanted))
			for _, w := range wanted {
				want[Ref{Blueprint: w.Blueprint, Key: w.Key}] = true
			}
			full = nil
			for _, e := range batch {
				if want[Ref{Blueprint: e.Blueprint, Key: e.Key}] {
					full = append(full, e)
				}
			}
			sighted += len(batch) - len(full)
		}
		if len(full) == 0 {
			continue
		}
		items := make([]whole, len(full))
		for i, e := range full {
			items[i] = whole{Entity: e}
			if run.Sightings {
				items[i].Digest = Digest(e)
			}
		}
		if _, err := c.putEntities(ctx, path, items); err != nil {
			return fail(err)
		}
		sent += len(full)
	}
	var done struct {
		Counts RunCounts `json:"counts"`
	}
	if err := c.do(ctx, http.MethodPost, path+"/commit", nil, &done); err != nil {
		return RunCounts{}, err
	}
	done.Counts.Sent, done.Counts.Sighted = sent, sighted
	c.log.InfoContext(ctx, "orrery run committed", "run", run.ID, "scope", scope, "sightings", run.Sightings, "sent", sent, "sighted", sighted)
	return done.Counts, nil
}

// putEntities sends one batch to a run and answers the sightings the hub
// wants sent in full.
func (c *Client) putEntities(ctx context.Context, path string, items any) ([]Ref, error) {
	var res struct {
		Rejected []struct {
			Blueprint string `json:"blueprint"`
			Key       string `json:"key"`
			Field     string `json:"field"`
			Reason    string `json:"reason"`
		} `json:"rejected"`
		Wanted []Ref `json:"wanted"`
	}
	if err := c.do(ctx, http.MethodPut, path+"/entities", map[string]any{"entities": items}, &res); err != nil {
		return nil, err
	}
	for _, r := range res.Rejected {
		c.log.WarnContext(ctx, "orrery rejected entity", "entity", r.Blueprint+":"+r.Key, "field", r.Field, "reason", r.Reason)
	}
	return res.Wanted, nil
}

// PutScorecard saves one scorecard by id.
func (c *Client) PutScorecard(ctx context.Context, id string, body any) error {
	return c.do(ctx, http.MethodPut, "/v1/scorecards/"+url.PathEscape(id), body, nil)
}

// StatusError is a hub's refusal of a call: a 4xx answer and what it said.
type StatusError struct {
	Code    int
	Message string
}

func (e *StatusError) Error() string { return fmt.Sprintf("orrery answered %d: %s", e.Code, e.Message) }

func (c *Client) do(ctx context.Context, method, path string, in, out any) error {
	var body []byte
	contentType := ""
	if in != nil {
		var err error
		if body, err = json.Marshal(in); err != nil {
			return err
		}
		contentType = "application/json"
	}
	res, err := c.send(ctx, method, path, contentType, body, nil)
	if err != nil {
		return err
	}
	if out != nil && len(res.body) > 0 {
		return json.Unmarshal(res.body, out)
	}
	return nil
}

// answer is what the hub said to one call.
type answer struct {
	status int
	header http.Header
	body   []byte
}

// send makes one call with header added, retrying network errors and 5xx
// answers.
func (c *Client) send(ctx context.Context, method, path, contentType string, body []byte, header http.Header) (answer, error) {
	var last error
	for attempt := 0; attempt <= c.retries; attempt++ {
		if attempt > 0 {
			wait := time.Duration(1<<uint(attempt-1))*time.Second + time.Duration(rand.IntN(500))*time.Millisecond
			c.log.WarnContext(ctx, "orrery request retry", "method", method, "path", path, "attempt", attempt, "error", last)
			select {
			case <-ctx.Done():
				return answer{}, ctx.Err()
			case <-time.After(wait):
			}
		}
		req, err := http.NewRequestWithContext(ctx, method, c.base+path, bytes.NewReader(body))
		if err != nil {
			return answer{}, err
		}
		for k, vs := range header {
			req.Header[k] = vs
		}
		req.Header.Set("Authorization", "Bearer "+c.token)
		req.Header.Set("Accept", "application/json")
		if contentType != "" {
			req.Header.Set("Content-Type", contentType)
		}
		res, err := c.http.Do(req)
		if err != nil {
			last = err
			continue
		}
		data, err := io.ReadAll(io.LimitReader(res.Body, 256<<20))
		res.Body.Close()
		if err != nil {
			last = err
			continue
		}
		if res.StatusCode >= 500 {
			last = fmt.Errorf("orrery answered %d: %s", res.StatusCode, strings.TrimSpace(string(data)))
			continue
		}
		if res.StatusCode >= 400 {
			var parsed struct {
				Error string `json:"error"`
			}
			msg := strings.TrimSpace(string(data))
			if json.Unmarshal(data, &parsed) == nil && parsed.Error != "" {
				msg = parsed.Error
			}
			return answer{}, &StatusError{Code: res.StatusCode, Message: msg}
		}
		return answer{status: res.StatusCode, header: res.Header, body: data}, nil
	}
	return answer{}, fmt.Errorf("orrery: giving up after %d tries: %w", c.retries+1, last)
}
