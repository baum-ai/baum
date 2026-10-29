package jobs

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strconv"

	"github.com/baum-ai/baum/internal/reviewgroups"
)

func (q *Queue) Handler() http.Handler {
	mux := http.NewServeMux()
	respond := func(w http.ResponseWriter, v any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(v)
	}
	decode := func(w http.ResponseWriter, r *http.Request, v any) bool {
		r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
		d := json.NewDecoder(r.Body)
		d.DisallowUnknownFields()
		if e := d.Decode(v); e != nil {
			http.Error(w, "invalid JSON request", 400)
			return false
		}
		if e := d.Decode(new(any)); e != io.EOF {
			http.Error(w, "one JSON request required", 400)
			return false
		}
		return true
	}
	// needsClone answers like isolation_required: a coded conflict the app
	// can act on, here by asking for a local clone first.
	needsClone := func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(map[string]any{ReasonNeedsClone: true})
	}
	// groupRefusal answers a group job refused before it was queued: members
	// without a saved review (409, naming them) or a group that does not
	// exist (404). It reports whether it answered.
	groupRefusal := func(w http.ResponseWriter, e error) bool {
		var missing *MembersNeedReviewsError
		switch {
		case errors.As(e, &missing):
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusConflict)
			_ = json.NewEncoder(w).Encode(map[string]any{"members_need_reviews": missing.Numbers})
		case errors.Is(e, reviewgroups.ErrNotFound):
			http.Error(w, "review group not found", http.StatusNotFound)
		default:
			return false
		}
		return true
	}
	mux.HandleFunc("GET /api/jobs", func(w http.ResponseWriter, r *http.Request) {
		respond(w, map[string]any{"available": true, "jobs": q.List()})
	})
	// POST /api/jobs starts what the person asked for at once: reviews, a
	// group's review or a fix. Only a fork or an author without known write
	// access waits for their confirmation (confirm_isolation); nothing waits
	// for a price.
	mux.HandleFunc("POST /api/jobs", func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Items            []Item `json:"items"`
			Mode             string `json:"mode"`
			ConfirmIsolation bool   `json:"confirm_isolation"`
		}
		if !decode(w, r, &request) {
			return
		}
		v, e := q.Generate(r.Context(), request.Items, request.Mode, request.ConfirmIsolation)
		var blocked *BlockedError
		switch {
		case errors.Is(e, ErrIsolationRequired):
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusConflict)
			_ = json.NewEncoder(w).Encode(map[string]any{"isolation_required": true})
			return
		case errors.As(e, &blocked):
			// The app says why in words where the person clicked.
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusConflict)
			_ = json.NewEncoder(w).Encode(map[string]any{"blocked_reason": blocked.Reason})
			return
		case errors.Is(e, ErrRestarting):
			w.Header().Set("Retry-After", "5")
			http.Error(w, ErrRestarting.Error(), http.StatusServiceUnavailable)
			return
		case errors.Is(e, ErrNeedsClone):
			needsClone(w)
			return
		}
		if groupRefusal(w, e) {
			return
		}
		if e != nil {
			message := "cannot generate: GitHub, the local clone or the agent is unavailable, the pull request changed, or a job is already running"
			if len(request.Items) == 1 && request.Items[0].Kind == KindFix {
				message = "cannot start the fix: GitHub, the local clone or the agent is unavailable, the pull request changed, or a fix is already running"
			}
			http.Error(w, message, http.StatusConflict)
			return
		}
		respond(w, map[string]any{"jobs": v})
	})
	mux.HandleFunc("POST /api/jobs/{id}/cancel", func(w http.ResponseWriter, r *http.Request) {
		var request struct{}
		if !decode(w, r, &request) {
			return
		}
		v, e := q.Cancel(r.PathValue("id"))
		if e != nil {
			status := 500
			if errors.Is(e, os.ErrNotExist) {
				status = 404
			}
			http.Error(w, http.StatusText(status), status)
			return
		}
		respond(w, v)
	})
	// transcript serves a fix job's transcript from entry after on, read
	// fresh from its file.
	mux.HandleFunc("GET /api/jobs/{id}/transcript", func(w http.ResponseWriter, r *http.Request) {
		after := 0
		if v := r.URL.Query().Get("after"); v != "" {
			n, e := strconv.Atoi(v)
			if e != nil || n < 0 {
				http.Error(w, "invalid after", 400)
				return
			}
			after = n
		}
		var job Job
		found := false
		for _, j := range q.List() {
			if j.ID == r.PathValue("id") {
				job, found = j, true
			}
		}
		if !found || !job.HasTranscript {
			http.Error(w, http.StatusText(404), 404)
			return
		}
		lines, more, e := readTranscript(transcriptPath(q.transcripts, job.ID), after)
		if errors.Is(e, os.ErrNotExist) {
			http.Error(w, http.StatusText(404), 404)
			return
		}
		if e != nil {
			http.Error(w, http.StatusText(500), 500)
			return
		}
		next := after
		if len(lines) > 0 {
			next = lines[len(lines)-1].Seq
		}
		if lines == nil {
			lines = []transcriptLine{}
		}
		respond(w, map[string]any{"entries": lines, "next": next, "more": more, "done": job.State != "queued" && job.State != "running"})
	})
	return mux
}
