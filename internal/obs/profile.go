package obs

import (
	"net/http"
	"net/http/pprof"
)

// Profiler serves Go's profiles of this process under /debug/pprof/: the
// heap, the goroutines, a CPU profile, a trace. A profile holds whatever
// the process holds, so it gets a port of its own that only an operator
// reaches.
func Profiler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /debug/pprof/", pprof.Index)
	mux.HandleFunc("GET /debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("GET /debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("GET /debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("GET /debug/pprof/trace", pprof.Trace)
	return mux
}
