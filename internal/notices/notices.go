// Package notices turns what the daemon already knows into the "notice"
// events the desktop app shows as operating system notifications: a review
// finished or failed, and a review requested from the user. A notice is the
// smallest record that can name its pull request: kind, repository, number,
// the clone that routes the click, the title as GitHub returned it, the risk
// score the stored review already holds, and a failure category. It never
// carries review text, paths, session content or error messages.
//
// The daily cap pause is not a notice: the scheduler's own "automatic" event
// already says it, and the app reads it there.
package notices

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/baum-ai/baum/internal/discovery"
	"github.com/baum-ai/baum/internal/jobs"
	"github.com/baum-ai/baum/internal/repos"
	"github.com/baum-ai/baum/internal/reviewindex"
)

// Notice kinds.
const (
	KindFinished = "review_finished"
	KindFailed   = "review_failed"
	// KindAutomatic is a review the daemon generated without being asked, when
	// it finished. A failed automatic review is a KindFailed notice.
	KindAutomatic = "review_automatic"
	KindRequested = "review_requested"
)

// maxTitle bounds the title: GitHub allows 256 characters and a notification
// shows far fewer.
const maxTitle = 200

// Notice is the payload of the "notice" event.
type Notice struct {
	Kind       string `json:"kind"`
	Repository string `json:"repository"`
	PR         int    `json:"pr"`
	// Clone is the registered clone whose app page shows the pull request;
	// empty when none is registered.
	Clone string `json:"clone,omitempty"`
	Title string `json:"title,omitempty"`
	// Risk is the review's score from 1 to 5. RiskStatus is the stored
	// score's status ("lower_bound" when incomplete coverage may hide a
	// higher one).
	Risk       *int   `json:"risk,omitempty"`
	RiskStatus string `json:"risk_status,omitempty"`
	// Reason is the failure category of a failed job, such as "timeout".
	Reason string `json:"reason,omitempty"`
}

// Registry lists the registered clones.
type Registry interface {
	List(context.Context) ([]repos.Clone, error)
}

// Service builds notices and hands them to Publish.
type Service struct {
	Registry Registry
	// Index reads the current review index, for the title and risk score.
	Index func(context.Context) (reviewindex.Snapshot, error)
	// Publish delivers one notice; failures are the caller's to ignore.
	Publish func(Notice)

	mu sync.Mutex
	// requested holds, per lower-case repository, the pull requests the
	// previous snapshot had review-requested from the user. A repository's
	// first snapshot is its baseline and notifies nothing.
	requested map[string]map[int]bool
}

// JobDone publishes the notice for a job that just finished: completed
// reviews and failures. Cancelled, interrupted and superseded jobs are not
// the user's news.
func (s *Service) JobDone(ctx context.Context, job jobs.Job) {
	if job.Phase != "finished" || (job.State != "completed" && job.State != "failed") {
		return
	}
	n := Notice{Kind: KindFinished, Repository: job.Repository, PR: job.PR, Clone: job.Clone}
	switch {
	case job.State == "failed":
		n.Kind = KindFailed
		n.Reason = reasonOf(job.Reason)
	case job.Automatic:
		n.Kind = KindAutomatic
	}
	if s.Index != nil {
		if snapshot, err := s.Index(ctx); err == nil {
			if entry, ok := find(snapshot, job); ok {
				n.Title = clip(entry.PullRequest.Title)
				if n.Kind != KindFailed {
					n.Risk, n.RiskStatus = risk(entry.RiskScore)
				}
			}
		}
	}
	s.Publish(n)
}

// Observe publishes a notice for each pull request a successful discovery
// snapshot shows as newly review-requested from the user. Safe for
// concurrent use; an older snapshot arriving late changes nothing it
// already saw.
func (s *Service) Observe(ctx context.Context, snapshot discovery.Snapshot) {
	name := strings.ToLower(snapshot.Repository)
	now := map[int]bool{}
	for _, pr := range snapshot.PullRequests {
		if pr.ReviewRequestedFromMe && !pr.AuthoredByMe {
			now[pr.Number] = true
		}
	}
	s.mu.Lock()
	if s.requested == nil {
		s.requested = map[string]map[int]bool{}
	}
	before, seen := s.requested[name]
	s.requested[name] = now
	s.mu.Unlock()
	if !seen {
		return
	}
	var clone string
	if clones, err := s.Registry.List(ctx); err == nil {
		for _, c := range clones {
			if c.Available && strings.EqualFold(c.Repository, snapshot.Repository) {
				clone = c.ID
				break
			}
		}
	}
	for _, pr := range snapshot.PullRequests {
		if now[pr.Number] && !before[pr.Number] {
			s.Publish(Notice{Kind: KindRequested, Repository: name, PR: pr.Number, Clone: clone, Title: clip(pr.Title)})
		}
	}
}

func find(snapshot reviewindex.Snapshot, job jobs.Job) (reviewindex.Entry, bool) {
	var fallback *reviewindex.Entry
	for i, e := range snapshot.Entries {
		if e.PullRequest.Number != job.PR || !strings.EqualFold(e.PullRequest.Repository, job.Repository) {
			continue
		}
		if e.CloneID == job.Clone {
			return e, true
		}
		fallback = &snapshot.Entries[i]
	}
	if fallback != nil {
		return *fallback, true
	}
	return reviewindex.Entry{}, false
}

func risk(raw json.RawMessage) (*int, string) {
	var stored struct {
		Status string `json:"status"`
		Score  *int   `json:"score"`
	}
	if len(raw) == 0 || json.Unmarshal(raw, &stored) != nil || stored.Score == nil || *stored.Score < 1 || *stored.Score > 5 {
		return nil, ""
	}
	return stored.Score, stored.Status
}

// reasonOf keeps only a short lower-case category, so nothing free-form
// reaches a notification.
func reasonOf(reason string) string {
	if len(reason) > 40 {
		return "internal"
	}
	for _, r := range reason {
		if (r < 'a' || r > 'z') && r != '_' && r != '-' {
			return "internal"
		}
	}
	return reason
}

func clip(title string) string {
	title = strings.Join(strings.Fields(title), " ")
	if utf8.RuneCountInString(title) <= maxTitle {
		return title
	}
	return string([]rune(title)[:maxTitle-1]) + "…"
}
