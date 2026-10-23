package jobs

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
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
	mux.HandleFunc("GET /api/jobs", func(w http.ResponseWriter, r *http.Request) {
		respond(w, map[string]any{"available": true, "jobs": q.List()})
	})
	mux.HandleFunc("POST /api/jobs/estimate", func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Items []Item `json:"items"`
			Mode  string `json:"mode"`
		}
		if !decode(w, r, &request) {
			return
		}
		v, e := q.Estimate(r.Context(), request.Items, request.Mode)
		if errors.Is(e, ErrNeedsClone) {
			needsClone(w)
			return
		}
		if e != nil {
			http.Error(w, "cannot estimate: select available PRs with an explicit priced model", 400)
			return
		}
		respond(w, v)
	})
	mux.HandleFunc("POST /api/jobs", func(w http.ResponseWriter, r *http.Request) {
		// Either items to generate now, or a confirmed estimate.
		var request struct {
			Items            []Item `json:"items"`
			Mode             string `json:"mode"`
			EstimateID       string `json:"estimate_id"`
			ConfirmEstimate  bool   `json:"confirm_estimate"`
			ConfirmIsolation bool   `json:"confirm_isolation"`
		}
		if !decode(w, r, &request) {
			return
		}
		var v []Job
		var e error
		if len(request.Items) > 0 {
			v, e = q.Generate(r.Context(), request.Items, request.Mode, request.ConfirmIsolation)
			if errors.Is(e, ErrIsolationRequired) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusConflict)
				_ = json.NewEncoder(w).Encode(map[string]any{"isolation_required": true})
				return
			}
		} else {
			v, e = q.SubmitConfirmed(r.Context(), request.EstimateID, Confirmation{Estimate: request.ConfirmEstimate, Isolation: request.ConfirmIsolation})
		}
		if errors.Is(e, ErrRestarting) {
			w.Header().Set("Retry-After", "5")
			http.Error(w, ErrRestarting.Error(), http.StatusServiceUnavailable)
			return
		}
		if errors.Is(e, ErrNeedsClone) {
			needsClone(w)
			return
		}
		if e != nil {
			http.Error(w, "estimate expired, generation inputs changed, confirmation missing, or job already running", http.StatusConflict)
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
	return mux
}
