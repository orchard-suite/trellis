package orrery

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/orchard-suite/trellis/internal/estate"
)

func readsFor(t *testing.T, m Mapping) Reads {
	t.Helper()
	c, err := Compile(m)
	if err != nil {
		t.Fatal(err)
	}
	return c.reads
}

func TestAnExpressionReadsTheFieldsItNames(t *testing.T) {
	r := readsFor(t, Mapping{Kind: "k", Blueprint: "b", Fields: map[string]string{
		"a": `entity.fields.?plain.orValue(null)`,
		"b": `has(entity.fields.tested) ? entity.fields["indexed"] : entity.?fields.?optional.orValue(0)`,
		"c": `entity.relations.?owner.orValue([]).map(o, o.key)`,
		"d": `joins.other[entity.key].exists(x, x.fields.joined == 1 && size(x.relations.linked) > 0)`,
	}, Where: `entity.key != ""`, Join: map[string]Join{"other": {Blueprint: "o", Key: `entity.fields.keyed`}}})
	if got := r.FieldNames(); !reflect.DeepEqual(got, []string{"indexed", "joined", "keyed", "optional", "plain", "tested"}) {
		t.Errorf("fields: %v", got)
	}
	if got := r.RelationNames(); !reflect.DeepEqual(got, []string{"linked", "owner"}) {
		t.Errorf("relations: %v", got)
	}
}

func TestAnExpressionThatTakesTheEntityWholeReadsEverything(t *testing.T) {
	for name, m := range map[string]Mapping{
		"no fields":     {Kind: "k", Blueprint: "b"},
		"entity whole":  {Kind: "k", Blueprint: "b", Fields: map[string]string{"x": `[entity].size()`}},
		"fields whole":  {Kind: "k", Blueprint: "b", Fields: map[string]string{"x": `entity.fields.size()`}},
		"a name worked": {Kind: "k", Blueprint: "b", Fields: map[string]string{"x": `entity.fields[entity.key]`}},
	} {
		r := readsFor(t, m)
		if r.FieldNames() != nil {
			t.Errorf("%s: asks for fields %v, not every one", name, r.FieldNames())
		}
	}
	r := readsFor(t, Mapping{Kind: "k", Blueprint: "b", Fields: map[string]string{"x": `entity.relations`}})
	if r.RelationNames() != nil || r.FieldNames() == nil {
		t.Errorf("a relations map taken whole reads every relation and no field: %+v", r)
	}
}

func TestTheStandardMappingsAskForWhatTheyRead(t *testing.T) {
	got := asked(standard(t))
	images := got["sw_container_image"]
	want := []string{"cve_critical", "cve_fixable", "cve_high", "cve_kev", "cve_total", "epss_max", "images", "software_count"}
	if !reflect.DeepEqual(images.FieldNames(), want) {
		t.Errorf("an image scan asks for %v, want %v", images.FieldNames(), want)
	}
	if rels := images.RelationNames(); rels == nil || len(rels) != 0 {
		t.Errorf("an image scan asks for no relation: %v", rels)
	}
	if rels := got["entra_app_role_assignment"].RelationNames(); !reflect.DeepEqual(rels, []string{"app", "group", "sign_in", "user"}) {
		t.Errorf("a grant asks for its app, its holders, and what the application mapping that joins it names: %v", rels)
	}
	if f := got["plat_waiver"].FieldNames(); !reflect.DeepEqual(f, []string{"category", "control", "expires_at", "match", "owner", "reason", "review_at", "rule", "subject"}) {
		t.Errorf("a waiver asks for what a waiver holds: %v", f)
	}
	for bp := range needed(standard(t)) {
		if _, ok := got[bp]; !ok {
			t.Errorf("%s is read but nothing says what to ask of it", bp)
		}
	}
}

// cut is what the hub answers for a list asked with reads.
func cut(recs Records, reads map[string]Reads) Records {
	out := Records{}
	for bp, list := range recs {
		r := reads[bp]
		for _, rec := range list {
			c := Record{Blueprint: rec.Blueprint, Key: rec.Key, Title: rec.Title, Fields: rec.Fields, Relations: rec.Relations}
			if names := r.FieldNames(); names != nil {
				c.Fields = map[string]json.RawMessage{}
				for _, n := range names {
					if v, ok := rec.Fields[n]; ok {
						c.Fields[n] = v
					}
				}
			}
			if names := r.RelationNames(); names != nil {
				c.Relations = map[string][]Ref{}
				for _, n := range names {
					if v, ok := rec.Relations[n]; ok {
						c.Relations[n] = v
					}
				}
			}
			out[bp] = append(out[bp], c)
		}
	}
	return out
}

func snapshot(e *estate.Estate) map[string]any {
	out := map[string]any{"waivers": e.Waivers}
	for _, k := range e.Kinds() {
		out[k] = e.Of(k)
	}
	return out
}

func TestACutReadJudgesTheSameAsAWholeOne(t *testing.T) {
	now := time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC)
	maps := standard(t)
	for name, recs := range map[string]Records{"standard": standardRecords(t), "access": accessRecords(t)} {
		whole, cutRead := estate.New(now), estate.New(now)
		apply(t, whole, maps, recs)
		apply(t, cutRead, maps, cut(recs, asked(maps)))
		if a, b := snapshot(whole), snapshot(cutRead); !reflect.DeepEqual(a, b) {
			wa, _ := json.Marshal(a)
			wb, _ := json.Marshal(b)
			t.Errorf("%s: a cut read judges otherwise:\nwhole %s\ncut   %s", name, wa, wb)
		}
	}
}

func TestAListPageUnchangedIsTheCopyKept(t *testing.T) {
	var body atomic.Value
	var tag atomic.Value
	body.Store(`{"total":1,"entities":[{"key":"a","title":"A","fields":{"n":1}}]}`)
	tag.Store(`"t1"`)
	var asked, notModified atomic.Int32
	var query atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked.Add(1)
		query.Store(r.URL.RawQuery)
		w.Header().Set("ETag", tag.Load().(string))
		if r.Header.Get("If-None-Match") == tag.Load().(string) {
			notModified.Add(1)
			w.WriteHeader(http.StatusNotModified)
			return
		}
		_, _ = io.WriteString(w, body.Load().(string))
	}))
	defer srv.Close()
	c := NewClient(srv.URL, "tok", slog.New(slog.NewTextHandler(io.Discard, nil)))
	reads := Reads{Fields: map[string]bool{"n": true}, Relations: map[string]bool{}}
	read := func() []Record {
		t.Helper()
		got, err := c.Active(context.Background(), "b", reads)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}

	first := read()
	if q := query.Load().(string); q != "blueprint=b&fields=n&limit=2000&offset=0&relations=&state=active" {
		t.Errorf("asked %s", q)
	}
	second := read()
	if notModified.Load() != 1 || !reflect.DeepEqual(first, second) || second[0].Blueprint != "b" || second[0].Field("n") != 1.0 {
		t.Errorf("an unchanged page is the copy kept: 304s %d, %+v", notModified.Load(), second)
	}

	body.Store(`{"total":1,"entities":[{"key":"a","title":"A","fields":{"n":2}}]}`)
	tag.Store(`"t2"`)
	if got := read(); got[0].Field("n") != 2.0 {
		t.Errorf("a page that moved is read afresh: %+v", got)
	}
	if got := read(); notModified.Load() != 2 || got[0].Field("n") != 2.0 {
		t.Errorf("the fresh page replaces the copy kept: 304s %d, %+v", notModified.Load(), got)
	}
	if asked.Load() != 4 {
		t.Errorf("calls: %d", asked.Load())
	}
}
