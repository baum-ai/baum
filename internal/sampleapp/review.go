package sampleapp

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"time"

	"github.com/baum-ai/baum/bundle"
	"github.com/baum-ai/baum/internal/github"
	"github.com/baum-ai/baum/internal/gitlocal"
	"github.com/baum-ai/baum/internal/store"
	"github.com/baum-ai/baum/review"
)

// The sample reviews, written as a model writes them: reviews/<number>.json.
// In them "@path" stands for the hunks of a changed file ("@path#2" for its
// second one) and "@commitN" for the pull request's Nth commit; the hunks'
// IDs and the commits' SHAs are only known once the project is written.
//
//go:embed reviews/*.json
var authoredReviews embed.FS

// reviews are the pull requests with a sample review.
var reviews = []pull{tags, accents}

// writeReviews saves a review of each reviewed pull request into the
// clone's store, finished the way generated reviews are.
func writeReviews(ctx context.Context, p project, commonDir string, now time.Time) error {
	repo, err := gitlocal.Open(ctx, p.dir, gitlocal.Options{})
	if err != nil {
		return err
	}
	defer func() { _ = repo.Close() }()
	validator, err := review.NewDocumentValidator()
	if err != nil {
		return err
	}
	versions, err := store.Open(commonDir, "", true)
	if err != nil {
		return err
	}
	for _, pr := range reviews {
		head := p.heads[pr.number]
		comparison, err := repo.Compare(ctx, p.main, head)
		if err != nil {
			return err
		}
		hunks, err := bundle.BuildDiffIndex(comparison, repo.PatchFetcher(ctx, comparison), bundle.RoleForPath)
		if err != nil {
			return err
		}
		opened := now.Add(-pr.opened)
		b := bundle.Bundle{Repository: repository, Diff: hunks, Sources: bundle.Sources{"diff": true,
			github.SourcePullRequest: true, github.SourceRequestedReviewers: true, github.SourceCommits: true, github.SourceComments: true,
			github.SourceReviews: true, github.SourceReviewThreads: true, github.SourceChecks: true, github.SourceRequiredChecks: true},
			PullRequest: bundle.PullRequest{Number: pr.number, State: "open", Title: pr.title, Body: pr.body,
				AuthorLogin: pr.author.login, AuthorAssociation: "MEMBER", BaseRef: "main", BaseSHA: p.main,
				HeadRef: pr.branch, HeadSHA: head, MergeBaseSHA: comparison.MergeBaseOID, OpenedAt: &opened,
				RequestedReviewers: requested(pr), Commits: commits(p, pr, now)},
			Checks: checks(head, now)}
		b.Conversation, b.Threads = activity(p, pr, now)
		b.Sessions = wroteIt(pr, now)
		raw, err := authoredReviews.ReadFile("reviews/" + strconv.Itoa(pr.number) + ".json")
		if err != nil {
			return err
		}
		authored, err := resolve(raw, hunks, p.commits[pr.number])
		if err != nil {
			return fmt.Errorf("sample review of #%d: %w", pr.number, err)
		}
		document, findings := validator.Complete(authored, &b, "", "sample", now.Add(-time.Hour))
		if findings != "" {
			return fmt.Errorf("sample review of #%d: %s", pr.number, findings)
		}
		version := store.Version{Metadata: store.Metadata{Version: 1, Head: head, GeneratedAt: now.UTC().Format(time.RFC3339), Backend: "claude", Model: "sample"}, Document: document}
		if err := versions.Put(ctx, store.Key{Repository: repository, Number: pr.number}, store.Version{}, version); err != nil {
			return err
		}
	}
	return nil
}

var placeholder = regexp.MustCompile(`^@(commit(\d+)|([\w./-]+)(?:#(\d+))?)$`)

