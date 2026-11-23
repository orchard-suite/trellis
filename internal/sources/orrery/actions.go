package orrery

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
)

// Action is a self-service action's access as the hub answers it at
// /v1/actions/{id}: who may run it, whether a run waits, and the tool
// tiers that come with it. The token must see every blueprint the action
// runs on.
type Action struct {
	ID       string          `json:"id"`
	Approval json.RawMessage `json:"approval"`
	Access   *struct {
		Everyone  bool              `json:"everyone"`
		Grants    []json.RawMessage `json:"grants"`
		Audiences []string          `json:"audiences"`
		// Home is true, or the list of ways of owning the action's home.
		Home  json.RawMessage `json:"home"`
		Rules []ActionRule    `json:"rules"`
		Tiers []string        `json:"tiers"`
	} `json:"access"`
}

// ActionRule is one way a person may run an action on its entities.
type ActionRule struct {
	Audiences []string          `json:"audiences"`
	Grants    []json.RawMessage `json:"grants"`
	Owns      json.RawMessage   `json:"owns"`
	Where     json.RawMessage   `json:"where"`
	// Approval is false, true, or "self".
	Approval json.RawMessage `json:"approval"`
}

// Action reads one action.
func (c *Client) Action(ctx context.Context, id string) (Action, error) {
	var a Action
	err := c.do(ctx, http.MethodGet, "/v1/actions/"+url.PathEscape(id), nil, &a)
	return a, err
}

// ActionIDs are the ids of every action a posture lists, whoever may run it.
func (p Posture) ActionIDs() map[string]bool {
	out := map[string]bool{}
	for _, it := range p.Items {
		if it.Target.Kind == "action" {
			out[it.Target.ID] = true
		}
	}
	return out
}

// HubSource is a source as the hub lists it at /v1/sources, with the rule
// that says which tools a runner lends its identity to.
type HubSource struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Class  string `json:"class"`
	Serves *struct {
		Tiers   []string `json:"tiers"`
		Sources []string `json:"sources"`
		On      *struct {
			Blueprint string `json:"blueprint"`
		} `json:"on"`
	} `json:"serves"`
}

// Sources lists the hub's sources.
func (c *Client) Sources(ctx context.Context) ([]HubSource, error) {
	var out struct {
		Sources []HubSource `json:"sources"`
	}
	err := c.do(ctx, http.MethodGet, "/v1/sources", nil, &out)
	return out.Sources, err
}
