package orrery

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestClientReadsAnActionAndTheSources(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/actions/desk_lane":
			_, _ = io.WriteString(w, `{"id":"desk_lane","approval":true,"access":{"everyone":true,"rules":[{"owns":true,"approval":"self"}],"tiers":["read"]}}`)
		case "/v1/sources":
			_, _ = io.WriteString(w, `{"sources":[{"id":"reader","class":"runner","serves":{"tiers":["read"],"sources":["desk"],"on":{"blueprint":"repository"}}},{"id":"ui","class":"ui"}]}`)
		default:
			http.Error(w, `{"error":"Unknown action"}`, http.StatusNotFound)
		}
	}))
	defer srv.Close()
	c := NewClient(srv.URL, "tok", slog.New(slog.NewTextHandler(io.Discard, nil)))
	a, err := c.Action(context.Background(), "desk_lane")
	if err != nil {
		t.Fatal(err)
	}
	if a.Access == nil || !a.Access.Everyone || len(a.Access.Rules) != 1 || string(a.Access.Rules[0].Approval) != `"self"` || string(a.Approval) != "true" || len(a.Access.Tiers) != 1 {
		t.Errorf("action: %+v", a)
	}
	if _, err := c.Action(context.Background(), "gone"); err == nil {
		t.Error("an unknown action was read")
	}
	list, err := c.Sources(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].Serves == nil || list[0].Serves.On == nil || list[0].Serves.On.Blueprint != "repository" || list[1].Serves != nil {
		t.Errorf("sources: %+v", list)
	}
}

func TestPostureActionIDs(t *testing.T) {
	var p Posture
	p.Items = make([]struct {
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
	}, 2)
	p.Items[0].Target.Kind, p.Items[0].Target.ID = "action", "desk_lane"
	p.Items[1].Target.Kind, p.Items[1].Target.ID = "blueprint", "repository"
	if ids := p.ActionIDs(); len(ids) != 1 || !ids["desk_lane"] {
		t.Errorf("ids: %v", ids)
	}
}
