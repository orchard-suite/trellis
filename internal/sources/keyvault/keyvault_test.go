package keyvault

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/orchard-suite/trellis/internal/estate"
)

func TestCollectPages(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer kv" {
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		if r.URL.Query().Get("$skiptoken") == "" {
			_, _ = io.WriteString(w, `{"value":[{"id":"`+srv.URL+`/secrets/api-key","contentType":"Token: the partner API","attributes":{"enabled":true,"updated":1700000000}}],"nextLink":"`+srv.URL+`/secrets?api-version=7.4&$skiptoken=x"}`)
			return
		}
		_, _ = io.WriteString(w, `{"value":[{"id":"`+srv.URL+`/secrets/old-cert","attributes":{"enabled":false,"updated":1600000000}}]}`)
	}))
	defer srv.Close()
	e := estate.New(time.Now())
	p := New(srv.URL, func(context.Context) (string, error) { return "kv", nil }, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := p.Collect(context.Background(), e.Writer("test")); err != nil {
		t.Fatal(err)
	}
	if n := len(e.Of("secret")); n != 2 {
		t.Fatalf("secrets: %d", n)
	}
	s, _ := e.Find("secret", p.vault+"/api-key")
	f := s.Facts["secret"].Value.(map[string]any)
	if f["enabled"] != true || f["updated"] != time.Unix(1700000000, 0).UTC().Format(time.RFC3339) || f["name"] != "api-key" || f["type"] != "token" || f["content_type"] != "Token: the partner API" {
		t.Errorf("api-key: %+v", f)
	}
	old, _ := e.Find("secret", p.vault+"/old-cert")
	if f := old.Facts["secret"].Value.(map[string]any); f["enabled"] != false || f["type"] != "" {
		t.Errorf("old-cert: %+v", old.Facts)
	}
}

func TestVaultName(t *testing.T) {
	p := New("https://example.vault.azure.net/", nil, nil)
	if p.vault != "example" || p.vaultURL != "https://example.vault.azure.net" {
		t.Errorf("%q %q", p.vault, p.vaultURL)
	}
}
