package pack

import (
	"fmt"
	"math"
	"reflect"
	"sort"
	"strings"
	"time"

	"cel.dev/cel-go/cel"
	celast "cel.dev/cel-go/common/ast"
	"cel.dev/cel-go/common/types"
	"cel.dev/cel-go/common/types/ref"
	"cel.dev/cel-go/ext"
	"cel.dev/cel-go/interpreter"

	"github.com/orchard-suite/trellis/internal/estate"
)

// env is the one CEL environment every rule is written in. Expressions are
// parsed, not type-checked: facts are plain JSON data of whatever shape a
// source writes, so every fact is dynamic. Numbers from JSON are doubles;
// cross-type comparison lets a rule write `scan.cve_kev > 0`.
var env = func() *cel.Env {
	e, err := cel.NewEnv(
		cel.CrossTypeNumericComparisons(true),
		cel.OptionalTypes(),
		ext.Strings(),
		ext.Lists(),
		ext.Sets(),
		cel.Function("plural",
			cel.Overload("plural_double", []*cel.Type{cel.DoubleType, cel.StringType, cel.StringType}, cel.StringType,
				cel.FunctionBinding(func(args ...ref.Val) ref.Val {
					return types.String(plural(int64(math.Round(float64(args[0].(types.Double)))), string(args[1].(types.String)), string(args[2].(types.String))))
				})),
			cel.Overload("plural_int", []*cel.Type{cel.IntType, cel.StringType, cel.StringType}, cel.StringType,
				cel.FunctionBinding(func(args ...ref.Val) ref.Val {
					return types.String(plural(int64(args[0].(types.Int)), string(args[1].(types.String)), string(args[2].(types.String))))
				})),
		),
		cel.Function("days_since",
			cel.Overload("days_since_string", []*cel.Type{cel.StringType, cel.TimestampType}, cel.IntType,
				cel.BinaryBinding(func(at, now ref.Val) ref.Val {
					t, err := time.Parse(time.RFC3339Nano, string(at.(types.String)))
					if err != nil {
						return types.NewErr("days_since: %q is not an RFC 3339 time", string(at.(types.String)))
					}
					return types.Int(int64(now.(types.Timestamp).Time.Sub(t).Hours() / 24))
				})),
		),
	)
	if err != nil {
		panic(err)
	}
	return e
}()

var (
	anySliceType = reflect.TypeOf([]any{})
	mapType      = reflect.TypeOf(map[string]any{})
)

func plural(n int64, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, one)
	}
	return fmt.Sprintf("%d %s", n, many)
}

// program compiles an expression and names the variables it reads that
// a subject's facts would fill: every free identifier but the ones every
// rule sees.
func program(expr string) (cel.Program, map[string]bool, error) {
	parsed, iss := env.Parse(expr)
	if iss.Err() != nil {
		return nil, nil, iss.Err()
	}
	prog, err := env.Program(parsed)
	if err != nil {
		return nil, nil, err
	}
	idents, bound := map[string]bool{}, map[string]bool{}
	celast.PostOrderVisit(parsed.NativeRep().Expr(), celast.NewExprVisitor(func(e celast.Expr) {
		switch e.Kind() {
		case celast.IdentKind:
			idents[e.AsIdent()] = true
		case celast.ComprehensionKind:
			c := e.AsComprehension()
			bound[c.IterVar()] = true
			bound[c.AccuVar()] = true
			if c.HasIterVar2() {
				bound[c.IterVar2()] = true
			}
		}
	}))
	names := map[string]bool{}
	for id := range idents {
		if !bound[id] && !builtin[id] {
			names[id] = true
		}
	}
	return prog, names, nil
}

// builtin are the variables every expression sees beside the facts.
var builtin = map[string]bool{"subject": true, "params": true, "now": true, "linked": true}

// Status is what a rule said about one subject.
type Status string

