package azuregraph

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/orchard-suite/trellis/internal/azure"
	"github.com/orchard-suite/trellis/internal/estate"
)

func TestCollectWritesEachRow(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"data":[{"id":"/subscriptions/S/rg/a","name":"a","minimumTlsVersion":"TLS1_0"},{"name":"keyless"}]}`)
	}))
	defer srv.Close()
	api := azure.NewManagement(srv.URL, func(context.Context) (string, error) { return "tok", nil }, srv.Client())
	s, err := New(api, azure.Scope{Subscriptions: []string{"S"}}, []Query{{Kind: "storage_account", Query: "Resources | where type =~ 'microsoft.storage/storageaccounts'"}}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	e := estate.New(time.Now())
	if err := s.Collect(context.Background(), e.Writer("test")); err != nil {
		t.Fatal(err)
	}
	if len(e.Of("storage_account")) != 1 {
		t.Fatalf("subjects %d", len(e.Of("storage_account")))
	}
	a, _ := e.Find("storage_account", "/subscriptions/s/rg/a")
	if a.Title != "a" || a.Facts["azure"].Value.(map[string]any)["minimumTlsVersion"] != "TLS1_0" {
		t.Errorf("a: %+v", a)
	}
	if _, err := New(api, azure.Scope{}, []Query{{Kind: "x"}}, nil); err == nil {
		t.Error("a query without its text was taken")
	}
	if _, err := New(api, azure.Scope{}, []Query{{Kind: "x", Query: "Resources", Key: "tolower(id)"}}, nil); err == nil {
		t.Error("a key that is not a column's bare name was taken; the rows cannot be read in its order")
	}
	if _, err := New(api, azure.Scope{Subscriptions: []string{"S"}, ManagementGroups: []string{"M"}}, nil, nil); err == nil {
		t.Error("a scope naming both subscriptions and management groups was taken")
	}
}