// resolve replaces a sample review's placeholders, a whole string of "@"
// and a path or commit with no spaces: in a list, "@path"
// becomes every hunk of the file; anywhere else, its first. A group's count
// becomes the number of its hunks.
func resolve(raw []byte, hunks []bundle.Hunk, commits []string) ([]byte, error) {
	byPath := map[string][]string{}
	for _, h := range hunks {
		byPath[h.Path] = append(byPath[h.Path], h.ID)
	}
	var problem error
	lookup := func(text string) ([]string, bool) {
		m := placeholder.FindStringSubmatch(text)
		if m == nil {
			return nil, false
		}
		if m[2] != "" {
			n, _ := strconv.Atoi(m[2])
			if n < 1 || n > len(commits) {
				problem = fmt.Errorf("%s: no such commit", text)
				return nil, true
			}
			return []string{commits[n-1]}, true
		}
		ids := byPath[m[3]]
		if m[4] != "" {
			n, _ := strconv.Atoi(m[4])
			if n < 1 || n > len(ids) {
				problem = fmt.Errorf("%s: no such hunk", text)
				return nil, true
			}
			ids = ids[n-1 : n]
		}
		if len(ids) == 0 {
			problem = fmt.Errorf("%s: the file has no hunk", text)
		}
		return ids, true
	}
	var walk func(any) any
	walk = func(value any) any {
		switch v := value.(type) {
		case string:
			if ids, ok := lookup(v); ok && len(ids) > 0 {
				return ids[0]
			}
		case []any:
			var out []any
			for _, item := range v {
				if text, ok := item.(string); ok {
					if ids, ok := lookup(text); ok {
						for _, id := range ids {
							out = append(out, id)
						}
						continue
					}
				}
				out = append(out, walk(item))
			}
			if out == nil {
				out = []any{}
			}
			return out
		case map[string]any:
			for key, item := range v {
				v[key] = walk(item)
			}
			if listed, ok := v["hunks"].([]any); ok && v["count"] != nil {
				v["count"] = len(listed)
			}
		}
		return value
	}
	var document any
	if err := json.Unmarshal(raw, &document); err != nil {
		return nil, err
	}
	document = walk(document)
	if problem != nil {
		return nil, problem
	}
	return json.Marshal(document)
}

// requested is who a pull request asks for a review: the person, on their
// teammates' pull requests.
func requested(pr pull) []string {
	if pr.author.login == me {
		return []string{}
	}
	return []string{me}
}

// commits are a pull request's commits as GitHub lists them.
func commits(p project, pr pull, now time.Time) []bundle.Commit {
	var listed []bundle.Commit
	for i, c := range pr.commits {
		at := now.Add(-c.ago)
		sha := p.commits[pr.number][i]
		added, removed := p.lines[sha][0], p.lines[sha][1]
		listed = append(listed, bundle.Commit{SHA: sha, Subject: c.message, CommittedAt: &at, Additions: &added, Deletions: &removed})
	}
	return listed
}

// checks are the CI runs on a reviewed head, which pass: the same test and
// lint runs the sample's GitHub answers with.
func checks(head string, now time.Time) []bundle.Check {
	yes := true
	started, done := now.Add(-4*time.Hour), now.Add(-4*time.Hour+3*time.Minute)
	check := func(id, name string) bundle.Check {
		return bundle.Check{HeadSHA: head, ID: "check_run:" + id, Name: name, Kind: "check_run", Conclusion: "success", Status: "completed",
			Required: &yes, StartedAt: &started, CompletedAt: &done}
	}
	return []bundle.Check{check("1", "test"), check("2", "lint")}
}

