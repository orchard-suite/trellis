package openapi

import (
	"fmt"
	"sort"
	"strings"
)

// Breaking lists what head changes about base that a caller written against
// base would trip on: an operation gone, a parameter or a body now
// required, a field the body now requires, or a field a success response no
// longer returns. Retiring a whole major version is not a break once a
// newer one ships beside it, which is how a breaking change is made. Nor
// is moving an unversioned route under a version: /api/things becoming
// /v1/things is the same operation, compared as one.
func Breaking(base, head *Doc) []string {
	now := map[string]Operation{}
	moved := map[string]Operation{}
	versions := map[string]bool{}
	for _, o := range head.Operations {
		now[o.Key()] = o
		moved[unversioned(o)] = o
		versions[majorVersion(o.Path)] = true
	}
	var out []string
	for _, was := range base.Operations {
		is, ok := now[was.Key()]
		if !ok && majorVersion(was.Path) == "" {
			is, ok = moved[unversioned(was)]
		}
		if !ok {
			if v := majorVersion(was.Path); v != "" && !versions[v] && newer(versions, v) {
				continue
			}
			out = append(out, fmt.Sprintf("%s is gone", was.Name()))
			continue
		}
		before := map[string]Param{}
		for _, p := range was.Params {
			before[p.In+" "+p.Name] = p
		}
		for _, p := range is.Params {
			if !p.Required {
				continue
			}
			if old, ok := before[p.In+" "+p.Name]; !ok || !old.Required {
				out = append(out, fmt.Sprintf("%s now requires the %s parameter %s", is.Name(), p.In, p.Name))
			}
		}
		if is.Body.Required && !was.Body.Required {
			out = append(out, fmt.Sprintf("%s now requires a body", is.Name()))
		}
		for _, f := range missing(is.Body.Fields, was.Body.Fields) {
			out = append(out, fmt.Sprintf("%s now requires %s in its body", is.Name(), f))
		}
		if was.Success != nil && is.Success != nil {
			for _, f := range missing(was.Success.Fields, is.Success.Fields) {
				out = append(out, fmt.Sprintf("%s no longer returns %s", is.Name(), f))
			}
		}
	}
	sort.Strings(out)
	return out
}

// missing is what a has that b does not.
func missing(a, b []string) []string {
	have := map[string]bool{}
	for _, x := range b {
		have[x] = true
	}
	var out []string
	for _, x := range a {
		if !have[x] {
			out = append(out, x)
		}
	}
	return out
}

// newer says whether versions holds a major version above v.
func newer(versions map[string]bool, v string) bool {
	var n int
	fmt.Sscanf(v, "v%d", &n)
	for w := range versions {
		var m int
		if _, err := fmt.Sscanf(w, "v%d", &m); err == nil && m > n {
			return true
		}
	}
	return false
}

// unversioned is an operation's key with its version segment and a leading
// api segment left out, so /api/things and /v1/things are one key.
func unversioned(o Operation) string {
	var parts []string
	for i, seg := range strings.Split(paramName.ReplaceAllString(o.Path, "{}"), "/") {
		if versionSegment.MatchString(seg) || (i == 1 && seg == "api") {
			continue
		}
		parts = append(parts, seg)
	}
	return strings.ToUpper(o.Method) + " " + strings.Join(parts, "/")
}
