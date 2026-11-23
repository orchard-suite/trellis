package orrery

import (
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"

	"cel.dev/cel-go/cel"
	"cel.dev/cel-go/common/types"
	"cel.dev/cel-go/common/types/ref"
	"cel.dev/cel-go/common/types/traits"
	"cel.dev/cel-go/ext"

	"github.com/orchard-suite/trellis/internal/estate"
	"github.com/orchard-suite/trellis/internal/sources"
)

// Mapping makes one catalog list into subjects. Nothing in trellis names a
// blueprint or a field: the estate says which list, which entities, how a
// subject is keyed, and which facts it carries, in expressions over the
// entity. A mapping with kind waiver makes waivers instead of subjects.
//
// Every expression is CEL over one variable, entity: its key, title,
// blueprint, its fields by name under fields, and under relations each
// relation's targets as a list of {blueprint, key, title}. A joined list
// (Join) is a map from key to entity under its name.
type Mapping struct {
	Kind      string `yaml:"kind"`
	Blueprint string `yaml:"blueprint"`
	// Where keeps the entities for which it is true; every one when empty.
	Where string `yaml:"where"`
	// Each makes one subject per element of a list, read as item in Key,
	// Title, and Fields. The entity is still entity.
	Each string `yaml:"each"`
	// Key and Title key and title each subject; the entity's when empty.
	Key   string `yaml:"key"`
	Title string `yaml:"title"`
	// Fact names the fact the fields are written as; the kind when empty.
	Fact string `yaml:"fact"`
	// Fields are the fact's fields, each an expression. Empty writes every
	// field of the entity and its relations, as they are.
	Fields map[string]string `yaml:"fields"`
	// Join names other lists an expression may look an entity up in.
	Join map[string]Join `yaml:"join"`
	// Optional says a list the hub has that cannot be read is a gap rather
	// than a failed read; a waiver list is always optional.
	Optional bool `yaml:"optional"`
	// Link says whether a finding on the subject links to the entity; on
	// unless false. Off for a subject that is not an entity, such as one
	// image of the many a workload runs.
	Link *bool `yaml:"link"`
	// Links ties each subject to others, a list of {kind, key}, such as the
	// workload an image runs in, so a rule or an owner reads across them.
	Links string `yaml:"links"`
	// Ref is the entity a finding on the subject links to when it is not
	// the entity read, such as the scan of an image a workload runs: a
	// blueprint, and an expression for the key. It wins over Link.
	Ref *Target `yaml:"ref"`
}

// Target is another entity, by its blueprint and a key expression.
type Target struct {
	Blueprint string `yaml:"blueprint"`
	Key       string `yaml:"key"`
}

// Join is another list, keyed by an expression over its own entities.
type Join struct {
	Blueprint string `yaml:"blueprint"`
	// Key keys each entity of the list; its key when empty.
	Key string `yaml:"key"`
	// Many keeps every entity under a key as a list; the last one when false.
	Many bool `yaml:"many"`
}

// WaiverKind is the mapping kind whose subjects are waivers. Its fields
// fill estate.Waiver: rule, subject, reason, owner, category, control,
// match, granted_at, expires_at, review_at.
const WaiverKind = "waiver"

