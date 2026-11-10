// Package inboxqueue builds the app's Inbox from the daemon's own state: the
// open pull requests with what each waits for, which saved inboxes hold each,
// and how many wait on the person.
//
// The total (Count) is the shared definition of the number on the sidebar's
// Inbox heading and the desktop app's dock badge and tray: the distinct
// unsnoozed actionable pull requests in the queue, each once however many
// clones hold it. It ignores saved inboxes, their badge switches and any
// filter. Which inboxes hold a pull request is decided here too, with the
// inbox filter language of internal/prfilter, so the app does not match every
// inbox query against every pull request itself.
package inboxqueue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/baum-ai/baum/internal/inboxes"
	"github.com/baum-ai/baum/internal/jobs"
	"github.com/baum-ai/baum/internal/prfilter"
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

// Kinds of Item: what the person does next about a pull request in the
// queue, as the app's UpNextKind, then Other for someone else's that waits
// on nothing from them and Waiting for their own that waits on others.
const (
	Review  = "review"
	Clone   = "clone"
	Fix     = "fix"
	Merge   = "merge"
	Update  = "update"
	Other   = "other"
	Waiting = ""
)

// Item is an open pull request as the inboxes match it: one per Key, the
// clone that speaks for it chosen as the app chooses.
type Item struct {
	Key string `json:"key"`
	// Kind is what it waits for, as an inbox's waiting= filter reads it: a
	// queue kind, Other, or Waiting (""). One an agent is fixing now is Fix,
	// with Fixing set, though it is not in the queue.
	Kind   string `json:"kind"`
	Fixing bool   `json:"fixing,omitempty"`
	// Snoozed is set while the person's snooze hides it.
	Snoozed bool `json:"snoozed,omitempty"`
	// Inboxes are the ids of the saved inboxes that hold it, in their order.
	Inboxes []string `json:"inboxes"`

	entry reviewindex.Entry
}

// Queued reports whether the item waits on the person: in the queue, not
// being fixed, waiting on others or someone else's.
func (i Item) Queued() bool {
	return i.Kind != Waiting && i.Kind != Other && !i.Fixing
}

// Listed reports whether an inbox's own count includes the item: what the
// queue holds and others' pull requests, unsnoozed.
func (i Item) Listed() bool {
	return !i.Snoozed && (i.Queued() || i.Kind == Other)
}

// Inbox is a saved inbox as matched: the query its parts fold into, and how
// many listed items it holds.
type Inbox struct {
	ID    string `json:"id"`
	Query string `json:"query"`
	Count int    `json:"count"`
}

// Queue is the Inbox: every open pull request with what it waits for and
// the inboxes that hold it, the total waiting on the person (Count) and each
// inbox's own count. Inboxes is nil when the saved inboxes could not be read.
type Queue struct {
	Count   int     `json:"count"`
	Items   []Item  `json:"items"`
	Inboxes []Inbox `json:"inboxes"`
}

// Items classifies the open pull requests of entries (the index's, in its
// order), one per Key, with queue the job queue's and groups the review
// groups, and marks those snoozed at now. Their Inboxes are empty.
func Items(entries []reviewindex.Entry, queue []jobs.Job, groups []reviewgroups.Group, snoozes Snoozes, now time.Time) []Item {
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
	out := []Item{}
	for _, key := range keys {
		e := best[key]
		if !isOpen(e) {
			continue
		}
		item := Item{Key: key, Kind: kindOf(e, queue), Inboxes: []string{}, entry: e}
		if item.Kind == Merge && stacked[key] {
			item.Kind = Waiting
		}
		if item.Kind == Waiting {
			switch {
			case !e.PullRequest.AuthoredByMe:
				item.Kind = Other
			case fixing(e, queue):
				item.Kind, item.Fixing = Fix, true
			}
		}
		head, held := snoozes.Held[key]
		item.Snoozed = snoozes.Snoozed[key] > ms || held && head == e.PullRequest.HeadSHA
		out = append(out, item)
	}
	return out
}

// Build is the Queue: Items, each matched against the saved inboxes in
// their order.
func Build(entries []reviewindex.Entry, queue []jobs.Job, groups []reviewgroups.Group, snoozes Snoozes, saved []inboxes.Inbox, now time.Time) Queue {
	return match(Items(entries, queue, groups, snoozes, now), saved)
}

func match(items []Item, saved []inboxes.Inbox) Queue {
	out := Queue{Items: items, Inboxes: make([]Inbox, 0, len(saved))}
	for _, inbox := range saved {
		query := Folded(inbox)
		test := prfilter.Compile(query)
		counted := Inbox{ID: inbox.ID, Query: query}
		for i := range items {
			if test(items[i].entry, items[i].Kind) {
				items[i].Inboxes = append(items[i].Inboxes, inbox.ID)
				if items[i].Listed() {
					counted.Count++
				}
			}
		}
		out.Inboxes = append(out.Inboxes, counted)
	}
	for _, item := range items {
		if item.Queued() && !item.Snoozed {
			out.Count++
		}
	}
	return out
}

// Folded is the inbox's query with its older parts (what, owners, risk,
// repositories, bots) folded in, as the app matches it. An unknown what or
// risk reads as "any", as the app reads it.
func Folded(inbox inboxes.Inbox) string {
	what, risk := inbox.What, inbox.Risk
	switch what {
	case "review", "merge", "agent", "mine":
	default:
		what = "any"
	}
	if risk != "3" && risk != "4" {
		risk = "any"
	}
	return prfilter.FoldedQuery(prfilter.Parts{
		What: what, Owners: inbox.Owners, Risk: risk,
		Repositories: inbox.Repositories, Query: inbox.Query, NoBots: inbox.NoBots,
	})
}

