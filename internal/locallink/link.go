// Package locallink links the coding-agent sessions recorded on this machine
// to the commits of a pull request, using only this machine: the clone
// containing a directory supplies the commits and the home-directory and
// checkpoint stores supply the sessions. It is the local counterpart of the
// hosted service's linkage resolver and runs the same matcher over the same
// kinds of evidence.
//
// It never fetches. Commits the clone does not hold make the answer
// unavailable rather than being downloaded, and nothing here contacts a
// server. Every piece of text it returns has been redacted the way
// "baum sessions" redacts its listing.
//
// A caller such as "baum view" serves the Result; a later "baum bundle"
// evidence step can build on Resolve without an HTTP server.
package locallink

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/lastpersonlabs/baum/bundle"
	"github.com/lastpersonlabs/baum/internal/gitlocal"
	"github.com/lastpersonlabs/baum/linkage"
	"github.com/lastpersonlabs/baum/linkage/sessioncorrelate"
	"github.com/lastpersonlabs/baum/linkage/steps"
	"github.com/lastpersonlabs/baum/redact"
	"github.com/lastpersonlabs/baum/sessions"
	"github.com/lastpersonlabs/baum/sessions/session"
)

// Codes say why a Result is unavailable, for a reader to branch on. The
// Reason beside each is for people.
const (
	// CodeSample is the built-in sample document, which belongs to no clone.
	CodeSample = "sample"
	// CodeNoHead is a document that does not name a repository and full head
	// and base commit ids.
	CodeNoHead = "no_head"
	// CodeNotAClone is a directory outside a git clone with a working tree.
	CodeNotAClone = "not_a_clone"
	// CodeRemoteMismatch is a clone none of whose remotes is the document's
	// repository.
	CodeRemoteMismatch = "remote_mismatch"
	// CodeCommitsMissing is a clone that lacks the pull request's commits.
	CodeCommitsMissing = "commits_missing"
	// CodePartialClone is a partial clone that has the pull request's commits
	// but not the trees or blobs reading them needs.
	CodePartialClone = "partial_clone"
	// CodeAmbiguousCommit is an abbreviated head or base that names more than
	// one commit in the clone.
	CodeAmbiguousCommit = "ambiguous_commit"
	// CodeTooManyCommits is a pull request with more commits than are listed.
	CodeTooManyCommits = "too_many_commits"
	// CodeError is anything else that stopped the read.
	CodeError = "error"
)

// Request says which pull request to link and where to look.
type Request struct {
	// Dir is a directory inside the clone; "" means the current directory.
	Dir string
	// Repository is the pull request's "owner/name".
	Repository string
	// Head, Base and MergeBase bound the pull request, as full or abbreviated
	// (7 or more hex digits) commit ids. The pull request's commits are those
	// from the merge base to the head, so MergeBase is used when the clone
	// has it, and Base otherwise. MergeBase may be empty.
	Head, Base, MergeBase string
	// Sessions narrows or redirects the stores read; the zero value reads
	// the defaults.
	Sessions sessions.Options
	// Warn receives one line for each session store file that could not be
	// read, as "baum sessions" prints them. It defaults to discarding them.
	Warn io.Writer
}

// Result is the outcome of Resolve. Its JSON is
//
//	{"available":true,"sessions":[...],"commits":[...]}
//
// or {"available":false,"code":"...","reason":"..."}.
type Result struct {
	Available bool
	// Code and Reason are set when the result is unavailable.
	Code, Reason string
	// Sessions are those with at least one matched commit, oldest first.
	Sessions []Session
	// Commits are every commit of the pull request, oldest first.
	Commits []Commit
}

// Session is a linked session. All text is redacted.
type Session struct {
	// ID is the vendor's own session id, stable across runs.
	ID string `json:"id"`
	// Source is the store the session was read from and Vendor the agent
	// that ran; they differ for sessions an Entire checkpoint recorded.
	Source      string          `json:"source"`
	Vendor      string          `json:"vendor"`
	Title       string          `json:"title,omitempty"`
	Branch      string          `json:"branch,omitempty"`
	StartedAt   *time.Time      `json:"started_at,omitempty"`
	EndedAt     *time.Time      `json:"ended_at,omitempty"`
	Outcome     session.Outcome `json:"outcome"`
	EditedFiles int             `json:"edited_files"`
}