var mappingEnv = func() *cel.Env {
	e, err := cel.NewEnv(
		cel.Variable("entity", cel.DynType),
		cel.Variable("item", cel.DynType),
		cel.Variable("joins", cel.MapType(cel.StringType, cel.DynType)),
		cel.CrossTypeNumericComparisons(true),
		cel.OptionalTypes(),
		ext.Strings(), ext.Lists(), ext.Sets(), ext.Encoders(),
		cel.Function("image",
			cel.Overload("image_string", []*cel.Type{cel.StringType}, cel.DynType,
				cel.UnaryBinding(func(v ref.Val) ref.Val {
					img := sources.ParseImage(string(v.(types.String)))
					return types.DefaultTypeAdapter.NativeToValue(map[string]any{"ref": img.Ref, "registry": img.Registry, "tag": img.Tag, "pinned": img.Pinned})
				})),
		),
		// sum adds the numbers of a list, skipping nulls; null when none is a number.
		cel.Function("sum",
			cel.Overload("sum_list", []*cel.Type{cel.ListType(cel.DynType)}, cel.DynType,
				cel.UnaryBinding(func(v ref.Val) ref.Val {
					it := v.(traits.Lister).Iterator()
					total, any := 0.0, false
					for it.HasNext() == types.True {
						switch n := it.Next().(type) {
						case types.Double:
							total, any = total+float64(n), true
						case types.Int:
							total, any = total+float64(n), true
						case types.Uint:
							total, any = total+float64(n), true
						}
					}
					if !any {
						return types.NullValue
					}
					return types.Double(total)
				})),
		),
		cel.Function("repo_url",
			cel.Overload("repo_url_string", []*cel.Type{cel.StringType}, cel.DynType,
				cel.UnaryBinding(func(v ref.Val) ref.Val {
					base, folder := SplitRepoURL(string(v.(types.String)))
					return types.DefaultTypeAdapter.NativeToValue(map[string]any{"base": strings.ToLower(base), "folder": folder})
				})),
			// An unset link names no repository: its base and folder are empty.
			cel.Overload("repo_url_null", []*cel.Type{cel.NullType}, cel.DynType,
				cel.UnaryBinding(func(ref.Val) ref.Val {
					return types.DefaultTypeAdapter.NativeToValue(map[string]any{"base": "", "folder": ""})
				})),
		),
	)
	if err != nil {
		panic(err)
	}
	return e
}()

// compiled is a mapping ready to run.
type compiled struct {
	Mapping
	where, each, key, title, links, ref cel.Program
	fields                              map[string]cel.Program
	joinKeys                            map[string]cel.Program
	// reads is everything the expressions read of an entity, of this
	// list or of one it joins.
	reads Reads
}

// compile parses one expression and adds what it reads to r.
func compile(expr string, r *Reads) (cel.Program, error) {
	if expr == "" {
		return nil, nil
	}
	ast, iss := mappingEnv.Parse(expr)
	if iss.Err() != nil {
		return nil, iss.Err()
	}
	r.add(readsOf(ast))
	return mappingEnv.Program(ast)
}

// Compile checks a mapping and parses its expressions.
func Compile(m Mapping) (*compiled, error) {
	if m.Kind == "" || m.Blueprint == "" {
		return nil, errors.New("an orrery mapping needs kind and blueprint")
	}
	c := &compiled{Mapping: m, fields: map[string]cel.Program{}, joinKeys: map[string]cel.Program{}, reads: newReads()}
	// A mapping with no fields writes the entity whole.
	if len(m.Fields) == 0 {
		c.reads.AllFields, c.reads.AllRelations = true, true
	}
	var err error
	if m.Ref != nil && (m.Ref.Blueprint == "" || m.Ref.Key == "") {
		return nil, fmt.Errorf("mapping %s/%s ref needs a blueprint and a key", m.Blueprint, m.Kind)
	}
	for name, at := range map[string]*cel.Program{"where": &c.where, "each": &c.each, "key": &c.key, "title": &c.title, "links": &c.links, "ref": &c.ref} {
		expr := map[string]string{"where": m.Where, "each": m.Each, "key": m.Key, "title": m.Title, "links": m.Links}[name]
		if name == "ref" && m.Ref != nil {
			expr = m.Ref.Key
		}
		if *at, err = compile(expr, &c.reads); err != nil {
			return nil, fmt.Errorf("mapping %s/%s %s: %w", m.Blueprint, m.Kind, name, err)
		}
	}
	for name, expr := range m.Fields {
		if c.fields[name], err = compile(expr, &c.reads); err != nil {
			return nil, fmt.Errorf("mapping %s/%s field %s: %w", m.Blueprint, m.Kind, name, err)
		}
	}
	for name, j := range m.Join {
		if j.Blueprint == "" {
			return nil, fmt.Errorf("mapping %s/%s join %s needs a blueprint", m.Blueprint, m.Kind, name)
		}
		if c.joinKeys[name], err = compile(j.Key, &c.reads); err != nil {
			return nil, fmt.Errorf("mapping %s/%s join %s key: %w", m.Blueprint, m.Kind, name, err)
		}
	}
	if m.Kind == WaiverKind && m.Each != "" {
		return nil, fmt.Errorf("mapping %s: a waiver list has no each", m.Blueprint)
	}
	return c, nil
}

