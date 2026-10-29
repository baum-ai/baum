// Package notices turns what the daemon already knows into the "notice"
// events the desktop app shows as operating system notifications: a review
// finished or failed, a review requested from the user, a change in the state
// of the user's own pull request (checks failing, a conflict, changes
// requested, ready to merge), a fix that pushed or failed, and an agent
// session that began waiting for its user's permission or answer. A notice
// is the smallest record that can name its pull request or session: kind,
// repository, number, the clone that routes the click, the title as GitHub
// returned it, the risk score the stored review already holds, a failure
// category, a fix's short commit, and a session's agent, id, branch and the
// one masked line its agent showed. It never carries review text, paths,
// transcripts or error messages.
//
// The daily cap pause is not a notice: the scheduler's own "automatic" event
// already says it, and the app reads it there.
package notices

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"github.com/baum-ai/baum/internal/discovery"
	"github.com/baum-ai/baum/internal/jobs"
	"github.com/baum-ai/baum/internal/livesessions"
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
	// The user's own pull requests, from transitions between discovery
	// snapshots.
	KindChecksFailed     = "checks_failed"
	KindConflict         = "conflict"
	KindChangesRequested = "changes_requested"
	KindReadyToMerge     = "ready_to_merge"
	// A fix job's outcome: pushed (with its short commit) or failed.
	KindFixPushed = "fix_pushed"
	KindFixFailed = "fix_failed"
	// An agent session began waiting for the user: to allow a tool, or to
	// answer it. A finished turn is not news.
	KindSessionPermission = "session_permission"
	KindSessionInput      = "session_input"
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
	// Commit is the short hash of the commit a fix pushed.
	Commit string `json:"commit,omitempty"`
	// Agent and Session name the agent session a session notice is about,
	// Branch the branch it works on and Message the line its agent showed,
	// already cleaned and masked by the tracker. PR is 0 when the branch has
	// no pull request.
	Agent   string `json:"agent,omitempty"`
	Session string `json:"session,omitempty"`
	Branch  string `json:"branch,omitempty"`
	Message string `json:"message,omitempty"`
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
	// authored holds, per lower-case repository, the states of the user's
	// own open pull requests in the previous snapshot. A pull request seen
	// for the first time records its state without a notice.
	authored map[string]map[int]prState
}

// prState is what the transitions of one of the user's pull requests are
// read from. An empty string is unknown, which never counts as a change.
type prState struct{ review, checks, merge string }

func (s *Service) ResetIdentity() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requested = nil
	s.authored = nil
}

