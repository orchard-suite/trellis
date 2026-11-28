package gate

import (
	"context"
	"strconv"
	"time"

	"github.com/orchard-suite/platformkit/v3/change"

	"github.com/orchard-suite/trellis/internal/estate"
	"github.com/orchard-suite/trellis/internal/pack"
)

// Source stamps every fact the judge builds from a statement.
const Source = "statement"

// input is one statement made ready for the gate's rules: a subject for the
// change, one per effect, and one per unknown part, each with the facts a
// rule reads and the catalog's subjects for what it names as linked.
type input struct {
	st       *change.Statement
	proof    []Proof
	whole    *pack.Prepared
	effects  []*pack.Prepared
	unknowns []*pack.Prepared
}

func (in *input) subjects(kind string) []*pack.Prepared {
	switch kind {
	case pack.GateChange:
		return []*pack.Prepared{in.whole}
	case pack.GateEffect:
		return in.effects
	case pack.GateUnknown:
		return in.unknowns
	}
	return nil
}

// prepare reads each effect's proof, then builds the subjects. The facts are
// plain JSON data, as a source's are, so a rule and its fixtures read the
// same shapes.
//
// A rule sees, on every gate subject, change: the statement (key, digest,
// writer, actor, things, artifact, applier, effects, and unknown). An
// effect's subject adds effect: its index, kind, family, unit, subject,
// path, size, undo, claim, proof (level, read, watched, clean, harm, blind,
// last_seen, error), safe_paths (path, proven, proven_at, failed_at), safe
// (its path is one the watch proved on the thing after it last did harm),
// held (the catalog holds the thing; null before the board read it), and
// leaning (how many subjects on the board are linked to the thing). An
// unknown part's adds unknown: index, thing, part, and why. linked holds,
// by kind, the board's subjects for the thing an effect or a part names,
// and for the change every thing it touches.
func (j *Judge) prepare(ctx context.Context, st *change.Statement, proofs Proofs, now time.Time) *input {
	cat := j.s.Catalog.current()
	in := &input{st: st, proof: j.proofs(ctx, st, proofs, cat)}
	effects := make([]any, len(st.Effects))
	for i := range st.Effects {
		effects[i] = j.effectView(i, st.Effects[i], in.proof[i], cat)
	}
	unknowns := make([]any, len(st.Unknown))
	for i, u := range st.Unknown {
		unknowns[i] = unknownView(i, u)
	}
	whole := changeView(st, effects, unknowns)
	fact := func(v any) estate.Fact { return estate.Fact{Value: v, Source: Source, ObservedAt: now} }

	s := &estate.Subject{Kind: pack.GateChange, Key: st.Key, Title: st.Key, Facts: map[string]estate.Fact{"change": fact(whole)}}
	in.whole = pack.Prepare(s, now, cat.linked(st.Things...))
	in.effects = make([]*pack.Prepared, len(st.Effects))
	for i, e := range st.Effects {
		s := &estate.Subject{Kind: pack.GateEffect, Key: strconv.Itoa(i), Title: e.Kind + " " + e.Subject.Blueprint + "/" + e.Subject.Key, Facts: map[string]estate.Fact{"change": fact(whole), "effect": fact(effects[i])}}
		in.effects[i] = pack.Prepare(s, now, cat.linked(e.Subject))
	}
	in.unknowns = make([]*pack.Prepared, len(st.Unknown))
	for i, u := range st.Unknown {
		s := &estate.Subject{Kind: pack.GateUnknown, Key: strconv.Itoa(i), Title: u.Part, Facts: map[string]estate.Fact{"change": fact(whole), "unknown": fact(unknowns[i])}}
		var linked map[string][]*estate.Subject
		if u.Thing != nil {
			linked = cat.linked(*u.Thing)
		}
		in.unknowns[i] = pack.Prepare(s, now, linked)
	}
	return in
}

func (j *Judge) effectView(i int, e change.Effect, p Proof, cat *index) map[string]any {
	v := map[string]any{
		"index":   float64(i),
		"kind":    e.Kind,
		"family":  nil,
		"unit":    nil,
		"subject": refView(e.Subject),
		"path":    e.Path,
		"size":    nil,
		"undo":    e.Undo,
		"claim":   map[string]any{"from": e.Claim.From, "rule": e.Claim.Rule, "version": e.Claim.Version},
		"proof":   p.view(),
	}
	if k, ok := j.kinds.Get(e.Kind); ok {
		v["family"] = k.Family
		if k.Unit != "" {
			v["unit"] = k.Unit
		}
	}
	if e.Size != nil {
		v["size"] = float64(*e.Size)
	}
	paths, safe := cat.safe(e.Subject, e.Path)
	v["safe_paths"], v["safe"] = paths, safe
	v["held"], v["leaning"] = cat.held(e.Subject)
	return v
}

func unknownView(i int, u change.Unknown) map[string]any {
	v := map[string]any{"index": float64(i), "thing": nil, "part": u.Part, "why": u.Why}
	if u.Thing != nil {
		v["thing"] = refView(*u.Thing)
	}
	return v
}

func changeView(st *change.Statement, effects, unknowns []any) map[string]any {
	things := make([]any, len(st.Things))
	for i, t := range st.Things {
		things[i] = refView(t)
	}
	digest, _ := st.Digest()
	return map[string]any{
		"key":      st.Key,
		"digest":   digest,
		"writer":   map[string]any{"name": st.Writer.Name, "version": st.Writer.Version},
		"actor":    map[string]any{"kind": st.Actor.Kind, "id": st.Actor.ID, "on_behalf_of": st.Actor.OnBehalfOf},
		"things":   things,
		"artifact": map[string]any{"kind": st.Artifact.Kind, "digest": st.Artifact.Digest},
		"applier":  st.Applier,
		"effects":  effects,
		"unknown":  unknowns,
	}
}

func refView(r change.Ref) map[string]any {
	return map[string]any{"blueprint": r.Blueprint, "key": r.Key}
}