// fact is the name of the fact the mapping writes: the kind unless named.
func (c *compiled) fact() string {
	if c.Fact != "" {
		return c.Fact
	}
	return c.Kind
}

// entityOf is a record as an expression reads it. A field the catalog
// holds no value for is absent, so `.?field.orValue(x)` gives x for it.
func entityOf(r Record) map[string]any {
	fields := map[string]any{}
	for name := range r.Fields {
		if v := r.Field(name); v != nil {
			fields[name] = v
		}
	}
	rels := map[string]any{}
	for name, refs := range r.Relations {
		list := make([]any, 0, len(refs))
		for _, t := range refs {
			list = append(list, map[string]any{"blueprint": t.Blueprint, "key": t.Key, "title": t.Title})
		}
		rels[name] = list
	}
	return map[string]any{"key": r.Key, "title": r.Title, "blueprint": r.Blueprint, "fields": fields, "relations": rels}
}

func eval(p cel.Program, act map[string]any) (any, error) {
	out, _, err := p.Eval(act)
	if err != nil {
		return nil, err
	}
	if types.IsUnknownOrError(out) {
		return nil, fmt.Errorf("%v", out)
	}
	if out == types.NullValue {
		return nil, nil
	}
	native, err := out.ConvertToNative(reflect.TypeOf((*any)(nil)).Elem())
	if err != nil {
		return nil, err
	}
	return estate.Plain(native)
}

// joined builds each join's lookup.
func (c *compiled) joined(lists Records) (map[string]any, error) {
	out := map[string]any{}
	for name, j := range c.Join {
		if _, read := lists[j.Blueprint]; !read {
			return nil, errJoinUnread{name: name, blueprint: j.Blueprint}
		}
		byKey := map[string]any{}
		for _, r := range lists[j.Blueprint] {
			ent := entityOf(r)
			key := r.Key
			if p := c.joinKeys[name]; p != nil {
				v, err := eval(p, map[string]any{"entity": ent, "item": nil, "joins": map[string]any{}})
				if err != nil {
					return nil, fmt.Errorf("join %s key on %s: %w", name, r.Key, err)
				}
				s, _ := v.(string)
				if s == "" {
					continue
				}
				key = s
			}
			if j.Many {
				list, _ := byKey[key].([]any)
				byKey[key] = append(list, ent)
			} else {
				byKey[key] = ent
			}
		}
		out[name] = byKey
	}
	return out, nil
}

// errJoinUnread says a list a mapping joins was not read: the mapping
// writes nothing, and what it would give is a gap.
type errJoinUnread struct{ name, blueprint string }

func (e errJoinUnread) Error() string {
	return fmt.Sprintf("join %s: %s was not read", e.name, e.blueprint)
}

// Result is what a mapping wrote, for the log; skipped are entities an
// expression could not read, with why.
type Result struct {
	Subjects int
	Skipped  []string
}

