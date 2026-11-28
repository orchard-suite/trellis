package gate

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/orchard-suite/platformkit/v3/annals"
	"github.com/orchard-suite/platformkit/v3/change"

	"github.com/orchard-suite/trellis"
	"github.com/orchard-suite/trellis/internal/estate"
	"github.com/orchard-suite/trellis/internal/pack"
)

// The scale target (one estate of 150 people after ten years): a million
// things in the catalog. A statement holds the most effects and things the
// kit allows.
const (
	catalogThings = 1_000_000
	blueprints    = 40
)

var paths = []string{change.PathInPlace, change.PathSwap, change.PathRolling}

var scale struct {
	once  sync.Once
	cat   *Catalog
	built time.Duration
}

// scaleCatalog is the board's estate at the target: every thing a subject
// with its catalog ref, a fact, and a link, and a safe path on one in ten.
func scaleCatalog(b *testing.B) *Catalog {
	scale.once.Do(func() {
		e := estate.New(now)
		w := e.Writer("orrery")
		for i := range catalogThings {
			bp := fmt.Sprintf("bp%02d", i%blueprints)
			key := fmt.Sprintf("thing-%07d", i)
			h := w.Subject(bp, key, key)
			h.SetRef(estate.Ref{Blueprint: bp, Key: key})
			h.Facts[bp] = estate.Fact{Value: map[string]any{"tier": float64(i % 3), "owner": "platform"}, Source: "orrery", ObservedAt: now}
			if i > 0 {
				h.Links = append(h.Links, estate.Link{Kind: fmt.Sprintf("bp%02d", (i-1)%blueprints), Key: fmt.Sprintf("thing-%07d", i-1)})
			}
			if i%10 == 0 {
				sp := w.Subject("safe_path", bp+"/"+key+"/swap", "")
				sp.Facts["safe_path"] = estate.Fact{Value: map[string]any{"path": "swap", "subject_blueprint": bp, "subject_key": key, "proven_at": "2026-11-01T00:00:00Z", "failed_at": nil}, Source: "orrery", ObservedAt: now}
			}
		}
		scale.cat = NewCatalog("safe_path")
		started := time.Now()
		scale.cat.Update(e)
		scale.built = time.Since(started)
		b.Logf("the catalog's index of %d things took %s to build, once a pass", catalogThings, scale.built.Round(time.Millisecond))
	})
	return scale.cat
}

// scaleStatement touches the most things the kit allows, with the most
// effects, over every base kind and the example estate's own, across the
// blueprints and paths, applied by the hub's job.
func scaleStatement(n int) *change.Statement {
	kinds := []string{"opens", "grants", "loses", "costs", "stops", "cuts", "sends"}
	st := statement(change.ApplierHub)
	st.Key = fmt.Sprintf("run-%d", n)
	st.Things = nil
	for i := range change.MaxThings {
		st.Things = append(st.Things, change.Ref{Blueprint: fmt.Sprintf("bp%02d", (i*7919)%blueprints), Key: fmt.Sprintf("thing-%07d", (i*7919+n)%catalogThings)})
	}
	for i := range change.MaxEffects {
		t := st.Things[i%len(st.Things)]
		e := effect(kinds[i%len(kinds)], t.Blueprint, t.Key, paths[i%len(paths)], int64(i%5))
		if e.Kind == "opens" || e.Kind == "loses" || e.Kind == "cuts" {
			e.Size = nil
		}
		st.Effects = append(st.Effects, e)
	}
	return st
}

// annalsServer answers proof by key over HTTP, as Annals' API port does.
func annalsServer(b *testing.B) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		_ = json.NewEncoder(w).Encode(annals.Proof{Kind: q.Get("kind"), Blueprint: q.Get("blueprint"), Path: q.Get("path"), Watched: 12, Clean: 11, Blind: 1})
	}))
	b.Cleanup(srv.Close)
	return srv
}

// BenchmarkJudge is one judgment at the scale target: a statement at the
// kit's limits, the built-in base pack and the example estate's gate pack,
// the catalog at a million things in memory, and every disruption's proof
// read from Annals by key, over HTTP as deployed, and from memory to show
// what the rules alone cost. Its time sets Limit.
func BenchmarkJudge(b *testing.B) {
	cat := scaleCatalog(b)
	set, err := pack.LoadGate(trellis.Packs(), "../../deploy/kubernetes/packs", []string{"example-gate"})
	if err != nil {
		b.Fatal(err)
	}
	srv := annalsServer(b)
	inMemory := &fakeAnnals{counts: map[annals.ProofKey]annals.Proof{}}
	for i := range blueprints {
		for _, kind := range []string{"stops", "cuts"} {
			for _, path := range paths {
				inMemory.counts[annals.ProofKey{Kind: kind, Blueprint: fmt.Sprintf("bp%02d", i), Path: path}] = annals.Proof{Watched: 12, Clean: 11, Blind: 1}
			}
		}
	}
	for name, proofs := range map[string]Proofs{"annals-http": annals.New(srv.URL, "test-token", "trellis"), "proof-in-memory": inMemory} {
		b.Run(name, func(b *testing.B) { benchJudge(b, set, proofs, cat) })
	}
}

func benchJudge(b *testing.B, set *pack.Set, proofs Proofs, cat *Catalog) {
	j, err := New(set, Settings{Approvers: approvers, Proofs: proofs, Catalog: cat, Version: "bench", Now: func() time.Time { return now }})
	if err != nil {
		b.Fatal(err)
	}
	statements := make([]*change.Statement, 16)
	for i := range statements {
		statements[i] = scaleStatement(i)
		if err := statements[i].Validate(j.Kinds()); err != nil {
			b.Fatal(err)
		}
	}
	var slowest time.Duration
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; b.Loop(); i++ {
		started := time.Now()
		ans, err := j.Judge(b.Context(), statements[i%len(statements)])
		if err != nil {
			b.Fatal(err)
		}
		if took := time.Since(started); took > slowest {
			slowest = took
		}
		if ans.Route.To != change.RoutePeople {
			b.Fatalf("a statement at the limits goes to people: %+v", ans.Route)
		}
	}
	b.ReportMetric(float64(slowest.Milliseconds()), "slowest-ms")
}
