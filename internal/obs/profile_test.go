package obs

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestProfilerServesTheHeap(t *testing.T) {
	srv := httptest.NewServer(Profiler())
	defer srv.Close()
	for path, want := range map[string]int{"/debug/pprof/heap": http.StatusOK, "/debug/pprof/goroutine?debug=1": http.StatusOK, "/": http.StatusNotFound} {
		res, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != want {
			t.Errorf("%s answered %d, want %d", path, res.StatusCode, want)
		}
	}
}