const (
	Pass Status = "pass"
	Fail Status = "fail"
	// NoData: a fact the rule needs was not collected, so nothing is said.
	NoData Status = "nodata"
	// NotApplicable: the rule means nothing for this subject, or the
	// subject is outside the rule's scope.
	NotApplicable Status = "n/a"
)

// Result is a rule's answer about one subject.
type Result struct {
	Status   Status
	Detail   string
	Evidence []estate.Evidence
	// AsOf is when the oldest fact the rule read was seen; zero when it
	// read none the subject has.
	AsOf time.Time
	// Carried says a fact the rule read is from an earlier pass.
	Carried bool
}

// Prepared is one subject made ready to judge: its facts by name,
// subject, now, and linked, the facts of each linked subject by kind. It
// is built once and read by every rule judged over the subject.
type Prepared struct {
	Subject *estate.Subject
	vars    interpreter.Activation
}

// Prepare makes a subject ready to judge at now, with the subjects linked
// to it by kind.
func Prepare(s *estate.Subject, now time.Time, linked map[string][]*estate.Subject) *Prepared {
	byKind := map[string]any{}
	for kind, list := range linked {
		out := make([]any, 0, len(list))
		for _, t := range list {
			m := t.Values()
			m["subject"] = map[string]any{"kind": t.Kind, "key": t.Key, "title": t.Title}
			out = append(out, m)
		}
		byKind[kind] = out
	}
	vars := map[string]any{
		"subject": map[string]any{"kind": s.Kind, "key": s.Key, "title": s.Title},
		"now":     now,
		"linked":  byKind,
	}
	for name, f := range s.Facts {
		vars[name] = f.Value
	}
	act, _ := interpreter.NewActivation(vars)
	return &Prepared{Subject: s, vars: act}
}

// with is what an expression sees: the subject and a pack's parameters.
func (p *Prepared) with(params map[string]any) interpreter.Activation {
	act, _ := interpreter.NewActivation(map[string]any{"params": params})
	return interpreter.NewHierarchicalActivation(p.vars, act)
}

// Judge runs the rule over one subject with its pack's parameters, in
// order: a subject outside the rule's scope or a skip that holds is n/a, a
// missing need or an unknown that holds is no data, then the check. A rule
// whose expression cannot be evaluated answers no data with why, never a
// guess.
func (r *Rule) Judge(p *Prepared, params map[string]any) Result {
	res := r.judge(p.Subject, p.with(params))
	for _, name := range r.Reads {
		f, ok := p.Subject.Facts[name]
		if !ok {
			continue
		}
		if res.AsOf.IsZero() || f.ObservedAt.Before(res.AsOf) {
			res.AsOf = f.ObservedAt
		}
		if f.Carried {
			res.Carried = true
		}
	}
	return res
}

func (r *Rule) judge(s *estate.Subject, act interpreter.Activation) Result {
	// A scope or a skip that reads a fact the subject lacks says nothing;
	// the needs below answer for the missing fact.
	var scopeErr error
	if r.scope != nil {
		inside, err := evalBool(r.scope, act)
		if err == nil && !inside {
			return Result{Status: NotApplicable, Detail: "outside the " + r.Scope + " scope"}
		}
		scopeErr = err
	}
	for _, c := range r.Skip {
		if hit, err := evalBool(c.prog, act); err == nil && hit {
			return Result{Status: NotApplicable, Detail: c.words(act)}
		}
	}
	for _, path := range sortedKeys(r.Needs) {
		if !has(s, path) {
			detail := r.Needs[path]
			if m, ok := s.Missed[strings.SplitN(path, ".", 2)[0]]; ok {
				detail += ": " + m.Reason
			}
			return Result{Status: NoData, Detail: detail}
		}
	}
	if scopeErr != nil {
		return cannot(fmt.Errorf("scope %s: %w", r.Scope, scopeErr))
	}
	for _, c := range r.Unknown {
		hit, err := evalBool(c.prog, act)
		if err != nil {
			return cannot(err)
		}
		if hit {
			return Result{Status: NoData, Detail: c.words(act)}
		}
	}
	ok, err := evalBool(r.check, act)
	if err != nil {
		return cannot(err)
	}
	res := Result{Status: Pass}
	if !ok {
		res.Status = Fail
		out, _, err := r.detail.Eval(act)
		if err != nil {
			return cannot(err)
		}
		d, isString := out.Value().(string)
		if !isString {
			return cannot(fmt.Errorf("detail is a %s, not a string", out.Type().TypeName()))
		}
		res.Detail = d
	}
	if r.evidence != nil {
		if ev, err := evidence(r.evidence, act); err == nil {
			res.Evidence = ev
		}
	}
	return res
}