// Commit is one commit of the pull request. All text is redacted.
type Commit struct {
	OID     string `json:"oid"`
	Subject string `json:"subject"`
	// Author is the author's name; an e-mail address is never included.
	Author      string `json:"author,omitempty"`
	CommittedAt string `json:"committed_at,omitempty"`
	// Links are the sessions matched to the commit, strongest first.
	Links []Link `json:"links"`
}

// Link is one session matched to a commit.
type Link struct {
	Session string `json:"session"`
	// Method is the linkage step that matched: commit_ref, start_commit or
	// files_overlap.
	Method     string  `json:"method"`
	Confidence float64 `json:"confidence"`
}

// Unavailable builds the Result for a pull request that cannot be linked.
func Unavailable(code, reason string) Result {
	return Result{Code: code, Reason: reason}
}

// MarshalJSON writes the shape documented on Result; an available result
// always carries both lists, empty or not.
func (r Result) MarshalJSON() ([]byte, error) {
	if !r.Available {
		return json.Marshal(struct {
			Available bool   `json:"available"`
			Code      string `json:"code"`
			Reason    string `json:"reason"`
		}{false, r.Code, r.Reason})
	}
	linked, commits := r.Sessions, r.Commits
	if linked == nil {
		linked = []Session{}
	}
	if commits == nil {
		commits = []Commit{}
	}
	return json.Marshal(struct {
		Available bool      `json:"available"`
		Sessions  []Session `json:"sessions"`
		Commits   []Commit  `json:"commits"`
	}{true, linked, commits})
}

// maxSubject bounds the subject a commit carries, in characters.
const maxSubject = 200

// Resolve reads the pull request's commits from the clone containing
// req.Dir and the sessions recorded for that clone, and matches them. A
// pull request that cannot be linked from this machine is an unavailable
// Result, not an error; an error is a failure to read something that should
// have been readable.
func Resolve(ctx context.Context, req Request) (Result, error) {
	if req.Repository == "" || req.Head == "" || (req.Base == "" && req.MergeBase == "") {
		return Unavailable(CodeNoHead, "the document does not name its repository and head and base commits"), nil
	}
	dir := req.Dir
	if dir == "" {
		dir = "."
	}
	repo, err := gitlocal.Open(ctx, dir, gitlocal.Options{})
	if err != nil {
		var gitErr *gitlocal.Error
		if errors.As(err, &gitErr) && gitErr.Command == "rev-parse" {
			return Unavailable(CodeNotAClone, "the current directory is not inside a git clone"), nil
		}
		return Result{}, fmt.Errorf("opening the clone: %w", err)
	}
	defer func() { _ = repo.Close() }()

	remotes, err := repo.RemoteURLs(ctx)
	if err != nil {
		return Result{}, fmt.Errorf("reading the clone's remotes: %w", err)
	}
	if !anyRemoteIs(remotes, req.Repository) {
		return Unavailable(CodeRemoteMismatch, remoteReason(remotes, req.Repository)), nil
	}
	head, start, unavailable, err := resolveBounds(ctx, repo, req)
	if err != nil {
		return Result{}, err
	}
	if unavailable != nil {
		return *unavailable, nil
	}
	commits, err := repo.Commits(ctx, start, head)
	switch {
	case errors.Is(err, gitlocal.ErrInvalidInput):
		return Unavailable(CodeNoHead, "the document's head and base are not commit ids"), nil
	case errors.Is(err, gitlocal.ErrPartialClone):
		return Unavailable(CodePartialClone, "this is a partial clone that lacks objects needed to read the pull request; fetch them with git to see linked sessions"), nil
	case errors.Is(err, gitlocal.ErrObjectNotFound):
		return Unavailable(CodeCommitsMissing, "this clone does not have the pull request's commits; fetch them with git to see linked sessions"), nil
	case errors.Is(err, bundle.ErrOutputTooLarge):
		return Unavailable(CodeTooManyCommits, "the pull request has too many commits to link here"), nil
	case err != nil:
		return Result{}, fmt.Errorf("reading the pull request's commits: %w", err)
	}
	engine, err := redact.New()
	if err != nil {
		return Result{}, err
	}
	if len(commits) == 0 {
		return Result{Available: true, Sessions: []Session{}, Commits: []Commit{}}, nil
	}

	load := req.Sessions
	if since, ok := sessionsSince(commits); ok && load.Since.Before(since) {
		load.Since = since
	}
	found, report, err := sessions.Load(ctx, dir, load)
	if err != nil {
		return Result{}, fmt.Errorf("reading sessions: %w", err)
	}
	if req.Warn != nil {
		for _, skipped := range report.Skipped {
			_, _ = fmt.Fprintf(req.Warn, "warning: skipped %s: %s\n", skipped.Path, skipped.Reason)
		}
		for _, failure := range report.Failures {
			_, _ = fmt.Fprintf(req.Warn, "warning: %s session %s: %v\n", failure.Source, failure.Path, failure.Err)
		}
	}
	return match(ctx, engine, found, commits)
}

