package command

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/orchard-suite/trellis/internal/estate"
)

func TestReadRecordsEveryFact(t *testing.T) {
	e := estate.New(time.Now())
	out := `{"kind":"repository","key":"github/example/app","title":"app","fact":"scorecard","value":{"score":7.5,"checks":["Branch-Protection"]}}

{"kind":"repository","key":"github/example/app","fact":"license","value":"MIT"}
`
	n, err := Read(e.Writer("test"), []byte(out))
	if err != nil || n != 2 {
		t.Fatalf("read %d: %v", n, err)
	}
	s, _ := e.Find("repository", "github/example/app")
	if s.Title != "app" || s.Facts["scorecard"].Value.(map[string]any)["score"] != 7.5 || s.Facts["license"].Value != "MIT" {
		t.Errorf("facts: %+v", s.Facts)
	}
	for _, bad := range []string{`{"kind":"repository","fact":"x","value":1}`, `not json`} {
		if _, err := Read(estate.New(time.Now()).Writer("test"), []byte(bad)); err == nil {
			t.Errorf("%q was read", bad)
		}
	}
}

func TestCollectRunsTheProgram(t *testing.T) {
	s, err := New([]string{"sh", "-c", `echo '{"kind":"host","key":"h","fact":"up","value":true}'`}, time.Minute, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	e := estate.New(time.Now())
	if err := s.Collect(context.Background(), e.Writer("test")); err != nil {
		t.Fatal(err)
	}
	if h, ok := e.Find("host", "h"); !ok || h.Facts["up"].Value != true {
		t.Errorf("host: %+v", h)
	}
	failing, _ := New([]string{"sh", "-c", "echo broken >&2; exit 3"}, time.Minute, slog.New(slog.DiscardHandler))
	if err := failing.Collect(context.Background(), estate.New(time.Now()).Writer("test")); err == nil {
		t.Error("a program that fails fails the run")
	}
}
