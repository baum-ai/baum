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
		if e != nil {
			http.Error(w, "cannot estimate: select available PRs with an explicit priced model", 400)
			return
		}
		respond(w, v)
	})
	mux.HandleFunc("POST /api/jobs", func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			EstimateID       string `json:"estimate_id"`
			ConfirmEstimate  bool   `json:"confirm_estimate"`
			ConfirmIsolation bool   `json:"confirm_isolation"`
		}
		if !decode(w, r, &request) {
			return
		}
		v, e := q.Submit(r.Context(), request.EstimateID, request.ConfirmEstimate, request.ConfirmIsolation)
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
