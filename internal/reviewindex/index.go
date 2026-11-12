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
	"github.com/baum-ai/baum/internal/etagjson"
	"github.com/baum-ai/baum/internal/github"
	"github.com/baum-ai/baum/internal/repos"
	"github.com/baum-ai/baum/internal/store"
)

type Registry interface {
	Repositories(context.Context) ([]repos.Repository, error)
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
	// The default uses the same common-dir/XDG selection as the generation pipeline.
	OpenStore func(commonDir string) (Reader, error)
}

type Entry struct {
	PullRequest discovery.PullRequest `json:"pull_request"`
	CloneID     string                `json:"clone_id,omitempty"`
	// NeedsClone marks a repository with no available clone: its pull
	// requests are listed from discovery, but a review waits for a clone.
	NeedsClone    bool            `json:"needs_clone,omitempty"`
	Status        string          `json:"status"`
	FailureReason string          `json:"failure_reason,omitempty"`
	VersionCount  int             `json:"version_count"`
	Latest        *store.Metadata `json:"latest"`
	RiskScore     json.RawMessage `json:"risk_score"`
	// Historical includes saved reviews outside the current discovered inbox.
	// Their state is UNKNOWN unless the saved document already says closed/merged.
	Historical        bool   `json:"historical"`
	Stale             bool   `json:"stale"`
	StoreError        string `json:"store_error,omitempty"`
	Review            string `json:"review"`
	Merge             string `json:"merge"`
	Unresolved        *int   `json:"unresolved"`
	UnresolvedCapped  bool   `json:"unresolved_capped"`
	ReviewMinutes     *int   `json:"review_minutes"`
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
	out, _, err := s.build(ctx, time.Now())
	return out, err
}

// build also returns the earliest instant after now at which building again
// could give another snapshot with nothing written: a run lease expiring, a
// discovery snapshot reaching its next poll, or an observation turning stale.
// It is zero when there is none.
func (s *Service) build(ctx context.Context, now time.Time) (Snapshot, time.Time, error) {
	var next time.Time
	changeAt := func(t time.Time) {
		if t.After(now) && (next.IsZero() || t.Before(next)) {
			next = t
		}
	}
	out, err := s.assemble(ctx, changeAt)
	return out, next, err
}

