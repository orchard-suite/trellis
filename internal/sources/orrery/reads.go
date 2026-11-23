package orrery

import (
	"sort"

	"cel.dev/cel-go/cel"
	celast "cel.dev/cel-go/common/ast"
	"cel.dev/cel-go/common/operators"
	"cel.dev/cel-go/common/types"
)

// Reads is what the mappings read of a list's entities: the fields and the
// relations by name, or every one of either. The hub is asked for those
// alone, so a list costs what a judgment reads.
type Reads struct {
	Fields, Relations       map[string]bool
	AllFields, AllRelations bool
}

func newReads() Reads {
	return Reads{Fields: map[string]bool{}, Relations: map[string]bool{}}
}

// add takes in what another expression reads.
func (r *Reads) add(o Reads) {
	r.AllFields = r.AllFields || o.AllFields
	r.AllRelations = r.AllRelations || o.AllRelations
	for f := range o.Fields {
		r.Fields[f] = true
	}
	for f := range o.Relations {
		r.Relations[f] = true
	}
}

// FieldNames is the fields to ask for, sorted; nil asks for every one.
func (r Reads) FieldNames() []string { return names(r.Fields, r.AllFields) }

// RelationNames is the relations to ask for, sorted; nil asks for every one.
func (r Reads) RelationNames() []string { return names(r.Relations, r.AllRelations) }

func names(set map[string]bool, all bool) []string {
	if all {
		return nil
	}
	out := make([]string, 0, len(set))
	for n := range set {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// readsOf is what one expression reads. A field or relation is read by
// name, `x.fields.name`, `x.fields.?name`, or `x.fields["name"]`, on the
// entity or on any entity reached through it or a join. A `fields` or
// `relations` map used any other way, or the entity itself used whole,
// reads every one. A name is counted wherever it is read, so a mapping
// asks it of its own list and of each list it joins.
func readsOf(p *cel.Ast) Reads {
	r := newReads()
	if p != nil {
		walk(p.NativeRep().Expr(), &r)
	}
	return r
}

// mapOf says whether e is an entity's fields or relations map, and which.
func mapOf(e celast.Expr) (string, bool) {
	var name string
	switch e.Kind() {
	case celast.SelectKind:
		name = e.AsSelect().FieldName()
	case celast.CallKind:
		c := e.AsCall()
		if c.FunctionName() != operators.OptSelect || len(c.Args()) != 2 {
			return "", false
		}
		name, _ = constString(c.Args()[1])
	}
	return name, name == "fields" || name == "relations"
}

// mapBase is what a fields or relations map is taken from.
func mapBase(e celast.Expr) celast.Expr {
	if e.Kind() == celast.SelectKind {
		return e.AsSelect().Operand()
	}
	return e.AsCall().Args()[0]
}

func constString(e celast.Expr) (string, bool) {
	if e.Kind() != celast.LiteralKind {
		return "", false
	}
	s, ok := e.AsLiteral().(types.String)
	return string(s), ok
}

// named reads one name out of a map: what the access reads, and the map.
func named(e celast.Expr) (celast.Expr, string, bool) {
	switch e.Kind() {
	case celast.SelectKind:
		s := e.AsSelect()
		return s.Operand(), s.FieldName(), true
	case celast.CallKind:
		c := e.AsCall()
		switch c.FunctionName() {
		case operators.OptSelect, operators.Index, operators.OptIndex:
			if len(c.Args()) == 2 {
				if n, ok := constString(c.Args()[1]); ok {
					return c.Args()[0], n, true
				}
			}
		}
	}
	return nil, "", false
}

func mark(r *Reads, kind, name string) {
	if kind == "fields" {
		r.Fields[name] = true
	} else {
		r.Relations[name] = true
	}
}

func markAll(r *Reads, kind string) {
	if kind == "fields" {
		r.AllFields = true
	} else {
		r.AllRelations = true
	}
}

// operand walks what a select or an access reads from: a variable there is
// read by name, not whole.
func operand(e celast.Expr, r *Reads) {
	if e.Kind() != celast.IdentKind {
		walk(e, r)
	}
}

func walk(e celast.Expr, r *Reads) {
	if m, name, ok := named(e); ok {
		if kind, isMap := mapOf(m); isMap {
			mark(r, kind, name)
			operand(mapBase(m), r)
			return
		}
	}
	if kind, isMap := mapOf(e); isMap {
		markAll(r, kind)
		operand(mapBase(e), r)
		return
	}
	switch e.Kind() {
	case celast.IdentKind:
		if e.AsIdent() == "entity" {
			r.AllFields, r.AllRelations = true, true
		}
	case celast.SelectKind:
		operand(e.AsSelect().Operand(), r)
	case celast.CallKind:
		c := e.AsCall()
		if c.IsMemberFunction() {
			walk(c.Target(), r)
		}
		for i, a := range c.Args() {
			if i == 0 && c.FunctionName() == operators.OptSelect {
				operand(a, r)
				continue
			}
			walk(a, r)
		}
	case celast.ListKind:
		for _, el := range e.AsList().Elements() {
			walk(el, r)
		}
	case celast.MapKind:
		for _, en := range e.AsMap().Entries() {
			me := en.AsMapEntry()
			walk(me.Key(), r)
			walk(me.Value(), r)
		}
	case celast.StructKind:
		for _, f := range e.AsStruct().Fields() {
			walk(f.AsStructField().Value(), r)
		}
	case celast.ComprehensionKind:
		c := e.AsComprehension()
		for _, part := range []celast.Expr{c.IterRange(), c.AccuInit(), c.LoopCondition(), c.LoopStep(), c.Result()} {
			walk(part, r)
		}
	}
}

// asked is what Collect asks the hub for, by list: everything each mapping
// reads of its own list and of the lists it joins.
func asked(maps []*compiled) map[string]Reads {
	out := map[string]Reads{}
	for _, m := range maps {
		bps := []string{m.Blueprint}
		for _, j := range m.Join {
			bps = append(bps, j.Blueprint)
		}
		for _, bp := range bps {
			have, ok := out[bp]
			if !ok {
				have = newReads()
			}
			have.add(m.reads)
			out[bp] = have
		}
	}
	return out
}
