// Package inboxcount counts the pull requests in the app's Inbox, so the
// desktop app's dock badge and tray show the number the app's sidebar does.
//
// It mirrors the app's queue (upNext, kindOf and the snooze filter in
// app/src/up-next.tsx). Saved inboxes whose badge is off are not applied:
// that needs the inbox filter language, which only the app has for now.
package inboxcount

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/baum-ai/baum/internal/jobs"
	"github.com/baum-ai/baum/internal/reviewgroups"
	"github.com/baum-ai/baum/internal/reviewindex"
)

// Snoozes is what the app saved as ui-state "inbox" that hides a pull request
// from the queue: until a time (Unix milliseconds) or while its head stays
// the one it was held on. Both are keyed by Key.
type Snoozes struct {
	Snoozed map[string]int64  `json:"snoozed"`
	Held    map[string]string `json:"held"`
}

// ParseSnoozes reads the ui-state "inbox" value; null, as before the app
// saved one, hides nothing.
func ParseSnoozes(raw json.RawMessage) (Snoozes, error) {
	var out Snoozes
	if len(raw) == 0 || string(raw) == "null" {
		return out, nil
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return Snoozes{}, fmt.Errorf("unreadable inbox state: %w", err)
	}
	return out, nil
}

// Key names a pull request across the clones that hold it, as the app's
// upNextKey does.
func Key(pr reviewindex.Entry) string {
	return fmt.Sprintf("%s#%d", strings.ToLower(pr.PullRequest.Repository), pr.PullRequest.Number)
}

// Count is the number of pull requests in the Inbox: each open one asking
// for the user's review, and each of their own with something for them to
// do (fix, merge or update), less those snoozed at now. entries are the
// index's, in its order; jobs the queue's; groups the review groups.
func Count(entries []reviewindex.Entry, queue []jobs.Job, groups []reviewgroups.Group, snoozes Snoozes, now time.Time) int {
	// Of the clones that hold a pull request, the one with a readable saved
	// review and a clone speaks for it; a tie keeps the first.
	score := func(e reviewindex.Entry) int {
		n := 0
		if e.VersionCount > 0 {
			n += 4
		}
		if e.StoreError == "" {
			n += 2
		}
		if !e.NeedsClone {
			n++
		}
		return n
	}
	var keys []string
	best := map[string]reviewindex.Entry{}
	for _, e := range entries {
		key := Key(e)
		current, ok := best[key]
		if !ok {
			keys = append(keys, key)
		}
		if !ok || score(e) > score(current) {
			best[key] = e
		}
	}
	stacked := stackedOn(keys, best, groups)
	ms := now.UnixMilli()
	n := 0
	for _, key := range keys {
		e := best[key]
		kind := kindOf(e, queue)
		if kind == "" || kind == "merge" && stacked[key] {
			continue
		}
		if head, held := snoozes.Held[key]; snoozes.Snoozed[key] > ms || held && head == e.PullRequest.HeadSHA {
			continue
		}
		n++
	}
	return n
}

func isOpen(e reviewindex.Entry) bool {
	return !e.Historical && (e.PullRequest.State == "" || strings.EqualFold(e.PullRequest.State, "OPEN"))
}

// blocked: the author has changes requested, a conflict, failing checks or
// review threads left open.
func blocked(e reviewindex.Entry) bool {
	return e.Review == "changes_requested" || e.Merge == "conflict" || e.Checks == "failing" || e.Unresolved != nil && *e.Unresolved > 0
}

func kindOf(e reviewindex.Entry, queue []jobs.Job) string {
	pr := e.PullRequest
	switch {
	case !isOpen(e):
		return ""
	case pr.ReviewRequestedFromMe && e.NeedsClone:
		return "clone"
	case pr.ReviewRequestedFromMe:
		return "review"
	case !pr.AuthoredByMe:
		return ""
	// An agent already fixing it leaves nothing for the person until it ends.
	case fixing(e, queue):
		return ""
	case blocked(e):
		return "fix"
	// GitHub can call a head just pushed clean before its checks are queued.
	case e.Merge == "ready" && !pr.Draft && e.Checks != "running":
		return "merge"
	case !pr.Draft && !pr.Fork && pr.Behind && pr.ViewerCanPush != nil && *pr.ViewerCanPush:
		return "update"
	}
	return ""
}

// fixing reports a queued or running fix of the pull request.
func fixing(e reviewindex.Entry, queue []jobs.Job) bool {
	for _, job := range queue {
		if job.Kind == jobs.KindFix && job.Group == "" && job.PR == e.PullRequest.Number &&
			strings.EqualFold(job.Repository, e.PullRequest.Repository) &&
			(job.State == "queued" || job.State == "running") {
			return true
		}
	}
	return false
}

// stackedOn reports, by key, the open members of stack groups that have
// another open member under them: GitHub would merge them, but the one under
// must merge first. A member is stacked on the one whose head branch is its
// base.
func stackedOn(keys []string, best map[string]reviewindex.Entry, groups []reviewgroups.Group) map[string]bool {
	out := map[string]bool{}
	for _, group := range groups {
		if !group.Stack {
			continue
		}
		in := map[int]bool{}
		for _, number := range group.Members {
			in[number] = true
		}
		var members []reviewindex.Entry
		byHead := map[string]reviewindex.Entry{}
		for _, key := range keys {
			e := best[key]
			if isOpen(e) && strings.EqualFold(e.PullRequest.Repository, group.Repository) && in[e.PullRequest.Number] {
				members = append(members, e)
				if e.PullRequest.HeadRef != "" {
					byHead[e.PullRequest.HeadRef] = e
				}
			}
		}
		for _, e := range members {
			if base, ok := byHead[e.PullRequest.BaseRef]; ok && base.PullRequest.Number != e.PullRequest.Number {
				out[Key(e)] = true
			}
		}
	}
	return out
}

// Service serves the count from the daemon's own index, queue, groups and
// saved ui-state.
type Service struct {
	Index   func(context.Context) (reviewindex.Snapshot, error)
	Jobs    func() []jobs.Job
	Groups  func(context.Context) ([]reviewgroups.Group, error)
	Snoozes func() (json.RawMessage, error)
	Now     func() time.Time
}

func (s *Service) Count(ctx context.Context) (int, error) {
	snapshot, err := s.Index(ctx)
	if err != nil {
		return 0, err
	}
	groups, err := s.Groups(ctx)
	if err != nil {
		return 0, err
	}
	raw, err := s.Snoozes()
	if err != nil {
		return 0, err
	}
	snoozes, err := ParseSnoozes(raw)
	if err != nil {
		return 0, err
	}
	now := time.Now
	if s.Now != nil {
		now = s.Now
	}
	return Count(snapshot.Entries, s.Jobs(), groups, snoozes, now()), nil
}

// Handler answers GET /api/index?view=waiting with {"waiting":N}, the Inbox
// count, which the desktop app polls for its badge and tray; every other
// request goes to index. The host applies its launch authentication first.
func (s *Service) Handler(index http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/index" || r.Method != http.MethodGet || r.URL.Query().Get("view") != "waiting" {
			index.ServeHTTP(w, r)
			return
		}
		n, err := s.Count(r.Context())
		if err != nil {
			if !errors.Is(err, context.Canceled) {
				http.Error(w, "inbox count unavailable", http.StatusInternalServerError)
			}
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(map[string]int{"waiting": n})
	})
}
