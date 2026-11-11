package uiserver

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/baum-ai/baum/internal/discovery"
	"github.com/baum-ai/baum/internal/etagjson"
	"github.com/baum-ai/baum/internal/github"
	"github.com/baum-ai/baum/internal/repos"
)

// DiscoveryCache is shared with the review index. Cached must never contact
// GitHub; polling runs in the background or an authenticated same-origin mutation.
// A refresh the person asks for polls now, without waiting for the backoff.
type DiscoveryCache interface {
	Cached(context.Context, github.Repository) (discovery.Snapshot, error)
	PollNow(context.Context, github.Repository) (discovery.Snapshot, error)
}

// refreshTimeout is how long a refresh the person asks for waits for GitHub to
// answer, as the background poll of one repository waits: a connection that
// stalls (the network dropping mid-read) answers as a failed refresh instead
// of never, while a busy repository's refresh may take minutes.
var refreshTimeout = 45 * time.Second

// revisioned is what a DiscoveryCache may also offer: a stamp of what Cached
// would read, which lets GET /api/prs reuse its encoded answer.
type revisioned interface {
	Revision(context.Context, github.Repository) (discovery.Revision, error)
}

// listing is an encoded GET /api/prs answer and what it was built from.
type listing struct {
	key  string
	body etagjson.Body
	// until is when a snapshot in it reaches its next poll and turns stale,
	// which no revision records; zero when none does.
	until time.Time
}

func DiscoveryHandler(registry *repos.Registry, service DiscoveryCache, changed func()) http.Handler {
	mux := http.NewServeMux()
	var mu sync.Mutex
	var last *listing
	mux.HandleFunc("GET /api/prs", func(w http.ResponseWriter, r *http.Request) {
		list, err := registry.RepositoryRows(r.Context())
		if err != nil {
			apiError(w, err)
			return
		}
		seen := map[string]bool{}
		var picks []github.Repository
		for _, picked := range list {
			if seen[picked.Repository] {
				continue
			}
			seen[picked.Repository] = true
			repo, err := github.ParseRepository(picked.Repository)
			if err != nil {
				apiError(w, err)
				return
			}
			picks = append(picks, repo)
		}
		// The key is read before the snapshots: a write in between caches
		// newer data under the older key, which only costs another encode.
		key, keyed := "", false
		if rev, ok := service.(revisioned); ok {
			keyed = true
			var b strings.Builder
			for _, repo := range picks {
				stamp, err := rev.Revision(r.Context(), repo)
				if err != nil {
					keyed = false
					break
				}
				fmt.Fprintf(&b, "%s %+v\n", repo, stamp)
			}
			key = b.String()
		}
		if keyed {
			mu.Lock()
			hit := last
			mu.Unlock()
			if hit != nil && hit.key == key && (hit.until.IsZero() || time.Now().Before(hit.until)) {
				etagjson.Serve(w, r, hit.body)
				return
			}
		}
		out := []discovery.Snapshot{}
		var until time.Time
		for _, repo := range picks {
			snapshot, err := service.Cached(r.Context(), repo)
			if err != nil {
				snapshot.Repository = repo.String()
				snapshot.Stale = true
				snapshot.Error = "cache_unavailable"
				keyed = false
			} else if !snapshot.Stale && !snapshot.NextPoll.IsZero() && (until.IsZero() || snapshot.NextPoll.Before(until)) {
				until = snapshot.NextPoll
			}
			out = append(out, snapshot)
		}
		body, err := etagjson.Encode(out)
		if err != nil {
			apiError(w, err)
			return
		}
		if keyed {
			mu.Lock()
			last = &listing{key: key, body: body, until: until}
			mu.Unlock()
		}
		etagjson.Serve(w, r, body)
	})
	mux.HandleFunc("POST /api/prs/refresh", func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, 1024)
		var body struct {
			Clone      string `json:"clone"`
			Repository string `json:"repository"`
		}
		d := json.NewDecoder(r.Body)
		d.DisallowUnknownFields()
		if d.Decode(&body) != nil || d.Decode(new(any)) != io.EOF || (body.Clone == "") == (body.Repository == "") {
			http.Error(w, "exactly one of clone or repository is required", http.StatusBadRequest)
			return
		}
		var name string
		var err error
		if body.Clone != "" {
			name, err = RegisteredRepository(r.Context(), registry, body.Clone)
		} else {
			name, err = pickedRepository(r.Context(), registry, body.Repository)
		}
		if err != nil {
			apiError(w, err)
			return
		}
		repo, err := github.ParseRepository(name)
		if err != nil {
			apiError(w, err)
			return
		}
		ctx, cancel := discovery.WithIdleTimeout(r.Context(), refreshTimeout)
		snapshot, err := service.PollNow(ctx, repo)
		cancel()
		if err != nil {
			snapshot.Repository = repo.String()
			snapshot.Stale = true
			if snapshot.Error == "" {
				snapshot.Error = "discovery_unavailable"
			}
		}
		if changed != nil {
			changed()
		}
		writeJSON(w, snapshot)
	})
	return mux
}
