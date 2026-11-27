// Package githubreads is the read-only GitHub answers baum's MCP tools give:
// a pull request's review threads and timeline, and an issue. Answers are
// cached briefly in memory, per GitHub account.
package githubreads

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/baum-ai/baum/internal/fix"
	"github.com/baum-ai/baum/internal/github"
	"github.com/baum-ai/baum/internal/prupdate"
)

// Thread states Threads takes.
const (
	Unresolved = "unresolved"
	Resolved   = "resolved"
	All        = "all"
)

// Bounds on untrusted text, in characters (the issue body in bytes).
const (
	maxExcerpt = 200
	maxMessage = 4000
	maxBody    = 64 << 10
)

// Default cache lifetimes.
const (
	DefaultTTL      = time.Minute
	DefaultIssueTTL = 5 * time.Minute
)

const (
	rateLimited = "GitHub is limiting requests right now. Try again in a minute."
	reconnect   = "GitHub rejected the saved connection. Reconnect GitHub in Settings and try again."
)

// Thread is a review thread as the MCP tools list it: prupdate.Thread and
// whether it is resolved.
type Thread struct {
	prupdate.Thread
	Resolved bool `json:"resolved"`
}

// Threads lists a pull request's review threads.
type Threads struct {
	State   string   `json:"state"`
	Threads []Thread `json:"threads"`
	// Capped: the pull request has more threads than baum read.
	Capped bool `json:"capped"`
}

// Event is one event of a pull request's timeline, with the fields its type
// uses. Commit is an object for a committed event and an oid string for a
// merged one.
type Event struct {
	Type       string     `json:"type"`
	At         string     `json:"at"`
	Actor      string     `json:"actor"`
	Commit     any        `json:"commit,omitempty"`
	Before     string     `json:"before,omitempty"`
	After      string     `json:"after,omitempty"`
	State      string     `json:"state,omitempty"`
	Body       string     `json:"body,omitempty"`
	URL        string     `json:"url,omitempty"`
	Label      string     `json:"label,omitempty"`
	Assignee   string     `json:"assignee,omitempty"`
	Reviewer   string     `json:"reviewer,omitempty"`
	From       string     `json:"from,omitempty"`
	To         string     `json:"to,omitempty"`
	Source     *Reference `json:"source,omitempty"`
	WillClose  *bool      `json:"will_close,omitempty"`
	CommitOID  string     `json:"commit_oid,omitempty"`
	Repository string     `json:"repository,omitempty"`
}

// Commit is a commit of a committed event.
type Commit struct {
	OID      string `json:"oid"`
	Headline string `json:"headline"`
}

// Reference is a pull request or an issue that mentions another.
type Reference struct {
	Kind       string `json:"kind"`
	Repository string `json:"repository"`
	Number     int    `json:"number"`
	Title      string `json:"title"`
	State      string `json:"state"`
	URL        string `json:"url"`
}

// Timeline lists a pull request's events, oldest first.
type Timeline struct {
	Events []Event `json:"events"`
	// Capped: the pull request has more events than baum read.
	Capped bool `json:"capped"`
}

// IssueComment is one comment on an issue, as plain text.
type IssueComment struct {
	Author    string `json:"author"`
	Body      string `json:"body"`
	CreatedAt string `json:"created_at"`
	URL       string `json:"url"`
}

// LinkedPullRequest is a pull request that closes or mentions an issue.
type LinkedPullRequest struct {
	Repository string `json:"repository"`
	Number     int    `json:"number"`
	Title      string `json:"title"`
	State      string `json:"state"`
	URL        string `json:"url"`
}

// Issue is a GitHub issue as the MCP tools show it.
type Issue struct {
	Repository         string              `json:"repository"`
	Number             int                 `json:"number"`
	Title              string              `json:"title"`
	State              string              `json:"state"`
	StateReason        string              `json:"state_reason"`
	URL                string              `json:"url"`
	Author             string              `json:"author"`
	CreatedAt          string              `json:"created_at"`
	ClosedAt           string              `json:"closed_at"`
	Body               string              `json:"body"`
	Labels             []string            `json:"labels"`
	CommentsTotal      int                 `json:"comments_total"`
	Comments           []IssueComment      `json:"comments"`
	LinkedPullRequests []LinkedPullRequest `json:"linked_pull_requests"`
}

