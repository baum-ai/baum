package tilechecks

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"
)

// The most a request body may hold: a report's problems and runs, and a
// preflight's data (64 KiB) with its citations' payloads, whose excerpts the
// page caps at 384 KiB.
const (
	maxReportBody    = 1 << 20
	maxPreflightBody = 2 << 20
)

// Handler serves the check runner's routes and the page's preflight, behind
// the app server's Host, session (cookie or bearer), Origin and JSON
// checks:
//
//   - GET /api/tile-checks/next?wait=<seconds> claims the next job (Next),
//     waiting up to wait seconds (at most 25, the default): 200 with the
//     job, or 204 when none came.
//   - PUT /api/tile-checks/{id}/result ends the running job id with the
//     runner's report: 204, or 404 when no such job is running. A report
//     marked abandoned (the runner's page went away) ends it unavailable.
//   - POST /api/tile-checks/{id}/alive says the runner is still drawing the
//     running job id (Alive), which keeps it from being a hang: 204, or 404.
//   - POST /api/tile-checks/preflight runs a first-show preflight
//     (Preflight) and answers its result.
//
// A browser request whose Sec-Fetch-Site is anything but same-origin is
// refused: claiming registers a frame document, so only the app itself may
// do it. Clients that send no fetch metadata (bearer API clients) pass.
func (c *Checker) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/tile-checks/next", c.serveNext)
	mux.HandleFunc("PUT /api/tile-checks/{id}/result", c.serveResult)
	mux.HandleFunc("POST /api/tile-checks/{id}/alive", c.serveAlive)
	mux.HandleFunc("POST /api/tile-checks/preflight", c.servePreflight)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" {
			http.Error(w, "same-origin request required", http.StatusForbidden)
			return
		}
		mux.ServeHTTP(w, r)
	})
}

func (c *Checker) serveNext(w http.ResponseWriter, r *http.Request) {
	wait := defaultLongest
	if raw := r.URL.Query().Get("wait"); raw != "" {
		seconds, err := strconv.Atoi(raw)
		if err != nil || seconds < 0 {
			http.Error(w, "wait must be a nonnegative number of seconds", http.StatusBadRequest)
			return
		}
		wait = min(time.Duration(seconds)*time.Second, defaultLongest)
	}
	job, ok := c.Next(r.Context(), wait)
	if !ok {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	// A client that went away while it waited (a reload, an aborted poll)
	// never gets the job: it goes back to the head of the queue rather
	// than hang.
	if r.Context().Err() != nil {
		c.Requeue(job.ID)
		return
	}
	body, err := json.Marshal(job)
	if err != nil {
		c.Requeue(job.ID)
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if _, err := w.Write(append(body, '\n')); err != nil {
		c.Requeue(job.ID)
	}
}

func (c *Checker) serveAlive(w http.ResponseWriter, r *http.Request) {
	if err := c.Alive(r.PathValue("id")); err != nil {
		http.Error(w, "no such running check", http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (c *Checker) serveResult(w http.ResponseWriter, r *http.Request) {
	var report Report
	if !decode(w, r, maxReportBody, &report) {
		return
	}
	if len(report.Problems) > maxProblems*4 {
		report.Problems = report.Problems[:maxProblems*4]
	}
	if err := c.Report(r.PathValue("id"), report); err != nil {
		http.Error(w, "no such running check", http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (c *Checker) servePreflight(w http.ResponseWriter, r *http.Request) {
	var req PreflightRequest
	if !decode(w, r, maxPreflightBody, &req) {
		return
	}
	if !digestPattern(req.Digest) || len(req.Data) == 0 {
		http.Error(w, "a digest and data are required", http.StatusBadRequest)
		return
	}
	result, err := c.Preflight(r.Context(), req)
	switch {
	case errors.Is(err, ErrBusy):
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	case err != nil:
		if r.Context().Err() == nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
		}
		return
	}
	writeJSON(w, result)
}

// decode reads one JSON value of at most limit bytes into v, answering 400
// for anything else.
func decode(w http.ResponseWriter, r *http.Request, limit int64, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	d := json.NewDecoder(r.Body)
	if d.Decode(v) != nil || d.Decode(new(any)) != io.EOF {
		http.Error(w, "invalid JSON request", http.StatusBadRequest)
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// digestPattern reports whether digest is a definition digest as the host
// writes it.
func digestPattern(digest string) bool {
	if len(digest) != len("sha256:")+64 || digest[:7] != "sha256:" {
		return false
	}
	for _, b := range []byte(digest[7:]) {
		if (b < '0' || b > '9') && (b < 'a' || b > 'f') {
			return false
		}
	}
	return true
}