// match links sessions to commits with the matcher's default steps and
// builds the redacted result.
func match(ctx context.Context, engine *redact.Engine, found []sessions.Session, commits []gitlocal.Commit) (Result, error) {
	// An Entire session has one snapshot per checkpoint, each fuller than the
	// last, and the matcher identifies a run by its id, so only the newest
	// snapshot of each id is matched.
	latest := make(map[string]snapshot, len(found))
	var ids []string
	for _, s := range found {
		if err := ctx.Err(); err != nil {
			return Result{}, err
		}
		next := snapshot{session: s, correlated: sessioncorrelate.Correlate(s.Graph, s.Vendor)}
		current, seen := latest[s.ID]
		if !seen {
			ids = append(ids, s.ID)
		}
		if !seen || next.newerThan(current) {
			latest[s.ID] = next
		}
	}
	rows := make(map[string]Row, len(latest))
	candidates := make([]linkage.RunCandidate, 0, len(latest))
	for _, id := range ids {
		snap := latest[id]
		row, err := rowOf(engine, snap.session, snap.correlated)
		if err != nil {
			return Result{}, err
		}
		rows[id] = row
		candidates = append(candidates, candidateOf(snap.session, snap.correlated))
	}

	enriched := make([]linkage.EnrichedCommit, len(commits))
	for i, c := range commits {
		enriched[i] = enrichedOf(c)
	}
	edges, err := linkage.MatchCommits(enriched, candidates, linkage.DefaultConfig(), steps.Default())
	if err != nil {
		return Result{}, fmt.Errorf("matching commits: %w", err)
	}

	links := map[string][]Link{}
	linked := map[string]bool{}
	for _, edge := range edges {
		if edge.Status != linkage.StatusMatched || edge.RunID == "" {
			continue
		}
		links[edge.CommitSHA] = append(links[edge.CommitSHA], Link{
			Session: edge.RunID, Method: string(edge.Method), Confidence: edge.Confidence,
		})
		linked[edge.RunID] = true
	}

	result := Result{Available: true, Sessions: []Session{}, Commits: make([]Commit, 0, len(commits))}
	for _, id := range ids {
		row, ok := rows[id]
		if !ok || !linked[id] {
			continue
		}
		result.Sessions = append(result.Sessions, Session{
			ID: row.ID, Source: row.Source, Vendor: row.Vendor, Title: row.Title, Branch: row.Branch,
			StartedAt: row.StartedAt, EndedAt: row.EndedAt, Outcome: row.Outcome, EditedFiles: row.EditedFiles,
		})
	}
	for _, c := range commits {
		subject, _, err := engine.Redact(c.Subject)
		if err != nil {
			return Result{}, err
		}
		author, _, err := engine.Redact(c.AuthorName)
		if err != nil {
			return Result{}, err
		}
		commitLinks := links[c.OID]
		if commitLinks == nil {
			commitLinks = []Link{}
		}
		sortLinks(commitLinks)
		result.Commits = append(result.Commits, Commit{
			OID: c.OID, Subject: truncate(subject, maxSubject), Author: truncate(author, maxSubject),
			CommittedAt: c.CommittedAt, Links: commitLinks,
		})
	}
	return result, nil
}

func sortLinks(links []Link) {
	slices.SortStableFunc(links, func(a, b Link) int {
		if a.Confidence != b.Confidence {
			if a.Confidence > b.Confidence {
				return -1
			}
			return 1
		}
		return strings.Compare(a.Session, b.Session)
	})
}

func truncate(s string, limit int) string {
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) <= limit {
		return s
	}
	runes := []rune(s)
	return string(runes[:limit-1]) + "…"
}

// snapshot is one reading of a session, with its matcher evidence.
type snapshot struct {
	session    sessions.Session
	correlated sessioncorrelate.Result
}

// newerThan reports whether s is the later reading of the same session: the
// one written last, and when they were written together the one that knows of
// more commits.
func (s snapshot) newerThan(other snapshot) bool {
	if c := s.session.ModifiedAt.Compare(other.session.ModifiedAt); c != 0 {
		return c > 0
	}
	return len(s.correlated.CommitRefs) > len(other.correlated.CommitRefs)
}
