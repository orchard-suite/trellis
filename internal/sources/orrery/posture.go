package orrery

import (
	"context"
	"net/http"
	"net/url"
	"strings"

	"github.com/orchard-suite/trellis/internal/estate"
)

// Posture is what the hub says about its own access rules, read from
// /v1/posture with a token that holds posture.read: the facts the access
// pack judges, never the hub's own verdicts.
type Posture struct {
	Principals []struct {
		Kind  string `json:"kind"`
		Name  string `json:"name"`
		Admin bool   `json:"admin"`
	} `json:"principals"`
	Items []struct {
		Target struct {
			Kind      string `json:"kind"`
			ID        string `json:"id"`
			Blueprint string `json:"blueprint"`
			Field     string `json:"field"`
		} `json:"target"`
		Title    string  `json:"title"`
		Group    *string `json:"group"`
		Open     bool    `json:"open"`
		Class    *string `json:"class"`
		Coverage *string `json:"coverage"`
	} `json:"items"`
	Findings []struct {
		ID string `json:"id"`
	} `json:"findings"`
	Audiences []struct {
		ID        string   `json:"id"`
		Title     string   `json:"title"`
		Members   []string `json:"members"`
		Strangers []string `json:"strangers"`
	} `json:"audiences"`
	Review []struct {
		Blueprint  string   `json:"blueprint"`
		Title      string   `json:"title"`
		CreatedAt  *string  `json:"createdAt"`
		ReviewedAt *string  `json:"reviewedAt"`
		NewFields  []string `json:"newFields"`
	} `json:"review"`
}

// Posture reads the hub's security posture.
func (c *Client) Posture(ctx context.Context) (Posture, error) {
	var p Posture
	err := c.do(ctx, http.MethodGet, "/v1/posture", nil, &p)
	return p, err
}

// PostureCounts is what ShapePosture added, for the log.
type PostureCounts struct{ Blueprints, Audiences int }

// ShapePosture is a pure function from the hub's posture to subjects: one
// access_hub for the hub, one access_blueprint for each blueprint, and one
// access_audience for each audience. hub names the hub, such as its host.
func ShapePosture(e *estate.Writer, hub string, p Posture) PostureCounts {
	var c PostureCounts
	var adminTokens []string
	for _, pr := range p.Principals {
		if pr.Kind == "token" && pr.Admin {
			adminTokens = append(adminTokens, pr.Name)
		}
	}
	coverage := map[string]any{"classified": 0, "ruled": 0, "reviewed": 0, "unreviewed": 0}
	open := map[string][]string{}
	for _, it := range p.Items {
		if it.Target.Kind != "field" {
			continue
		}
		if it.Coverage != nil {
			coverage[*it.Coverage] = coverage[*it.Coverage].(int) + 1
		}
		// Open to whoever sees its blueprint: no class and no rule of its own.
		if it.Open {
			open[it.Target.Blueprint] = append(open[it.Target.Blueprint], it.Target.Field)
		}
	}
	nobody := map[string][]string{}
	for _, f := range p.Findings {
		ref, ok := strings.CutPrefix(f.ID, "nobody:field:")
		if !ok {
			continue
		}
		if bp, field, ok := strings.Cut(ref, "."); ok {
			nobody[bp] = append(nobody[bp], field)
		}
	}
	h := e.Subject("access_hub", hub, hub)
	_ = h.Set("posture", map[string]any{"admin_tokens": nonNil(adminTokens), "coverage": coverage})

	groups := map[string]string{}
	opens := map[string]bool{}
	for _, it := range p.Items {
		if it.Target.Kind == "blueprint" {
			if it.Group != nil {
				groups[it.Target.ID] = *it.Group
			}
			opens[it.Target.ID] = it.Open
		}
	}
	for _, r := range p.Review {
		s := e.Subject("access_blueprint", hub+":"+r.Blueprint, r.Title)
		_ = s.Set("access", map[string]any{
			"hub":         hub,
			"blueprint":   r.Blueprint,
			"group":       groups[r.Blueprint],
			"open":        opens[r.Blueprint],
			"created_at":  strOrNil(r.CreatedAt),
			"reviewed_at": strOrNil(r.ReviewedAt),
			"new_fields":  nonNil(r.NewFields),
			"open_fields": nonNil(open[r.Blueprint]),
			"nobody":      nonNil(nobody[r.Blueprint]),
		})
		c.Blueprints++
	}
	for _, a := range p.Audiences {
		s := e.Subject("access_audience", hub+":"+a.ID, a.Title)
		_ = s.Set("audience", map[string]any{"hub": hub, "id": a.ID, "members": len(a.Members), "strangers": nonNil(a.Strangers)})
		c.Audiences++
	}
	return c
}

// HubName is the host of the hub's URL, how its posture subjects are keyed.
func HubName(base string) string {
	u, err := url.Parse(base)
	if err != nil || u.Host == "" {
		return base
	}
	return u.Host
}

func strOrNil(s *string) any {
	if s == nil || *s == "" {
		return nil
	}
	return *s
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