// Service reads through one GitHub client and caches the answers, so an agent
// asking again within a minute (five for an issue) costs GitHub nothing. It is
// safe for concurrent use.
type Service struct {
	Client *github.Client
	// TTL is how long a pull request's answers are kept; IssueTTL an
	// issue's. Zero means DefaultTTL and DefaultIssueTTL.
	TTL      time.Duration
	IssueTTL time.Duration

	// Now is the clock; tests set it.
	Now func() time.Time

	mu      sync.Mutex
	entries map[string]entry
}

type entry struct {
	at    time.Time
	ttl   time.Duration
	value any
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Service) ttl(kind string) time.Duration {
	if kind == "issue" {
		if s.IssueTTL > 0 {
			return s.IssueTTL
		}
		return DefaultIssueTTL
	}
	if s.TTL > 0 {
		return s.TTL
	}
	return DefaultTTL
}

// cached returns the answer for kind of repository#number, read by read when
// no fresh one is cached. The key holds the GitHub account's identity, so a
// sign-in change never serves another account's answer.
func cached[T any](ctx context.Context, s *Service, repo github.Repository, number int, kind string, read func() (T, error)) (T, error) {
	var zero T
	reader, err := s.Client.REST(ctx, repo)
	if err != nil {
		return zero, failure(err)
	}
	key := reader.CacheIdentity() + "|" + repo.String() + "|" + kind + "|" + strconv.Itoa(number)
	now, ttl := s.now(), s.ttl(kind)
	s.mu.Lock()
	if e, ok := s.entries[key]; ok && now.Sub(e.at) < e.ttl {
		s.mu.Unlock()
		return e.value.(T), nil
	}
	s.mu.Unlock()
	value, err := read()
	if err != nil {
		return zero, failure(err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.entries == nil {
		s.entries = map[string]entry{}
	}
	for k, e := range s.entries {
		if now.Sub(e.at) >= e.ttl {
			delete(s.entries, k)
		}
	}
	s.entries[key] = entry{at: now, ttl: ttl, value: value}
	return value, nil
}

// failure words err as a refusal prupdate.AsError gives the handler.
func failure(err error) error {
	var limited *github.RateLimitedError
	var merge *github.MergeError
	switch {
	case errors.As(err, &limited):
		return &prupdate.Error{Code: prupdate.Failed, Message: rateLimited}
	case errors.Is(err, github.ErrNotFound):
		return &prupdate.Error{Code: prupdate.NotFound, Message: "GitHub does not show this to your account."}
	case errors.As(err, &merge):
		switch {
		case merge.Code == github.MergeForbidden:
			return &prupdate.Error{Code: prupdate.Forbidden, Message: reconnect}
		case strings.Contains(merge.Message, "limiting requests"):
			return &prupdate.Error{Code: prupdate.Failed, Message: rateLimited}
		}
	}
	return &prupdate.Error{Code: prupdate.Failed, Message: "baum could not read this from GitHub."}
}

// Threads lists the pull request's review threads in state (unresolved,
// resolved or all).
func (s *Service) Threads(ctx context.Context, repo github.Repository, number int, state string) (Threads, error) {
	if state == "" {
		state = Unresolved
	}
	all, err := cached(ctx, s, repo, number, "threads", func() (github.ReviewThreads, error) {
		return s.Client.AllThreads(ctx, repo, number)
	})
	if err != nil {
		return Threads{}, err
	}
	out := Threads{State: state, Threads: []Thread{}, Capped: all.Capped}
	for _, t := range all.All {
		if (state == Unresolved && t.Resolved) || (state == Resolved && !t.Resolved) {
			continue
		}
		first := t.Comments[0]
		messages := make([]prupdate.ThreadComment, 0, len(t.Comments))
		for _, c := range t.Comments {
			messages = append(messages, prupdate.ThreadComment{Author: c.Author, Body: bounded(fix.PlainText(c.Body), maxMessage), Markdown: strings.ToValidUTF8(c.Body, ""), URL: c.URL})
		}
		out.Threads = append(out.Threads, Thread{Thread: prupdate.Thread{ID: t.ID, Path: t.Path, Line: t.Line, Outdated: t.Outdated, Author: first.Author, Excerpt: excerpt(first.Body), Comments: max(t.Total, len(t.Comments)), URL: t.URL, Messages: messages, CanResolve: t.CanResolve}, Resolved: t.Resolved})
	}
	return out, nil
}

// Timeline lists the pull request's events, oldest first.
func (s *Service) Timeline(ctx context.Context, repo github.Repository, number int) (Timeline, error) {
	read, err := cached(ctx, s, repo, number, "timeline", func() (github.Timeline, error) {
		return s.Client.PullRequestTimeline(ctx, repo, number)
	})
	if err != nil {
		return Timeline{}, err
	}
	out := Timeline{Events: []Event{}, Capped: read.Capped}
	for _, e := range read.Events {
		ev := Event{Type: e.Type, At: e.At.UTC().Format(time.RFC3339), Actor: e.Actor, Before: e.Before, After: e.After, State: e.State, Body: bounded(fix.PlainText(e.Body), maxMessage), URL: e.URL, Label: e.Label, Assignee: e.Assignee, Reviewer: e.Reviewer, From: bounded(e.From, maxExcerpt), To: bounded(e.To, maxExcerpt), Repository: e.Repository}
		switch e.Type {
		case "committed":
			ev.Commit = Commit{OID: e.Commit, Headline: bounded(e.Headline, maxExcerpt)}
		case "merged":
			if e.Commit != "" {
				ev.Commit = e.Commit
			}
		case "referenced":
			ev.CommitOID = e.Commit
		case "cross_referenced":
			willClose := e.WillClose
			ev.WillClose = &willClose
			if r := e.Source; r != nil {
				ev.Source = &Reference{Kind: r.Kind, Repository: r.Repository, Number: r.Number, Title: bounded(r.Title, maxExcerpt), State: r.State, URL: r.URL}
			}
		}
		out.Events = append(out.Events, ev)
	}
	return out, nil
}

// Issue reads one issue. A number that is a pull request fails as not found.
func (s *Service) Issue(ctx context.Context, repo github.Repository, number int) (Issue, error) {
	read, err := cached(ctx, s, repo, number, "issue", func() (github.Issue, error) {
		return s.Client.ReadIssue(ctx, repo, number)
	})
	if err != nil {
		var refusal *prupdate.Error
		if errors.As(err, &refusal) && refusal.Code == prupdate.NotFound {
			return Issue{}, &prupdate.Error{Code: prupdate.NotFound, Message: "GitHub has no issue with this number in the repository. It may be a pull request."}
		}
		return Issue{}, err
	}
	out := Issue{Repository: read.Repository, Number: read.Number, Title: bounded(read.Title, maxExcerpt), State: read.State, StateReason: read.StateReason, URL: read.URL, Author: read.Author, CreatedAt: read.CreatedAt.UTC().Format(time.RFC3339), Body: boundedBytes(fix.PlainText(read.Body), maxBody), Labels: []string{}, CommentsTotal: read.CommentsTotal, Comments: []IssueComment{}, LinkedPullRequests: []LinkedPullRequest{}}
	if read.ClosedAt != nil {
		out.ClosedAt = read.ClosedAt.UTC().Format(time.RFC3339)
	}
	out.Labels = append(out.Labels, read.Labels...)
	for _, c := range read.Comments {
		out.Comments = append(out.Comments, IssueComment{Author: c.Author, Body: bounded(fix.PlainText(c.Body), maxMessage), CreatedAt: c.CreatedAt.UTC().Format(time.RFC3339), URL: c.URL})
	}
	for _, pr := range read.LinkedPullRequests {
		out.LinkedPullRequests = append(out.LinkedPullRequests, LinkedPullRequest{Repository: pr.Repository, Number: pr.Number, Title: bounded(pr.Title, maxExcerpt), State: pr.State, URL: pr.URL})
	}
	return out, nil
}

// bounded is text with invalid UTF-8 dropped, at most limit characters.
func bounded(text string, limit int) string {
	text = strings.ToValidUTF8(text, "")
	if utf8.RuneCountInString(text) > limit {
		text = strings.TrimSpace(string([]rune(text)[:limit-1])) + "…"
	}
	return text
}

// boundedBytes is text with invalid UTF-8 dropped, at most limit bytes.
func boundedBytes(text string, limit int) string {
	text = strings.ToValidUTF8(text, "")
	if len(text) <= limit {
		return text
	}
	text = text[:limit]
	for !utf8.ValidString(text) {
		text = text[:len(text)-1]
	}
	return text + "…"
}

// excerpt is a comment's headline, at most maxExcerpt characters.
func excerpt(body string) string { return bounded(fix.Headline(body), maxExcerpt) }
