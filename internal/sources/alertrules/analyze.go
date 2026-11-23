package alertrules

import (
	"sort"

	"github.com/prometheus/prometheus/model/labels"
	"github.com/prometheus/prometheus/promql/parser"
)

// Analysis is what a rule's query says about the alerts it raises.
type Analysis struct {
	// Selectors are the query's series selectors, each with what the store
	// knows of it.
	Selectors []SelectorUse
	// Keeps are the labels every alert the query raises carries; MayKeep
	// the labels some of them may carry.
	Keeps, MayKeep []string
}

// SelectorUse is one series selector of a query. An optional one only
// takes series away, on the right of unless, so the query works the same
// when it selects nothing.
type SelectorUse struct {
	Text     string
	Optional bool
	Selector
}

// set is a set of label names.
type set map[string]bool

// out is the labels of an expression's result: on every series (always)
// and on some (maybe).
type out struct{ always, maybe set }

// Analyze parses a query and works out, from the labels each selector's
// series carry, which labels its result keeps. look reads one selector.
func Analyze(query string, look func(string) Selector) (Analysis, error) {
	expr, err := parser.NewParser(parser.Options{}).ParseExpr(query)
	if err != nil {
		return Analysis{}, err
	}
	a := &analyzer{look: look, seen: map[string]Selector{}, needed: map[string]bool{}}
	res := a.expr(expr)
	var an Analysis
	for _, text := range a.order {
		an.Selectors = append(an.Selectors, SelectorUse{Text: text, Optional: !a.needed[text], Selector: a.seen[text]})
	}
	an.Keeps, an.MayKeep = sorted(res.always), sorted(res.maybe)
	return an, nil
}

type analyzer struct {
	look  func(string) Selector
	seen  map[string]Selector
	order []string
	// needed holds the selectors met outside an unless's right side.
	needed map[string]bool
	// optional counts the unless right sides the walk is inside.
	optional int
}

// selector reads a vector selector once, without its offset or @, so the
// store is asked about the series themselves.
func (a *analyzer) selector(vs *parser.VectorSelector) out {
	bare := &parser.VectorSelector{Name: vs.Name, LabelMatchers: vs.LabelMatchers}
	text := bare.String()
	s, ok := a.seen[text]
	if !ok {
		s = a.look(text)
		a.seen[text] = s
		a.order = append(a.order, text)
	}
	if a.optional == 0 {
		a.needed[text] = true
	}
	names := set{}
	for _, l := range s.Labels {
		names[l] = true
	}
	// An equality matcher's label is on every series it selects, even
	// when the store has none yet.
	for _, m := range vs.LabelMatchers {
		if m.Type == labels.MatchEqual && m.Name != labels.MetricName && m.Value != "" {
			names[m.Name] = true
		}
	}
	return out{always: names, maybe: copySet(names)}
}

func (a *analyzer) expr(e parser.Expr) out {
	switch n := e.(type) {
	case *parser.VectorSelector:
		return a.selector(n)
	case *parser.MatrixSelector:
		return a.expr(n.VectorSelector)
	case *parser.ParenExpr:
		return a.expr(n.Expr)
	case *parser.UnaryExpr:
		return a.expr(n.Expr)
	case *parser.SubqueryExpr:
		return a.expr(n.Expr)
	case *parser.StepInvariantExpr:
		return a.expr(n.Expr)
	case *parser.AggregateExpr:
		return a.aggregate(n)
	case *parser.Call:
		return a.call(n)
	case *parser.BinaryExpr:
		return a.binary(n)
	}
	// A number, a string, or anything else carries no labels.
	return out{always: set{}, maybe: set{}}
}

func (a *analyzer) aggregate(n *parser.AggregateExpr) out {
	if n.Param != nil {
		a.expr(n.Param)
	}
	child := a.expr(n.Expr)
	switch n.Op {
	case parser.TOPK, parser.BOTTOMK, parser.LIMITK, parser.LIMIT_RATIO:
		// These pick series and keep their labels.
		return child
	}
	group := set{}
	for _, g := range n.Grouping {
		group[g] = true
	}
	var res out
	if n.Without {
		res = out{always: minus(child.always, group), maybe: minus(child.maybe, group)}
	} else {
		res = out{always: and(child.always, group), maybe: and(child.maybe, group)}
	}
	if n.Op == parser.COUNT_VALUES {
		if s, ok := n.Param.(*parser.StringLiteral); ok {
			res.always[s.Val], res.maybe[s.Val] = true, true
		}
	}
	return res
}

