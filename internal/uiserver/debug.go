package uiserver

import (
	"net/http"
	"net/http/pprof"
	"strings"

	"github.com/baum-ai/baum/internal/workstats"
)

// debugStats answers with the process's work counters and resource use
// (workstats.Take), for "baum daemon profile". Like every /api route it
// needs the launch credential.
func debugStats() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, workstats.Take())
	})
}

// debugProfiles serves Go's runtime profiles under /api/debug/pprof/ as
// net/http/pprof serves them under /debug/pprof/: the index, named profiles
// such as heap and goroutine, a CPU profile (profile?seconds=N) and an
// execution trace (trace?seconds=N). They cost nothing until requested.
func debugProfiles() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name, _ := strings.CutPrefix(r.URL.Path, "/api/debug/pprof/")
		// The pprof handlers read the profile name from /debug/pprof/<name>.
		r = r.Clone(r.Context())
		r.URL.Path = "/debug/pprof/" + name
		switch name {
		case "profile":
			pprof.Profile(w, r)
		case "trace":
			pprof.Trace(w, r)
		case "symbol":
			pprof.Symbol(w, r)
		case "cmdline":
			http.NotFound(w, r)
		default:
			pprof.Index(w, r)
		}
	})
}
