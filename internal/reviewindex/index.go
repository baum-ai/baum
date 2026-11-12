// Package reviewindex joins GitHub discovery with local immutable version stores.
// Rebuilds are read-only; no documents are copied into a separate index database.
package reviewindex

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"slices"
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

// Revisions is a Discovery that stamps what Cached reads without reading it
// (discovery.Revision). A Cache keeps a repository's rows only while its
// revision holds, so without it every build reads every repository.
type Revisions interface {
	Revision(context.Context, github.Repository) (discovery.Revision, error)
}

type Service struct {
	Registry  Registry
	Discovery Discovery
	// OpenStore may resolve an explicit --store registration in a future host.
	// The default uses the same common-dir/XDG selection as the generation pipeline.
	OpenStore func(commonDir string) (Reader, error)
	// StoreStamp stamps what OpenStore's reader of commonDir reads, without
	// opening it; ok is false when it cannot vouch for it. Two equal stamps
	// must read the same summaries. The default, for the default OpenStore,
	// stats versions.sqlite and its write-ahead log under both store roots.
	// With another OpenStore and no StoreStamp, every build reads every store.
	StoreStamp func(commonDir string) (stamp string, ok bool)
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
	// ObservedAt is when the observation applied to the entry was fetched, and
	// is empty without one. The app counts its age; the index does not, so it
	// does not change with time alone.
	ObservedAt string `json:"observed_at,omitempty"`
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
	out, _, err := s.build(ctx, time.Now(), nil)
	return out, err
}

// build also returns the earliest instant after now at which building again
// could give another snapshot with nothing written: a run lease expiring or a
// discovery snapshot reaching its next poll. The age of fetched metadata does
// not count: the app works that out itself. It is zero when there is none.
// With kept, a repository whose inputs did not change since kept read it is
// not read again (parts.go).
func (s *Service) build(ctx context.Context, now time.Time, kept *parts) (Snapshot, time.Time, error) {
	var next time.Time
	changeAt := func(t time.Time) {
		if t.After(now) && (next.IsZero() || t.Before(next)) {
			next = t
		}
	}
	out, err := s.assemble(ctx, now, kept, changeAt)
	return out, next, err
}

func (s *Service) assemble(ctx context.Context, now time.Time, kept *parts, changeAt func(time.Time)) (Snapshot, error) {
	out := Snapshot{Entries: []Entry{}, Problems: []Problem{}}
	picked, err := s.Registry.Repositories(ctx)
	if err != nil {
		return out, err
	}
	groups := groupRepositories(picked)
	reuse, full := kept.reusable(now)
	if reuse {
		changeAt(full)
	}
	byIdentity := make(map[string]*part, len(groups))
	for _, g := range groups {
		p, err := s.repositoryPart(ctx, now, g, kept, reuse, changeAt)
		if err != nil {
			return out, err
		}
		changeAt(p.next)
		byIdentity[g.identity] = p
		out.Problems = append(out.Problems, p.problems...)
	}
	kept.finish(now, !reuse, byIdentity)
	// Each part holds one repository's rows, sorted: in repository order
	// they are the whole snapshot's order.
	total := 0
	for _, p := range byIdentity {
		total += len(p.entries)
	}
	out.Entries = make([]Entry, 0, total)
	for _, identity := range slices.Sorted(maps.Keys(byIdentity)) {
		out.Entries = append(out.Entries, byIdentity[identity].entries...)
	}
	return out, nil
}

// group is one repository's share of the registry: whether it is picked,
// how many of its picks have no clone, whether any of its picks has an
// available clone, and its clones in registry order.
type group struct {
	identity string
	picked   bool
	bare     int
	hasClone bool
	clones   []groupClone
}

// groupClone is a clone and whether its rows come from it. Distinct stores
// of one repository can hold independent histories, so each store gives a
// row per pull request with its clone_id rather than one arbitrary winner;
// a store several clones share (worktrees) is read for the first only.
type groupClone struct {
	clone repos.Clone
	owned bool
}

func groupRepositories(picked []repos.Repository) []*group {
	var groups []*group
	byIdentity := map[string]*group{}
	get := func(identity string) *group {
		g := byIdentity[identity]
		if g == nil {
			g = &group{identity: identity}
			byIdentity[identity] = g
			groups = append(groups, g)
		}
		return g
	}
	for _, pick := range picked {
		g := get(strings.ToLower(pick.Repository))
		g.picked = true
		if len(pick.Clones) == 0 {
			g.bare++
		}
		for _, clone := range pick.Clones {
			if clone.Available {
				g.hasClone = true
			}
		}
	}
	seenStore := map[string]bool{}
	for _, pick := range picked {
		for _, clone := range pick.Clones {
			g := get(strings.ToLower(clone.Repository))
			g.clones = append(g.clones, groupClone{clone: clone, owned: !seenStore[clone.CommonDir]})
			seenStore[clone.CommonDir] = true
		}
	}
	return groups
}