func (a *analyzer) call(n *parser.Call) out {
	var vec out
	found := false
	for _, arg := range n.Args {
		r := a.expr(arg)
		if !found && isVector(arg) {
			vec, found = r, true
		}
	}
	switch n.Func.Name {
	case "absent", "absent_over_time":
		// The one alert carries the equality matchers' labels, and nothing
		// else: there is no series to take labels from.
		names := set{}
		if len(n.Args) == 1 {
			for _, vs := range selectors(n.Args[0]) {
				for _, m := range vs.LabelMatchers {
					if m.Type == labels.MatchEqual && m.Name != labels.MetricName {
						names[m.Name] = true
					}
				}
			}
		}
		return out{always: names, maybe: copySet(names)}
	case "vector", "scalar", "time", "pi":
		return out{always: set{}, maybe: set{}}
	case "label_replace", "label_join":
		if len(n.Args) > 1 {
			if dst, ok := unwrapString(n.Args[1]); ok && found {
				vec.always[dst], vec.maybe[dst] = true, true
			}
		}
	case "histogram_quantile", "histogram_fraction":
		if found {
			delete(vec.always, "le")
			delete(vec.maybe, "le")
		}
	}
	if !found {
		return out{always: set{}, maybe: set{}}
	}
	return vec
}

func (a *analyzer) binary(n *parser.BinaryExpr) out {
	lhs := a.expr(n.LHS)
	if n.Op == parser.LUNLESS {
		a.optional++
	}
	rhs := a.expr(n.RHS)
	if n.Op == parser.LUNLESS {
		a.optional--
	}
	lvec, rvec := isVector(n.LHS), isVector(n.RHS)
	switch {
	case !lvec && !rvec:
		return out{always: set{}, maybe: set{}}
	case !rvec:
		return lhs
	case !lvec:
		return rhs
	}
	m := n.VectorMatching
	if m == nil {
		m = &parser.VectorMatching{Card: parser.CardOneToOne}
	}
	switch n.Op {
	case parser.LAND, parser.LUNLESS:
		return lhs
	case parser.LOR:
		return out{always: and(lhs.always, rhs.always), maybe: or(lhs.maybe, rhs.maybe)}
	}
	side := lhs
	if m.Card == parser.CardOneToMany {
		side = rhs
	}
	include := set{}
	for _, l := range m.Include {
		include[l] = true
	}
	// A comparison that filters keeps the side's series as they are.
	if n.Op.IsComparisonOperator() && !n.ReturnBool {
		return out{always: or(side.always, include), maybe: or(side.maybe, include)}
	}
	if m.Card == parser.CardManyToOne || m.Card == parser.CardOneToMany {
		return out{always: or(side.always, include), maybe: or(side.maybe, include)}
	}
	matching := set{}
	for _, l := range m.MatchingLabels {
		matching[l] = true
	}
	if m.On {
		return out{always: and(side.always, matching), maybe: and(side.maybe, matching)}
	}
	return out{always: minus(side.always, matching), maybe: minus(side.maybe, matching)}
}

// isVector says whether an expression yields an instant or range vector.
func isVector(e parser.Expr) bool {
	t := e.Type()
	return t == parser.ValueTypeVector || t == parser.ValueTypeMatrix
}

func selectors(e parser.Expr) []*parser.VectorSelector {
	var out []*parser.VectorSelector
	parser.Inspect(e, func(n parser.Node, _ []parser.Node) error {
		if vs, ok := n.(*parser.VectorSelector); ok {
			out = append(out, vs)
		}
		return nil
	})
	return out
}

func unwrapString(e parser.Expr) (string, bool) {
	for {
		switch n := e.(type) {
		case *parser.StringLiteral:
			return n.Val, true
		case *parser.ParenExpr:
			e = n.Expr
		default:
			return "", false
		}
	}
}

func copySet(s set) set {
	c := set{}
	for k := range s {
		c[k] = true
	}
	return c
}

func and(a, b set) set {
	c := set{}
	for k := range a {
		if b[k] {
			c[k] = true
		}
	}
	return c
}

func or(a, b set) set {
	c := copySet(a)
	for k := range b {
		c[k] = true
	}
	return c
}

func minus(a, b set) set {
	c := set{}
	for k := range a {
		if !b[k] {
			c[k] = true
		}
	}
	return c
}

func sorted(s set) []string {
	out := make([]string, 0, len(s))
	for k := range s {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
