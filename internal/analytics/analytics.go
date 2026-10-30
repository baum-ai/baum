// Package analytics reports what the person's coding agents did in their
// registered clones, what it cost and what was delivered, for the app's
// Analytics page. See README.md.
package analytics

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/baum-ai/baum/internal/decisionlog"
	"github.com/baum-ai/baum/internal/jobs"
	"github.com/baum-ai/baum/internal/planusage"
	"github.com/baum-ai/baum/internal/repos"
)

// Windows are the lengths, in days, a report can cover.
var Windows = []int{7, 30, 90}

// Agents are the agent filter's values: the vendors whose sessions the index
// reads.
var Agents = []string{"claude_code", "codex", "cursor"}

// Registry is what the report reads of the registered repositories;
// *repos.Registry is one.
type Registry interface {
	List(ctx context.Context) ([]repos.Clone, error)
	Repositories(ctx context.Context) ([]repos.Repository, error)
}

// Service builds reports from the index, the jobs ledger, the actions
// ledger, the plan usage history and GitHub's answers. Any of the last four
// may be nil; their sections then say they are unavailable.
type Service struct {
	Registry Registry
	Index    *Index
	Jobs     func() []jobs.Job
	Actions  *Actions
	Plans    *planusage.History
	Delivery *Delivery
	// Captures reads decision capture's ledger from since on; nil without
	// decision capture.
	Captures func(ctx context.Context, since time.Time) ([]decisionlog.LedgerEntry, error)
	// Location is the zone days and hours are counted in; nil is local.
	Location *time.Location
	Now      func() time.Time

	commits commitReader
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Service) location() *time.Location {
	if s.Location != nil {
		return s.Location
	}
	return time.Local
}

// Query is what a report covers.
type Query struct {
	Days       int
	Repository string
	Agent      string
}

// ParseQuery reads ?days=7|30|90&repo=owner/name&agent=claude_code|codex|cursor.
// days defaults to 30; repo and agent default to all.
func ParseQuery(values map[string][]string) (Query, string) {
	get := func(key string) string {
		if v := values[key]; len(v) > 0 {
			return strings.TrimSpace(v[0])
		}
		return ""
	}
	q := Query{Days: 30, Repository: strings.ToLower(get("repo")), Agent: get("agent")}
	if raw := get("days"); raw != "" {
		days, err := strconv.Atoi(raw)
		if err != nil || !slices.Contains(Windows, days) {
			return Query{}, "days must be 7, 30 or 90"
		}
		q.Days = days
	}
	if q.Agent != "" && !slices.Contains(Agents, q.Agent) {
		return Query{}, "agent must be claude_code, codex or cursor"
	}
	if q.Repository != "" && (strings.Count(q.Repository, "/") != 1 || len(q.Repository) > 200) {
		return Query{}, "repo must be owner/name"
	}
	return q, ""
}

// Handler serves GET /api/analytics. A repository that is neither picked nor
// registered is a 400, so a request cannot name what baum does not watch.
func (s *Service) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		q, problem := ParseQuery(r.URL.Query())
		if problem != "" {
			http.Error(w, problem, http.StatusBadRequest)
			return
		}
		report, err := s.Report(r.Context(), q)
		if err == errUnknownRepository {
			http.Error(w, "repo is not one of your repositories", http.StatusBadRequest)
			return
		}
		if err != nil {
			http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(report)
	})
}
