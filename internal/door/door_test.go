package door

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGateReadsTheConfiguredHeaders(t *testing.T) {
	g, err := New(Config{EmailHeader: "X-Auth-Email", GroupsHeader: "X-Auth-Roles", Roles: []string{"Viewer"}}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	var who Person
	h := g.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { who = From(r.Context()) }))
	ask := func(email, roles string) int {
		req := httptest.NewRequest("GET", "/", nil)
		req.Header.Set("X-Auth-Email", email)
		req.Header.Set("X-Auth-Roles", roles)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	if code := ask("", "Viewer"); code != http.StatusUnauthorized {
		t.Errorf("no identity: %d", code)
	}
	if code := ask("person@example.com", "Other"); code != http.StatusForbidden {
		t.Errorf("wrong role: %d", code)
	}
	if code := ask("person@example.com", "Other, Viewer"); code != http.StatusOK || who.Email != "person@example.com" {
		t.Errorf("admitted: %d %+v", code, who)
	}
	open, _ := New(Config{Mode: "open"}, slog.New(slog.DiscardHandler))
	rec := httptest.NewRecorder()
	open.Wrap(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})).ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("open mode admits anyone: %d", rec.Code)
	}
}