// JobDone publishes the notice for a job that just finished: completed
// reviews and failures, and a fix that pushed or failed. Cancelled,
// interrupted and superseded jobs are not the user's news.
func (s *Service) JobDone(ctx context.Context, job jobs.Job) {
	// A review group's job names no pull request; its page shows the outcome.
	// A question's answer shows where it was asked.
	if job.Phase != "finished" || (job.State != "completed" && job.State != "failed") || job.Group != "" || job.Kind == jobs.KindChat {
		return
	}
	n := Notice{Kind: KindFinished, Repository: job.Repository, PR: job.PR, Clone: job.Clone}
	if job.Kind == jobs.KindFix {
		n.Kind = KindFixFailed
		if job.State == "failed" {
			n.Reason = reasonOf(job.Reason)
		} else if commit := shortCommit(job.Commit); commit != "" {
			n.Kind, n.Commit = KindFixPushed, commit
		} else {
			// A fix that completed without a commit has nothing to report.
			return
		}
		if s.Index != nil {
			if snapshot, err := s.Index(ctx); err == nil {
				if entry, ok := find(snapshot, job); ok {
					n.Title = clip(entry.PullRequest.Title)
				}
			}
		}
		s.Publish(n)
		return
	}
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
//
// When GitHub's team list was unavailable (TeamMembershipKnown is false), a
// false flag proves nothing: a request made through a team would be hidden.
// Such a pull request keeps the state the previous snapshot gave it, and one
// never seen before is recorded as requested without a notice, so the next
// complete snapshot cannot announce it as new.
//
// The same snapshots tell what happened to the user's own pull requests: its
// checks began failing, it began conflicting, a review requested changes, or
// it became ready to merge. A pull request that changed several ways between
// two snapshots gets one notice, the first of those in that order. A pull
// request first seen, or one that left the snapshot (closed or merged), gets
// none; a state GitHub did not report keeps the one before it.
func (s *Service) Observe(ctx context.Context, snapshot discovery.Snapshot) {
	name := strings.ToLower(snapshot.Repository)
	s.mu.Lock()
	if s.requested == nil {
		s.requested = map[string]map[int]bool{}
		s.authored = map[string]map[int]prState{}
	}
	before, seen := s.requested[name]
	ownBefore := s.authored[name]
	ownNow := map[int]prState{}
	var changed []Notice
	now := map[int]bool{}
	for _, pr := range snapshot.PullRequests {
		if pr.AuthoredByMe {
			prev, known := ownBefore[pr.Number]
			state := prState{
				review: keep(pr.Review, prev.review),
				checks: keep(pr.Checks, prev.checks),
				merge:  keep(pr.Merge, prev.merge),
			}
			ownNow[pr.Number] = state
			if seen && known {
				if kind := transition(prev, state); kind != "" {
					changed = append(changed, Notice{Kind: kind, PR: pr.Number, Title: clip(pr.Title)})
				}
			}
		}
		switch {
		case pr.AuthoredByMe:
		case pr.ReviewRequestedFromMe:
			now[pr.Number] = true
		case !pr.TeamMembershipKnown:
			if had, known := before[pr.Number]; had || !known {
				now[pr.Number] = true
			}
		}
	}
	s.requested[name] = now
	s.authored[name] = ownNow
	s.mu.Unlock()
	if !seen {
		return
	}
	// The repository as the registry stores it, like the job notices.
	repository, clone := snapshot.Repository, ""
	if clones, err := s.Registry.List(ctx); err == nil {
		for _, c := range clones {
			if c.Available && strings.EqualFold(c.Repository, snapshot.Repository) {
				repository, clone = c.Repository, c.ID
				break
			}
		}
	}
	for _, n := range changed {
		n.Repository, n.Clone = repository, clone
		s.Publish(n)
	}
	for _, pr := range snapshot.PullRequests {
		if now[pr.Number] && !before[pr.Number] && pr.ReviewRequestedFromMe {
			s.Publish(Notice{Kind: KindRequested, Repository: repository, PR: pr.Number, Clone: clone, Title: clip(pr.Title)})
		}
	}
}

// SessionWaiting publishes the notice for an agent session that began waiting
// for its user's permission or answer. The live sessions tracker calls it
// once per wait; a finished turn, or any other reason, is not a notice.
func (s *Service) SessionWaiting(ctx context.Context, session livesessions.Session) {
	kind := ""
	switch session.WaitingReason {
	case livesessions.WaitPermission:
		kind = KindSessionPermission
	case livesessions.WaitInput:
		kind = KindSessionInput
	default:
		return
	}
	n := Notice{Kind: kind, Repository: session.Repository, PR: session.PR, Agent: session.Agent, Session: session.ID,
		Branch: clip(session.Branch), Message: clip(session.WaitingMessage)}
	// The repository as the registry stores it, like the other notices: the
	// tracker's is in lower case.
	if s.Registry != nil {
		if clones, err := s.Registry.List(ctx); err == nil {
			for _, c := range clones {
				if strings.EqualFold(c.Repository, session.Repository) {
					n.Repository = c.Repository
					break
				}
			}
		}
	}
	s.Publish(n)
}

func keep(now, before string) string {
	if now == "" {
		return before
	}
	return now
}

// transition names the notice a pull request's change of state calls for, or
// "". "Ready" is the merge state the app's inbox shows as Ready to merge.
func transition(before, now prState) string {
	switch {
	case now.review == "changes_requested" && before.review != "changes_requested":
		return KindChangesRequested
	case now.merge == "conflict" && before.merge != "conflict":
		return KindConflict
	case now.checks == "failing" && before.checks != "failing":
		return KindChecksFailed
	case now.merge == "ready" && before.merge != "ready":
		return KindReadyToMerge
	}
	return ""
}

// shortCommit is the first seven characters of a commit hash, or "" for
// anything that is not one.
func shortCommit(commit string) string {
	if len(commit) < 7 || len(commit) > 64 {
		return ""
	}
	for _, r := range commit {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') && (r < 'A' || r > 'F') {
			return ""
		}
	}
	return commit[:7]
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

// clip drops format characters (bidirectional overrides, zero-width
// characters), turns control characters into spaces, and bounds the length.
func clip(title string) string {
	title = strings.Map(func(r rune) rune {
		switch {
		case unicode.Is(unicode.Cf, r):
			return -1
		case unicode.IsControl(r):
			return ' '
		}
		return r
	}, title)
	title = strings.Join(strings.Fields(title), " ")
	if utf8.RuneCountInString(title) <= maxTitle {
		return title
	}
	return string([]rune(title)[:maxTitle-1]) + "…"
}