// Apply writes the mapping's subjects, or its waivers, from the lists read.
// An entity whose key cannot be computed is skipped and named; one whose
// field cannot be computed keeps that field unknown.
func (c *compiled) Apply(e *estate.Writer, lists Records) (Result, []estate.Waiver, error) {
	var res Result
	joins, err := c.joined(lists)
	if err != nil {
		return res, nil, err
	}
	var waivers []estate.Waiver
	for _, r := range lists[c.Blueprint] {
		ent := entityOf(r)
		act := map[string]any{"entity": ent, "item": nil, "joins": joins}
		if c.where != nil {
			v, err := eval(c.where, act)
			if err != nil {
				res.Skipped = append(res.Skipped, r.Key+": where: "+err.Error())
				continue
			}
			if keep, _ := v.(bool); !keep {
				continue
			}
		}
		items := []any{nil}
		if c.each != nil {
			v, err := eval(c.each, act)
			if err != nil {
				res.Skipped = append(res.Skipped, r.Key+": each: "+err.Error())
				continue
			}
			items, _ = v.([]any)
		}
		for _, item := range items {
			act := map[string]any{"entity": ent, "item": item, "joins": joins}
			fields, err := c.fieldsOf(act, ent)
			if err != nil {
				res.Skipped = append(res.Skipped, r.Key+": "+err.Error())
				continue
			}
			if c.Kind == WaiverKind {
				waivers = append(waivers, waiverOf(fields))
				continue
			}
			key, title := r.Key, r.Title
			if c.key != nil {
				v, err := eval(c.key, act)
				s, _ := v.(string)
				if err != nil || s == "" {
					res.Skipped = append(res.Skipped, r.Key+": no key")
					continue
				}
				key = s
			}
			if c.title != nil {
				if v, err := eval(c.title, act); err == nil {
					if s, ok := v.(string); ok {
						title = s
					}
				}
			}
			s := e.Subject(c.Kind, key, title)
			if c.ref != nil {
				v, err := eval(c.ref, act)
				if rk, _ := v.(string); err == nil && rk != "" {
					s.SetRef(estate.Ref{Blueprint: c.Ref.Blueprint, Key: rk})
				} else {
					res.Skipped = append(res.Skipped, r.Key+": no ref key")
				}
			} else if c.Link == nil || *c.Link {
				if s.Ref == nil {
					s.SetRef(estate.Ref{Blueprint: r.Blueprint, Key: r.Key})
				}
			}
			if c.links != nil {
				v, err := eval(c.links, act)
				if err != nil {
					res.Skipped = append(res.Skipped, r.Key+": links: "+err.Error())
				}
				list, _ := v.([]any)
				for _, l := range list {
					m, _ := l.(map[string]any)
					kind, _ := m["kind"].(string)
					lk, _ := m["key"].(string)
					if kind != "" && lk != "" {
						s.Link(kind, lk)
					}
				}
			}
			fact := c.fact()
			have, _ := s.Value(fact)
			if err := s.Set(fact, merged(have, fields)); err != nil {
				return res, nil, err
			}
			res.Subjects++
		}
	}
	return res, waivers, nil
}

// fieldsOf computes the fact. With no fields, it is every field of the
// entity and its relations, as they are. A field whose expression fails,
// or reads something the entity lacks, is unknown: absent, so a rule that
// needs it says no data.
func (c *compiled) fieldsOf(act map[string]any, ent map[string]any) (map[string]any, error) {
	if len(c.fields) == 0 {
		out := map[string]any{}
		for k, v := range ent["fields"].(map[string]any) {
			out[k] = v
		}
		out["relations"] = ent["relations"]
		return out, nil
	}
	out := map[string]any{}
	names := make([]string, 0, len(c.fields))
	for name := range c.fields {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		v, err := eval(c.fields[name], act)
		if err != nil || v == nil {
			continue
		}
		out[name] = v
	}
	return out, nil
}

// merged adds fields to a fact another mapping already wrote on the same
// subject, so two lists can fill one fact.
func merged(have any, add map[string]any) map[string]any {
	out := map[string]any{}
	if m, ok := have.(map[string]any); ok {
		for k, v := range m {
			out[k] = v
		}
	}
	for k, v := range add {
		out[k] = v
	}
	return out
}

func waiverOf(f map[string]any) estate.Waiver {
	str := func(k string) string { s, _ := f[k].(string); return s }
	return estate.Waiver{Rule: str("rule"), Subject: str("subject"), Reason: str("reason"), Owner: str("owner"), Category: estate.Category(str("category")), Control: str("control"), Match: str("match"), GrantedAt: parseTime(str("granted_at")), ExpiresAt: parseTime(str("expires_at")), ReviewAt: parseTime(str("review_at"))}
}

func parseTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}
	}
	return t
}