func (s *Service) assemble(ctx context.Context, changeAt func(time.Time)) (Snapshot, error) {
	out := Snapshot{Entries: []Entry{}, Problems: []Problem{}}
	picked, err := s.Registry.Repositories(ctx)
	if err != nil {
		return out, err
	}
	var clones []repos.Clone
	hasClone := map[string]bool{}
	for _, repository := range picked {
		for _, clone := range repository.Clones {
			clones = append(clones, clone)
			if clone.Available {
				hasClone[strings.ToLower(repository.Repository)] = true
			}
		}
	}
	discovered := map[store.Key]discovery.PullRequest{}
	stale := map[string]bool{}
	seenRepo := map[string]bool{}
	observations := map[store.Key]discovery.Observation{}
	discoveryFetched := map[string]time.Time{}
	for _, pick := range picked {
		repository := strings.ToLower(pick.Repository)
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
		if !stale[repository] {
			changeAt(snapshot.NextPoll)
		}
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
					changeAt(summary.Run.Expires)
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
	// A repository without a clone has no store: list its discovered pull
	// requests so they can be seen, without a clone ID to generate with.
	for _, pick := range picked {
		identity := strings.ToLower(pick.Repository)
		if len(pick.Clones) > 0 {
			continue
		}
		for key, pr := range discovered {
			if key.Repository == identity {
				out.Entries = append(out.Entries, Entry{PullRequest: pr, Status: "no_review", Stale: stale[identity]})
			}
		}
	}
	for i := range out.Entries {
		entry := &out.Entries[i]
		key := store.Key{Repository: entry.PullRequest.Repository, Number: entry.PullRequest.Number}
		entry.NeedsClone = !hasClone[key.Repository]
		entry.Review, entry.Checks, entry.Merge = entry.PullRequest.Review, entry.PullRequest.Checks, entry.PullRequest.Merge
		entry.Unresolved, entry.UnresolvedCapped = entry.PullRequest.Unresolved, entry.PullRequest.UnresolvedCapped
		if fetched := discoveryFetched[key.Repository]; !fetched.IsZero() && !entry.Historical {
			entry.MetadataFetchedAt = fetched.UTC().Format(time.RFC3339Nano)
		}
		if value, ok := observations[key]; ok {
			// A newer discovery head wins over a previous metadata fetch. Never
			// label old-head checks as checks on the current revision.
			if !entry.Historical && value.FetchedAt.Before(discoveryFetched[key.Repository]) {
				continue
			}
			if value.HeadSHA != entry.PullRequest.HeadSHA {
				// Observations carry no review/merge/thread state for a changed revision.
				entry.Review, entry.Merge, entry.Unresolved, entry.UnresolvedCapped = "", "", nil, false
				entry.PullRequest.Behind = false
			}
			if value.Draft {
				entry.Merge = "draft"
			} else if entry.PullRequest.Draft || value.State != "OPEN" {
				entry.Merge = ""
			}
			entry.PullRequest.HeadSHA = value.HeadSHA
			entry.PullRequest.Title = value.Title
			entry.PullRequest.State = value.State
			entry.PullRequest.Draft = value.Draft
			entry.Stale = stale[key.Repository] || time.Since(value.FetchedAt) > 2*time.Minute
			// Stale once strictly more than two minutes have passed.
			changeAt(value.FetchedAt.Add(2*time.Minute + time.Nanosecond))
			switch value.Checks {
			case "success", "passing":
				entry.Checks = "passing"
			case "failure", "failing":
				entry.Checks = "failing"
			case "pending", "running":
				entry.Checks = "running"
			case "none":
				entry.Checks = "none"
			default:
				entry.Checks = ""
			}
			entry.PullRequest.Review, entry.PullRequest.Checks, entry.PullRequest.Merge = entry.Review, entry.Checks, entry.Merge
			entry.PullRequest.Unresolved, entry.PullRequest.UnresolvedCapped = entry.Unresolved, entry.UnresolvedCapped
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
// and Host/Origin checks before routing here. history=1 includes saved-only rows;
// detail=summary leaves each entry's latest version with the fields a list reads.
func (s *Service) Handler() http.Handler {
	return handler(func(ctx context.Context, v variant) (etagjson.Body, error) {
		snapshot, err := s.Build(ctx)
		if err != nil {
			return etagjson.Body{}, err
		}
		return v.encode(snapshot)
	})
}

// variant is the shape of one GET /api/index answer: history=1 includes
// saved-only rows, detail=summary trims each entry's latest version to the
// fields a list shows (see summaryMetadata).
type variant struct{ history, summary bool }

func (v variant) encode(snapshot Snapshot) (etagjson.Body, error) {
	snapshot = withHistory(snapshot, v.history)
	if !v.summary {
		return etagjson.Encode(snapshot)
	}
	out := struct {
		Entries  []summaryEntry `json:"entries"`
		Problems []Problem      `json:"problems"`
	}{Entries: make([]summaryEntry, len(snapshot.Entries)), Problems: snapshot.Problems}
	for i, entry := range snapshot.Entries {
		out.Entries[i] = summaryEntry{Entry: entry, Latest: summarise(entry.Latest)}
	}
	return etagjson.Encode(out)
}

// summaryMetadata is the part of a saved version's metadata a list reads:
// which version it is, when and by what it was generated, what it cost. The
// rest (the per-attempt accounting, digests, the kept session) is several KB
// per entry, and a version's own read still serves it in full.
type summaryMetadata struct {
	Version         int      `json:"version"`
	Head            string   `json:"head"`
	Backend         string   `json:"backend"`
	Model           string   `json:"model"`
	GeneratedAt     string   `json:"generated_at"`
	Calls           int      `json:"calls"`
	Tokens          int      `json:"tokens"`
	RecordedCostUSD *float64 `json:"recorded_cost_usd"`
}

func summarise(m *store.Metadata) *summaryMetadata {
	if m == nil {
		return nil
	}
	return &summaryMetadata{Version: m.Version, Head: m.Head, Backend: m.Backend, Model: m.Model,
		GeneratedAt: m.GeneratedAt, Calls: m.Calls, Tokens: m.Tokens, RecordedCostUSD: m.RecordedCostUSD}
}

// summaryEntry is an Entry whose latest is a summaryMetadata: the outer
// field hides the embedded one in the JSON.
type summaryEntry struct {
	Entry
	Latest *summaryMetadata `json:"latest"`
}

// withHistory drops saved-only rows unless history is set. It copies the
// entries it keeps, as a cached snapshot is shared.
func withHistory(snapshot Snapshot, history bool) Snapshot {
	if history {
		return snapshot
	}
	filtered := []Entry{}
	for _, entry := range snapshot.Entries {
		if !entry.Historical {
			filtered = append(filtered, entry)
		}
	}
	snapshot.Entries = filtered
	return snapshot
}

// handler serves the bodies body returns. The response revalidates: an
// unchanged snapshot answers 304 to its ETag.
func handler(body func(ctx context.Context, v variant) (etagjson.Body, error)) http.Handler {
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
		query := r.URL.Query()
		b, err := body(r.Context(), variant{history: query.Get("history") == "1", summary: query.Get("detail") == "summary"})
		if err != nil {
			if !errors.Is(err, context.Canceled) {
				http.Error(w, "index unavailable", http.StatusInternalServerError)
			}
			return
		}
		etagjson.Serve(w, r, b)
	})
}
