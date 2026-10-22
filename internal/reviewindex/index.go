// Package reviewindex joins GitHub discovery with local immutable version stores.
// Rebuilds are read-only; no documents are copied into a separate index database.
package reviewindex

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/baum-ai/baum/internal/discovery"
	"github.com/baum-ai/baum/internal/github"
	"github.com/baum-ai/baum/internal/repos"
	"github.com/baum-ai/baum/internal/store"
)

type Registry interface {
	List(context.Context) ([]repos.Clone, error)
}
type Discovery interface {
	Cached(context.Context, github.Repository) (discovery.Snapshot, error)
}
type Reader interface {
	Snapshot(context.Context) ([]store.Summary, error)
}

type Service struct {
	Registry  Registry
	Discovery Discovery
	// OpenStore may resolve an explicit --store registration in a future host.
	// The default uses the same common-dir/XDG selection as baum internal generate.
	OpenStore func(commonDir string) (Reader, error)
}

type Entry struct {
	PullRequest   discovery.PullRequest `json:"pull_request"`
	CloneID       string                `json:"clone_id,omitempty"`
	Status        string                `json:"status"`
	FailureReason string                `json:"failure_reason,omitempty"`
	VersionCount  int                   `json:"version_count"`
	Latest        *store.Metadata       `json:"latest"`
	RiskScore     json.RawMessage       `json:"risk_score"`
	ReviewMinutes *int                  `json:"review_minutes,omitempty"`
	// Historical includes saved reviews outside the current discovered inbox.
	// Their state is UNKNOWN unless the saved document already says closed/merged.
	Historical        bool   `json:"historical"`
	Stale             bool   `json:"stale"`
	StoreError        string `json:"store_error,omitempty"`
	Checks            string `json:"checks"`
	MetadataFetchedAt string `json:"metadata_fetched_at,omitempty"`
}
type Problem struct {
	CloneID string `json:"clone_id"`
	Reason  string `json:"reason"`
}
type Snapshot struct {
	Entries  []Entry   `json:"entries"`
	Problems []Problem `json:"problems"`
}