// words are what a person reads when the condition holds.
func (c *Condition) words(act interpreter.Activation) string {
	if c.explain == nil {
		return c.Because
	}
	out, _, err := c.explain.Eval(act)
	if err != nil {
		return c.Explain
	}
	if s, ok := out.Value().(string); ok {
		return s
	}
	return c.Explain
}

func cannot(err error) Result {
	return Result{Status: NoData, Detail: "cannot judge: " + err.Error()}
}

func evalBool(p cel.Program, act interpreter.Activation) (bool, error) {
	out, _, err := p.Eval(act)
	if err != nil {
		return false, err
	}
	b, ok := out.Value().(bool)
	if !ok {
		return false, fmt.Errorf("the expression gives a %s, not a bool", out.Type().TypeName())
	}
	return b, nil
}

func evidence(p cel.Program, act interpreter.Activation) ([]estate.Evidence, error) {
	out, _, err := p.Eval(act)
	if err != nil {
		return nil, err
	}
	native, err := out.ConvertToNative(anySliceType)
	if err != nil {
		return nil, err
	}
	var evs []estate.Evidence
	for _, item := range native.([]any) {
		m, _ := toMap(item)
		evs = append(evs, estate.Evidence{Source: str(m["source"]), Query: str(m["query"]), Got: str(m["got"])})
	}
	return evs, nil
}

// has says whether a dotted path leads to a non-null value in the
// subject's facts.
func has(s *estate.Subject, path string) bool {
	parts := strings.Split(path, ".")
	f, ok := s.Facts[parts[0]]
	if !ok || f.Value == nil {
		return false
	}
	at := f.Value
	for _, part := range parts[1:] {
		m, ok := toMap(at)
		if !ok {
			return false
		}
		v, ok := m[part]
		if !ok || v == nil {
			return false
		}
		at = v
	}
	return true
}

func toMap(v any) (map[string]any, bool) {
	switch m := v.(type) {
	case map[string]any:
		return m, true
	case ref.Val:
		native, err := m.ConvertToNative(mapType)
		if err != nil {
			return nil, false
		}
		out, ok := native.(map[string]any)
		return out, ok
	}
	return nil, false
}

func str(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	if v == nil {
		return ""
	}
	return fmt.Sprint(v)
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Expr is one CEL expression an estate writes outside a pack, such as who
// owns a subject, in the same language and over the same variables a rule
// sees.
type Expr struct {
	Source string
	prog   cel.Program
}

// NewExpr compiles an expression.
func NewExpr(src string) (*Expr, error) {
	prog, _, err := program(src)
	if err != nil {
		return nil, err
	}
	return &Expr{Source: src, prog: prog}, nil
}

// String evaluates the expression over a subject to a string; null or a
// missing fact is "".
func (e *Expr) String(p *Prepared) (string, error) {
	out, _, err := e.prog.Eval(p.with(nil))
	if err != nil {
		return "", err
	}
	switch v := out.Value().(type) {
	case string:
		return v, nil
	case nil:
		return "", nil
	default:
		if out.Type().TypeName() == "null_type" {
			return "", nil
		}
		return "", fmt.Errorf("gives a %s, not a string", out.Type().TypeName())
	}
}