// Matching is the keys of the items query holds, in their order: what an
// inbox with that query would hold, for a filter not saved yet.
func Matching(items []Item, query string) []string {
	test := prfilter.Compile(query)
	out := []string{}
	for _, item := range items {
		if test(item.entry, item.Kind) {
			out = append(out, item.Key)
		}
	}
	return out
}

// Count is the number of pull requests in the Inbox: each open one asking
// for the user's review or assigned to them, and each of their own with
// something for them to do (fix, merge or update), less those snoozed at
// now. Saved inboxes play no part.
func Count(entries []reviewindex.Entry, queue []jobs.Job, groups []reviewgroups.Group, snoozes Snoozes, now time.Time) int {
	return Build(entries, queue, groups, snoozes, nil, now).Count
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
	// A pull request assigned to the viewer waits on them like a requested
	// review, unless they wrote it.
	mine := pr.ReviewRequestedFromMe || pr.AssignedToMe && !pr.AuthoredByMe
	switch {
	case !isOpen(e):
		return ""
	case mine && e.NeedsClone:
		return Clone
	case mine:
		return Review
	case !pr.AuthoredByMe:
		return ""
	// An agent already fixing it leaves nothing for the person until it ends.
	case fixing(e, queue):
		return ""
	case blocked(e):
		return Fix
	// GitHub can call a head just pushed clean before its checks are queued.
	case e.Merge == "ready" && !pr.Draft && e.Checks != "running":
		return Merge
	case !pr.Draft && !pr.Fork && pr.Behind && pr.ViewerCanPush != nil && *pr.ViewerCanPush:
		return Update
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

// Service builds the Queue from the daemon's own index, job queue, groups,
// saved ui-state and saved inboxes, read on each request.
type Service struct {
	Index   func(context.Context) (reviewindex.Snapshot, error)
	Jobs    func() []jobs.Job
	Groups  func(context.Context) ([]reviewgroups.Group, error)
	Snoozes func() (json.RawMessage, error)
	// Inboxes reads the saved inboxes. When it is nil or fails, no inbox
	// holds anything and the Queue's Inboxes is nil.
	Inboxes func() ([]inboxes.Inbox, error)
	Now     func() time.Time
}

func (s *Service) items(ctx context.Context) ([]Item, error) {
	snapshot, err := s.Index(ctx)
	if err != nil {
		return nil, err
	}
	groups, err := s.Groups(ctx)
	if err != nil {
		return nil, err
	}
	raw, err := s.Snoozes()
	if err != nil {
		return nil, err
	}
	snoozes, err := ParseSnoozes(raw)
	if err != nil {
		return nil, err
	}
	now := time.Now
	if s.Now != nil {
		now = s.Now
	}
	return Items(snapshot.Entries, s.Jobs(), groups, snoozes, now()), nil
}

// Queue is the Inbox now, matched against the saved inboxes when they read.
func (s *Service) Queue(ctx context.Context) (Queue, error) {
	items, err := s.items(ctx)
	if err != nil {
		return Queue{}, err
	}
	var saved []inboxes.Inbox
	readable := s.Inboxes != nil
	if readable {
		if saved, err = s.Inboxes(); err != nil {
			readable = false
		}
	}
	out := match(items, saved)
	if !readable {
		out.Inboxes = nil
	}
	return out, nil
}

// Count is the Inbox total now.
func (s *Service) Count(ctx context.Context) (int, error) {
	items, err := s.items(ctx)
	if err != nil {
		return 0, err
	}
	return match(items, nil).Count, nil
}

// maxDraft bounds a draft query: far over what an inbox saves, so the app
// learns why from the query's problem rather than a refusal.
const maxDraft = 8 << 10

// Draft is what a query not saved yet would hold: the keys of the items it
// matches, or why it cannot be read (Problem), in which case it holds them
// all as a filter that does not read narrows nothing.
type Draft struct {
	Query   string   `json:"query"`
	Problem string   `json:"problem"`
	Matches []string `json:"matches"`
}

// Handler serves GET /api/inbox, the Queue, and GET /api/inbox?query=Q, the
// Draft of Q. The host applies its launch authentication first.
func (s *Service) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", "GET")
			http.Error(w, "use GET", http.StatusMethodNotAllowed)
			return
		}
		params := r.URL.Query()
		if params.Has("query") {
			query := params.Get("query")
			if len(query) > maxDraft {
				http.Error(w, "query too long", http.StatusRequestEntityTooLarge)
				return
			}
			items, err := s.items(r.Context())
			if err != nil {
				unavailable(w, err)
				return
			}
			respond(w, Draft{Query: query, Problem: prfilter.Problem(query), Matches: Matching(items, query)})
			return
		}
		queue, err := s.Queue(r.Context())
		if err != nil {
			unavailable(w, err)
			return
		}
		respond(w, queue)
	})
}

// CountHandler answers GET /api/index?view=waiting with {"waiting":N}, the
// Inbox count, which the desktop app polls for its badge and tray; every
// other request goes to index. The host applies its launch authentication
// first.
func (s *Service) CountHandler(index http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/index" || r.Method != http.MethodGet || r.URL.Query().Get("view") != "waiting" {
			index.ServeHTTP(w, r)
			return
		}
		n, err := s.Count(r.Context())
		if err != nil {
			unavailable(w, err)
			return
		}
		respond(w, map[string]int{"waiting": n})
	})
}

func unavailable(w http.ResponseWriter, err error) {
	if !errors.Is(err, context.Canceled) {
		http.Error(w, "inbox unavailable", http.StatusInternalServerError)
	}
}

func respond(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(v)
}