func (s *Service) Build(ctx context.Context) (Snapshot, error) {
	out := Snapshot{Entries: []Entry{}, Problems: []Problem{}}
	clones, err := s.Registry.List(ctx)
	if err != nil {
		return out, err
	}
	discovered := map[store.Key]discovery.PullRequest{}
	stale := map[string]bool{}
	seenRepo := map[string]bool{}
	observations := map[store.Key]discovery.Observation{}
	discoveryFetched := map[string]time.Time{}
	for _, clone := range clones {
		repository := strings.ToLower(clone.Repository)
		if seenRepo[repository] {
			continue
		}
		seenRepo[repository] = true
		if s.Discovery == nil {
			stale[repository] = true
			continue
		}
		parsed, err := github.ParseRepository(repository)
		if err != nil {
			return out, err
		}
		snapshot, err := s.Discovery.Cached(ctx, parsed)
		stale[repository] = err != nil || snapshot.Stale
		discoveryFetched[repository] = snapshot.FetchedAt
		if err != nil && ctx.Err() != nil {
			return out, ctx.Err()
		}
		for _, value := range snapshot.Observations {
			observations[store.Key{Repository: repository, Number: value.Number}] = value
		}
		for _, pr := range snapshot.PullRequests {
			pr.Repository = repository
			discovered[store.Key{Repository: repository, Number: pr.Number}] = pr
		}
	}
	// Distinct stores of one repository can hold independent histories; expose a
	// row per store, with clone_id, rather than arbitrarily choosing a winner.
	seenStore := map[string]bool{}
	open := s.OpenStore
	if open == nil {
		open = func(common string) (Reader, error) { return store.Open(common, "", false) }
	}
	for _, clone := range clones {
		identity := strings.ToLower(clone.Repository)
		if seenStore[clone.CommonDir] {
			continue
		}
		seenStore[clone.CommonDir] = true
		if !clone.Available {
			out.Problems = append(out.Problems, Problem{clone.ID, "clone_unavailable"})
			stale[identity] = true
		}
		// Coverage is per store: a review in one clone must not hide the missing
		// review in another clone's independent history.
		covered := map[store.Key]bool{}
		reader, err := open(clone.CommonDir)
		var summaries []store.Summary
		if err == nil {
			summaries, err = reader.Snapshot(ctx)
		}
		if err != nil {
			if ctx.Err() != nil {
				return out, ctx.Err()
			}
			out.Problems = append(out.Problems, Problem{clone.ID, "store_unreadable"})
			for key, pr := range discovered {
				if key.Repository == identity {
					out.Entries = append(out.Entries, Entry{PullRequest: pr, CloneID: clone.ID, Status: "unknown", Stale: stale[identity], StoreError: "store_unreadable"})
				}
			}
			continue
		}
		for _, summary := range summaries {
			if summary.Key.Repository != identity {
				continue
			}
			pr, found := discovered[summary.Key]
			covered[summary.Key] = true
			if !found {
				state := strings.ToUpper(summary.PRState)
				if state != "CLOSED" && state != "MERGED" {
					state = "UNKNOWN"
				}
				pr = discovery.PullRequest{Repository: identity, Number: summary.Key.Number, Title: summary.Title, State: state}
			}
			entry := Entry{PullRequest: pr, CloneID: clone.ID, Status: "no_review", VersionCount: summary.VersionCount, Latest: summary.Latest, RiskScore: summary.RiskScore, ReviewMinutes: summary.ReviewMinutes, Historical: !found, Stale: stale[identity] || !found}
			if summary.Latest != nil {
				entry.Status = "outdated"
				if !found || pr.HeadSHA == "" {
					entry.Status = "unknown"
				} else if summary.Latest.Head == pr.HeadSHA {
					entry.Status = "current"
				}
			}
			if summary.Run != nil {
				switch summary.Run.State {
				case "generating":
					entry.Status = "generating"
				case "failed":
					entry.Status = "failed"
					entry.FailureReason = summary.Run.Reason
				}
			}
			out.Entries = append(out.Entries, entry)
		}
		for key, pr := range discovered {
			if key.Repository == identity && !covered[key] {
				out.Entries = append(out.Entries, Entry{PullRequest: pr, CloneID: clone.ID, Status: "no_review", Stale: stale[identity]})
			}
		}
	}
	for i := range out.Entries {
		entry := &out.Entries[i]
		key := store.Key{Repository: entry.PullRequest.Repository, Number: entry.PullRequest.Number}
		if value, ok := observations[key]; ok {
			// A newer discovery head wins over a previous metadata fetch. Never
			// label old-head checks as checks on the current revision.
			if !entry.Historical && value.FetchedAt.Before(discoveryFetched[key.Repository]) {
				continue
			}
			entry.PullRequest.HeadSHA = value.HeadSHA
			entry.PullRequest.Title = value.Title
			entry.PullRequest.State = value.State
			entry.PullRequest.Draft = value.Draft
			entry.Stale = stale[key.Repository] || time.Since(value.FetchedAt) > 2*time.Minute
			entry.Checks = value.Checks
			entry.MetadataFetchedAt = value.FetchedAt.UTC().Format(time.RFC3339Nano)
			if entry.Latest != nil && (entry.Status == "unknown" || entry.Status == "current" || entry.Status == "outdated") {
				entry.Status = "outdated"
				if entry.Latest.Head == value.HeadSHA {
					entry.Status = "current"
				}
			}
		}
	}
	sort.Slice(out.Entries, func(i, j int) bool {
		a, b := out.Entries[i], out.Entries[j]
		if a.PullRequest.Repository != b.PullRequest.Repository {
			return a.PullRequest.Repository < b.PullRequest.Repository
		}
		if a.PullRequest.Number != b.PullRequest.Number {
			return a.PullRequest.Number < b.PullRequest.Number
		}
		return a.CloneID < b.CloneID
	})
	return out, nil
}

// Handler serves GET /api/index. The host must apply its launch authentication
// and Host/Origin checks before routing here. history=1 includes saved-only rows.
func (s *Service) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/index" {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", "GET")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		snapshot, err := s.Build(r.Context())
		if err != nil {
			if !errors.Is(err, context.Canceled) {
				http.Error(w, "index unavailable", http.StatusInternalServerError)
			}
			return
		}
		if r.URL.Query().Get("history") != "1" {
			filtered := []Entry{}
			for _, entry := range snapshot.Entries {
				if !entry.Historical {
					filtered = append(filtered, entry)
				}
			}
			snapshot.Entries = filtered
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(snapshot)
	})
}