// activity is what was said on a reviewed pull request: on tags, an
// automated review whose finding a later commit fixed and a teammate's
// question, both answered by the person and resolved; on accents, an
// approval.
func activity(p project, pr pull, now time.Time) ([]bundle.ConversationItem, []bundle.Thread) {
	at := func(ago time.Duration) *time.Time { t := now.Add(-ago); return &t }
	yes := true
	switch pr.number {
	case tags.number:
		shas := p.commits[pr.number]
		indexLine := 12
		conversation := []bundle.ConversationItem{
			{ID: "PRR_sample_codex", Kind: "review", AuthorLogin: "chatgpt-codex-connector", Bot: true, BotTool: "codex", State: "commented",
				Commit: shas[1], CreatedAt: at(47 * time.Hour), Body: "Here are some automated review suggestions for this pull request."},
			{ID: "PRRC_sample_query", Kind: "review_comment", AuthorLogin: "chatgpt-codex-connector", Bot: true, BotTool: "codex",
				Review: "PRR_sample_codex", Path: "tags.go", Thread: "PRRT_sample_query", ThreadRoot: true, Resolved: &yes, CreatedAt: at(47 * time.Hour),
				Body: "**<sub><sub>![P2 Badge](https://img.shields.io/badge/P2-yellow?style=flat)</sub></sub>  Clean the tag ByTag is asked for like saved tags**\n\nByTag lowercases the tag but does not trim it, so `ByTag(\" work\")` finds nothing although Add saved the note under `work`."},
			{ID: "PRRC_sample_query_reply", Kind: "review_comment", AuthorLogin: sam.login, AuthorAssociation: "MEMBER", Path: "tags.go",
				Thread: "PRRT_sample_query", Resolved: &yes, CreatedAt: at(5*time.Hour + 25*time.Minute),
				Body: "Fixed in " + shas[2][:7] + ": ByTag now looks the tag up through cleanTag, which CleanTags uses too."},
			{ID: "PRR_sample_riley", Kind: "review", AuthorLogin: riley.login, AuthorAssociation: "MEMBER", State: "commented",
				Commit: shas[3], CreatedAt: at(4*time.Hour + 30*time.Minute), Body: "Reads well. One question about the index."},
			{ID: "PRRC_sample_index", Kind: "review_comment", AuthorLogin: riley.login, AuthorAssociation: "MEMBER", Review: "PRR_sample_riley",
				Path: "store.go", Thread: "PRRT_sample_index", ThreadRoot: true, Resolved: &yes, CreatedAt: at(4*time.Hour + 30*time.Minute),
				Body: "The index holds positions in notes. What keeps it right once notes can be removed?"},
			{ID: "PRRC_sample_index_reply", Kind: "review_comment", AuthorLogin: sam.login, AuthorAssociation: "MEMBER", Path: "store.go",
				Thread: "PRRT_sample_index", Resolved: &yes, CreatedAt: at(4 * time.Hour),
				Body: "Add is the only writer today, and its comment says that whatever removes or reorders notes must update the index too."},
		}
		threads := []bundle.Thread{
			// The fix rewrote the lines it was left on.
			{ID: "PRRT_sample_query", Path: "tags.go", Resolved: true, Outdated: true},
			{ID: "PRRT_sample_index", Path: "store.go", Line: &indexLine, Resolved: true},
		}
		return conversation, threads
	case accents.number:
		return []bundle.ConversationItem{
			{ID: "PRR_sample_accents", Kind: "review", AuthorLogin: riley.login, AuthorAssociation: "MEMBER", State: "approved",
				Commit: p.heads[pr.number], CreatedAt: at(2 * time.Hour),
				Body: "Works for everything I tried. Letters like ø can come later, in their own issue."},
		}, []bundle.Thread{}
	}
	return []bundle.ConversationItem{}, []bundle.Thread{}
}

// wroteIt are the person's agent sessions that wrote a pull request, as its
// review sees them.
func wroteIt(pr pull, now time.Time) []bundle.Session {
	var wrote []bundle.Session
	for _, a := range agentSessions {
		if a.pr != pr.number {
			continue
		}
		times := a.turnTimes(now)
		start, end := times[1], times[len(a.prompts)].Add(3*time.Minute)
		s := bundle.Session{RootSessionID: a.id, Vendor: "claude_code", Models: []string{a.model}, Title: a.title, Branch: pr.branch,
			StartedAt: &start, EndedAt: &end, Outcome: "committed"}
		for i, text := range a.prompts {
			sent := times[i+1]
			s.Prompts = append(s.Prompts, bundle.Prompt{At: &sent, Index: i + 1, ID: fmt.Sprintf("p%d", i+1), Text: text})
		}
		wrote = append(wrote, s)
	}
	return wrote
}
