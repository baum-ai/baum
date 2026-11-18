package jobs

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strconv"
	"sync"

	"github.com/baum-ai/baum/internal/reviewgroups"
	"github.com/baum-ai/baum/internal/tileview"
	"github.com/baum-ai/baum/tiles"
)

// A page of GET /api/jobs?limit=&before= holds defaultJobsPage jobs unless
// limit says otherwise, at most maxJobsPage.
const (
	defaultJobsPage = 50
	maxJobsPage     = 200
)

func (q *Queue) Handler() http.Handler {
	mux := http.NewServeMux()
	drafts := &q.draftTiles
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
		query := r.URL.Query()
		repository, pr, limit, before := query.Get("repository"), query.Get("pr"), query.Get("limit"), query.Get("before")
		for name := range query {
			if name != "repository" && name != "pr" && name != "limit" && name != "before" {
				http.Error(w, "unknown parameter "+name, 400)
				return
			}
		}
		paged := limit != "" || query.Has("before")
		switch {
		case (repository != "" || pr != "") && paged:
			http.Error(w, "repository and pr cannot be combined with limit or before", 400)
		case repository != "" || pr != "":
			number, e := strconv.Atoi(pr)
			if repository == "" || e != nil || number < 1 {
				http.Error(w, "repository and a positive pr are both required", 400)
				return
			}
			respond(w, map[string]any{"available": true, "jobs": q.OfPullRequest(repository, number)})
		case paged:
			n := defaultJobsPage
			if limit != "" {
				var e error
				if n, e = strconv.Atoi(limit); e != nil || n < 1 || n > maxJobsPage {
					http.Error(w, "limit must be 1 to "+strconv.Itoa(maxJobsPage), 400)
					return
				}
			}
			if query.Has("before") && before == "" {
				http.Error(w, "unknown job", 400)
				return
			}
			page, next, e := q.Page(before, n)
			if e != nil {
				http.Error(w, "unknown job", 400)
				return
			}
			answer := map[string]any{"available": true, "jobs": page}
			if next != "" {
				answer["next"] = next
			}
			respond(w, answer)
		default:
			respond(w, map[string]any{"available": true, "jobs": q.Current()})
		}
	})
	// GET /api/jobs/{id} is one loaded job, wherever it is in the history,
	// for the transcript page of a job the current view no longer holds.
	mux.HandleFunc("GET /api/jobs/{id}", func(w http.ResponseWriter, r *http.Request) {
		job, ok := q.Find(r.PathValue("id"))
		if !ok {
			http.Error(w, http.StatusText(404), 404)
			return
		}
		respond(w, job)
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
	// seen records that the user looked at a failed job (Queue.Seen).
	mux.HandleFunc("POST /api/jobs/{id}/seen", func(w http.ResponseWriter, r *http.Request) {
		var request struct{}
		if !decode(w, r, &request) {
			return
		}
		v, e := q.Seen(r.PathValue("id"))
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
	// draft serves a running staged review's latest draft: masked and
	// completed like a version, with the sections still being written
	// pending, and never a version. A job without one, or ended, is 404.
	// Only the exact value tiles=1 answers as GET /api/tiles does, plus the
	// draft number; any other value, or none, is the plain draft. The tile view
	// is converted once per draft and kept until the job ends, since the app
	// polls it while a review generates.
	mux.HandleFunc("GET /api/jobs/{id}/draft", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		draft, revision, ok := q.Draft(id)
		if !ok {
			drafts.drop(id)
			http.Error(w, http.StatusText(404), 404)
			return
		}
		if r.URL.Query().Get("tiles") == "1" {
			// The draft's tile view: a missing context is still being
			// written, so it is a pending context tile.
			body, err := drafts.get(q, id, revision, draft, nil)
			if err != nil {
				http.Error(w, http.StatusText(500), 500)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(body)
			return
		}
		respond(w, map[string]any{"draft": revision, "document": json.RawMessage(draft)})
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
		job, found := q.Find(r.PathValue("id"))
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

// draftTiles keeps the encoded tile view of each job's latest draft, by the
// draft's count, so an unchanged draft is not converted on every poll. A job's
// entry goes with its draft: when the queue ends or archives the job, or a
// read finds it has none.
type draftTiles struct {
	mu   sync.Mutex
	kept map[string]draftTileView
}

type draftTileView struct {
	revision    int
	composition string
	body        []byte
}

// get answers the tile view of a draft of q's job id, composed with
// composition when given (no draft has one yet). The kept view is that of the
// same draft count and composition, kept only while the job still has a
// draft, so one converted as the job ended is not kept.
func (d *draftTiles) get(q *Queue, id string, revision int, draft, composition []byte) ([]byte, error) {
	key := tileview.Key(composition)
	d.mu.Lock()
	kept, ok := d.kept[id]
	d.mu.Unlock()
	if ok && kept.revision == revision && kept.composition == key {
		return kept.body, nil
	}
	full, err := json.Marshal(struct {
		Draft int `json:"draft"`
		tileview.Response
	}{revision, tileview.Build(draft, composition, tiles.Options{Draft: true})})
	if err != nil {
		return nil, err
	}
	full = append(full, '\n')
	// q.mu before d.mu, as where the queue drops a job's draft.
	q.mu.Lock()
	if _, live := q.drafts[id]; live {
		d.mu.Lock()
		if d.kept == nil {
			d.kept = map[string]draftTileView{}
		}
		d.kept[id] = draftTileView{revision, key, full}
		d.mu.Unlock()
	}
	q.mu.Unlock()
	return full, nil
}

func (d *draftTiles) drop(id string) {
	d.mu.Lock()
	delete(d.kept, id)
	d.mu.Unlock()
}