// readPart reads one repository's rows from its discovery cache and stores.
// cacheable is false when a read failed, so that it is tried again.
func (s *Service) readPart(ctx context.Context, now time.Time, g *group) (p *part, cacheable bool, err error) {
	p = &part{read: now, entries: []Entry{}, problems: []Problem{}}
	changeAt := func(t time.Time) {
		if t.After(now) && (p.next.IsZero() || t.Before(p.next)) {
			p.next = t
		}
	}
	cacheable = true
	identity := g.identity
	stale := false
	discovered := map[int]discovery.PullRequest{}
	observations := map[int]discovery.Observation{}
	var fetched time.Time
	if g.picked {
		if s.Discovery == nil {
			stale = true
		} else {
			parsed, err := github.ParseRepository(identity)
			if err != nil {
				return nil, false, err
			}
			snapshot, err := s.Discovery.Cached(ctx, parsed)
			stale = err != nil || snapshot.Stale
			if !stale {
				changeAt(snapshot.NextPoll)
			}
			fetched = snapshot.FetchedAt
			if err != nil {
				if ctx.Err() != nil {
					return nil, false, ctx.Err()
				}
				cacheable = false
			}
			for _, value := range snapshot.Observations {
				observations[value.Number] = value
			}
			for _, pr := range snapshot.PullRequests {
				pr.Repository = identity
				discovered[pr.Number] = pr
			}
		}
	}
	open := s.OpenStore
	if open == nil {
		open = func(common string) (Reader, error) { return store.Open(common, "", false) }
	}
	for _, c := range g.clones {
		if !c.owned {
			continue
		}
		clone := c.clone
		if !clone.Available {
			p.problems = append(p.problems, Problem{clone.ID, "clone_unavailable"})
			stale = true
		}
		// Coverage is per store: a review in one clone must not hide the missing
		// review in another clone's independent history.
		covered := map[int]bool{}
		reader, err := open(clone.CommonDir)
		var summaries []store.Summary
		if err == nil {
			summaries, err = reader.Snapshot(ctx)
		}
		if err != nil {
			if ctx.Err() != nil {
				return nil, false, ctx.Err()
			}
			cacheable = false
			p.problems = append(p.problems, Problem{clone.ID, "store_unreadable"})
			for _, pr := range discovered {
				p.entries = append(p.entries, Entry{PullRequest: pr, CloneID: clone.ID, Status: "unknown", Stale: stale, StoreError: "store_unreadable"})
			}
			continue
		}
		for _, summary := range summaries {
			if summary.Key.Repository != identity {
				continue
			}
			pr, found := discovered[summary.Key.Number]
			covered[summary.Key.Number] = true
			if !found {
				state := strings.ToUpper(summary.PRState)
				if state != "CLOSED" && state != "MERGED" {
					state = "UNKNOWN"
				}
				pr = discovery.PullRequest{Repository: identity, Number: summary.Key.Number, Title: summary.Title, State: state}
			}
			entry := Entry{PullRequest: pr, CloneID: clone.ID, Status: "no_review", VersionCount: summary.VersionCount, Latest: summary.Latest, RiskScore: summary.RiskScore, ReviewMinutes: summary.ReviewMinutes, Historical: !found, Stale: stale || !found}
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
			p.entries = append(p.entries, entry)
		}
		for number, pr := range discovered {
			if !covered[number] {
				p.entries = append(p.entries, Entry{PullRequest: pr, CloneID: clone.ID, Status: "no_review", Stale: stale})
			}
		}
	}
	// A repository without a clone has no store: list its discovered pull
	// requests so they can be seen, without a clone ID to generate with.
	for range g.bare {
		for _, pr := range discovered {
			p.entries = append(p.entries, Entry{PullRequest: pr, Status: "no_review", Stale: stale})
		}
	}
	for i := range p.entries {
		entry := &p.entries[i]
		entry.NeedsClone = !g.hasClone
		entry.Review, entry.Checks, entry.Merge = entry.PullRequest.Review, entry.PullRequest.Checks, entry.PullRequest.Merge
		entry.Unresolved, entry.UnresolvedCapped = entry.PullRequest.Unresolved, entry.PullRequest.UnresolvedCapped
		if !fetched.IsZero() && !entry.Historical {
			entry.MetadataFetchedAt = fetched.UTC().Format(time.RFC3339Nano)
		}
		if value, ok := observations[entry.PullRequest.Number]; ok {
			// A newer discovery head wins over a previous metadata fetch. Never
			// label old-head checks as checks on the current revision.
			if !entry.Historical && value.FetchedAt.Before(fetched) {
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
			entry.Stale = stale
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
			entry.ObservedAt = entry.MetadataFetchedAt
			if entry.Latest != nil && (entry.Status == "unknown" || entry.Status == "current" || entry.Status == "outdated") {
				entry.Status = "outdated"
				if entry.Latest.Head == value.HeadSHA {
					entry.Status = "current"
				}
			}
		}
	}
	sort.Slice(p.entries, func(i, j int) bool {
		a, b := p.entries[i], p.entries[j]
		if a.PullRequest.Number != b.PullRequest.Number {
			return a.PullRequest.Number < b.PullRequest.Number
		}
		return a.CloneID < b.CloneID
	})
	return p, cacheable, nil
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
