package seeddata

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/baum-ai/baum/bundle"
	"github.com/baum-ai/baum/internal/github"
	"github.com/baum-ai/baum/internal/gitlocal"
	"github.com/baum-ai/baum/internal/store"
	"github.com/baum-ai/baum/review"
)

// diffStat is a pull request's diff at its latest head, as GitHub would
// count it.
type diffStat struct{ additions, deletions, files int }

// writeReviews saves the reviews of r's reviewed pull requests into the
// clone's store, finished as generated reviews are (review.Complete), and
// returns every pull request's diff size at its head.
func writeReviews(ctx context.Context, o Options, r *repoPlan, g gitRepo, commonDir string, validator *review.DocumentValidator, counts *Counts, note func(string, ...any)) (map[int]diffStat, error) {
	repo, err := gitlocal.Open(ctx, g.dir, gitlocal.Options{})
	if err != nil {
		return nil, err
	}
	defer func() { _ = repo.Close() }()
	versions, err := store.Open(commonDir, "", true)
	if err != nil {
		return nil, err
	}
	stats := map[int]diffStat{}
	for _, pr := range r.pullRequests {
		heads := g.heads[pr.number]
		var previous store.Version
		var prior *bundle.PreviousVersion
		for v := 1; v <= pr.versions; v++ {
			if v != pr.versions && v > pr.reviewed {
				continue
			}
			comparison, err := repo.Compare(ctx, g.main, heads[v-1])
			if err != nil {
				return nil, err
			}
			hunks, err := bundle.BuildDiffIndex(comparison, repo.PatchFetcher(ctx, comparison), bundle.RoleForPath)
			if err != nil {
				return nil, err
			}
			if v == pr.versions {
				var s diffStat
				seen := map[string]bool{}
				for _, h := range hunks {
					s.additions += h.Additions
					s.deletions += h.Deletions
					if !seen[h.Path] {
						seen[h.Path] = true
						s.files++
					}
				}
				stats[pr.number] = s
				counts.LargestDiffLines = max(counts.LargestDiffLines, s.additions+s.deletions)
				if pr.big {
					counts.LargeDiff = fmt.Sprintf("%s (%d files, +%d -%d)", pr.key(), s.files, s.additions, s.deletions)
					counts.LargeDiffLines = s.additions + s.deletions
				}
			}
			if v > pr.reviewed {
				continue
			}
			rng := rand.New(rand.NewPCG(uint64(o.Seed), uint64(r.index)<<40|uint64(pr.number)<<8|uint64(v)))
			at := o.Epoch.Add(-time.Duration(pr.updated)*time.Minute - time.Duration(pr.versions-v)*7*time.Hour + 20*time.Minute)
			b := pullBundle(o, r, pr, g, comparison, hunks, heads[:v], at)
			b.Previous = prior
			authored, sections := authorReview(rng, pr, hunks, b)
			document, findings := validator.Complete(authored, &b, "", "seeddata", at)
			if findings != "" {
				return nil, fmt.Errorf("review of %s v%d: %s", pr.key(), v, findings)
			}
			version := store.Version{Metadata: store.Metadata{Version: v, Head: heads[v-1], Base: g.main, MergeBase: comparison.MergeBaseOID,
				GeneratedAt: at.UTC().Format(time.RFC3339), Backend: "claude", Model: "seeddata"}, Document: document}
			if err := versions.Put(ctx, store.Key{Repository: r.identity(), Number: pr.number}, previous, version); err != nil {
				return nil, fmt.Errorf("saving the review of %s v%d: %w", pr.key(), v, err)
			}
			previous = version
			note("review %s v%d %s", pr.key(), v, digest(document))
			prior = &bundle.PreviousVersion{Version: v, Sections: sections}
			counts.ReviewVersions++
		}
		if pr.reviewed > 0 {
			counts.ReviewedPullRequests++
		}
	}
	return stats, nil
}

// pullBundle is the evidence a review of pr at heads' last commit reads:
// the pull request, its commits, checks and, when it has one, its
// conversation on GitHub.
func pullBundle(o Options, r *repoPlan, pr *prPlan, g gitRepo, comparison bundle.Comparison, hunks []bundle.Hunk, heads []string, at time.Time) bundle.Bundle {
	head := heads[len(heads)-1]
	opened := o.Epoch.Add(-time.Duration(pr.updated)*time.Minute - time.Duration(pr.versions)*7*time.Hour - time.Hour)
	requested := []string{}
	if pr.requested {
		requested = []string{viewer}
	}
	var commits []bundle.Commit
	for i, sha := range heads {
		when := opened.Add(time.Duration(i+1) * 30 * time.Minute)
		subject := pr.title
		if i > 0 {
			subject = fmt.Sprintf("Address review comments (%d)", i)
		}
		commits = append(commits, bundle.Commit{SHA: sha, Subject: subject, CommittedAt: &when})
	}
	b := bundle.Bundle{Repository: r.identity(), Diff: hunks, Sources: bundle.Sources{"diff": true,
		github.SourcePullRequest: true, github.SourceRequestedReviewers: true, github.SourceCommits: true, github.SourceComments: true,
		github.SourceReviews: true, github.SourceReviewThreads: true, github.SourceChecks: true, github.SourceRequiredChecks: true},
		PullRequest: bundle.PullRequest{Number: pr.number, State: "open", Draft: pr.draft, Title: pr.title, Body: prBody(pr),
			AuthorLogin: pr.author, AuthorAssociation: "MEMBER", BaseRef: "main", BaseSHA: g.main,
			HeadRef: pr.branch, HeadSHA: head, MergeBaseSHA: comparison.MergeBaseOID, OpenedAt: &opened,
			RequestedReviewers: requested, Commits: commits},
		Checks: checks(head, at)}
	b.Conversation, b.Threads = []bundle.ConversationItem{}, []bundle.Thread{}
	if pr.commented && len(hunks) > 0 {
		b.Conversation, b.Threads = conversation(pr, hunks, heads, opened)
	}
	return b
}

func prBody(pr *prPlan) string {
	if pr.bot {
		return "Bumps the dependency to its latest minor release.\n\nAutomated pull request."
	}
	return fmt.Sprintf("%s.\n\n## Testing\n\n- `go test ./...` passes.\n\nWritten with Claude Code.", pr.title)
}

// checks are the CI runs on a reviewed head.
func checks(head string, at time.Time) []bundle.Check {
	yes := true
	started, done := at.Add(-40*time.Minute), at.Add(-35*time.Minute)
	check := func(id, name string) bundle.Check {
		return bundle.Check{HeadSHA: head, ID: "check_run:" + id, Name: name, Kind: "check_run", Conclusion: "success", Status: "completed",
			Required: &yes, StartedAt: &started, CompletedAt: &done}
	}
	return []bundle.Check{check("1", "test"), check("2", "lint")}
}

// conversation is a teammate's review of the pull request with a thread on
// its first file, answered by the author.
func conversation(pr *prPlan, hunks []bundle.Hunk, heads []string, opened time.Time) ([]bundle.ConversationItem, []bundle.Thread) {
	reviewer := people[(pr.number+3)%len(people)]
	if reviewer == pr.author {
		reviewer = viewer
	}
	if reviewer == pr.author {
		reviewer = people[0]
	}
	at := func(d time.Duration) *time.Time { t := opened.Add(d); return &t }
	resolved := pr.unresolved == 0
	file := hunks[0].Path
	id := fmt.Sprintf("%d", pr.number)
	state := map[string]string{"approved": "approved", "changes_requested": "changes_requested"}[pr.review]
	if state == "" {
		state = "commented"
	}
	items := []bundle.ConversationItem{
		{ID: "PRR_seed_" + id, Kind: "review", AuthorLogin: reviewer, AuthorAssociation: "MEMBER", State: state,
			Commit: heads[0], CreatedAt: at(2 * time.Hour), Body: "Thanks, a question on " + file + "."},
		{ID: "PRRC_seed_" + id, Kind: "review_comment", AuthorLogin: reviewer, AuthorAssociation: "MEMBER", Review: "PRR_seed_" + id,
			Path: file, Thread: "PRRT_seed_" + id, ThreadRoot: true, Resolved: &resolved, CreatedAt: at(2 * time.Hour),
			Body: "What happens here when the input is empty?"},
		{ID: "PRRC_seed_" + id + "_reply", Kind: "review_comment", AuthorLogin: pr.author, AuthorAssociation: "MEMBER", Path: file,
			Thread: "PRRT_seed_" + id, Resolved: &resolved, CreatedAt: at(3 * time.Hour),
			Body: "It returns early with no change; a later commit adds a test for it."},
	}
	return items, []bundle.Thread{{ID: "PRRT_seed_" + id, Path: file, Resolved: resolved, Outdated: len(heads) > 1}}
}

// authorReview writes the review as a model would: sections over the
// hunks, grouped by directory, with risks, evidence and decisions. It
// returns the sections as the next version sees them.
func authorReview(rng *rand.Rand, pr *prPlan, hunks []bundle.Hunk, b bundle.Bundle) ([]byte, []bundle.PreviousSection) {
	groups := map[string][]bundle.Hunk{}
	var order []string
	for _, h := range hunks {
		dir := path.Dir(h.Path)
		if strings.HasPrefix(dir, "docs") || strings.HasPrefix(dir, "config") {
			dir = mechanical
		}
		if _, ok := groups[dir]; !ok {
			order = append(order, dir)
		}
		groups[dir] = append(groups[dir], h)
	}
	// The mechanical section first, so the sections past the limit join a
	// logical one.
	if i := slices.Index(order, mechanical); i > 0 {
		order = append([]string{mechanical}, slices.Delete(order, i, i+1)...)
	}
	const maxSections = 6
	if len(order) > maxSections {
		for _, dir := range order[maxSections:] {
			groups[order[maxSections-1]] = append(groups[order[maxSections-1]], groups[dir]...)
		}
		order = order[:maxSections]
	}
	anchor := func(h bundle.Hunk) map[string]any { return map[string]any{"type": "hunk", "id": h.ID} }
	var sections []any
	var previous []bundle.PreviousSection
	for i, dir := range order {
		group := groups[dir]
		ids := make([]string, 0, len(group))
		for _, h := range group {
			ids = append(ids, h.ID)
		}
		id := fmt.Sprintf("sec_%s_%d", strings.Trim(strings.Map(func(r rune) rune {
			if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
				return r
			}
			return '_'
		}, strings.ToLower(dir)), "_"), i)
		kind := "logical"
		if dir == mechanical {
			kind = "mechanical"
		}
		level := 1 + rng.IntN(5)
		first := group[0]
		section := map[string]any{
			"id": id, "title": sectionTitle(dir), "kind": kind, "hunks": ids,
			"summary": map[string]any{"text": fmt.Sprintf("Changes %d %s in `%s`, which %s.", len(group), plural(len(group), "place"), dir, purpose(rng)),
				"anchors": []any{anchor(first)}},
			"attention":  nil,
			"assessment": map[string]any{"level": level, "factors": []any{}, "reasons": []any{map[string]any{"text": reason(level), "anchors": []any{anchor(first)}}}},
			"risks":      []any{},
			"evidence": []any{map[string]any{"id": fmt.Sprintf("ev_%s_ci", id[4:]), "kind": "run", "claim": "The test and lint jobs passed on the head.",
				"status": "verified", "granularity": "job", "source": "ci", "anchors": []any{map[string]any{"type": "check", "id": "check_run:1"}, map[string]any{"type": "check", "id": "check_run:2"}}}},
			"blocks": []any{},
		}
		if kind == "logical" && level >= 3 {
			tier := []string{"low", "medium", "high"}[min(level-3, 2)]
			section["risks"] = []any{map[string]any{"id": fmt.Sprintf("rsk_%s", id[4:]), "tier": tier, "category": "behavior", "source": "model",
				"text":    fmt.Sprintf("An empty %s now takes the new path, which no test exercises.", identifiers[rng.IntN(len(identifiers))]),
				"finding": nil, "anchors": []any{anchor(first)}}}
		}
		if kind == "logical" && rng.IntN(2) == 0 {
			section["blocks"] = []any{map[string]any{"kind": "decisions", "items": []any{map[string]any{
				"id": fmt.Sprintf("dec_%s", id[4:]), "by": []string{"human", "agent"}[rng.IntN(2)],
				"title":     "Keep the change inside " + path.Base(dir),
				"text":      fmt.Sprintf("The %s stays in `%s` rather than moving to a shared package.", functions[rng.IntN(len(functions))], dir),
				"rationale": "Nothing else reads it yet.", "trigger": "request", "anchors": []any{anchor(first)}}}}}
		}
		sections = append(sections, section)
		content, _ := json.Marshal(section)
		previous = append(previous, bundle.PreviousSection{ID: id, Title: sectionTitle(dir), Kind: kind, HunkContentHashes: ids, Content: content})
	}
	summary := map[string]any{
		"text": fmt.Sprintf("%s. The change touches %d %s across %d %s.", pr.title, len(hunks), plural(len(hunks), "hunk"), len(order), plural(len(order), "area")),
		"context": map[string]any{
			"area":       map[string]any{"text": "The " + pr.repo.name + " service's " + sectionTitle(order[0]) + ".", "refs": []any{map[string]any{"path": hunks[0].Path}}},
			"reached_by": map[string]any{"text": "Requests that reach " + path.Base(order[0]) + ".", "refs": []any{map[string]any{"path": hunks[0].Path}}},
		},
		"change_kind": []string{"feature", "bugfix", "refactor"}[rng.IntN(3)], "implements": []any{},
		"release_note": nil, "breaking_change": nil, "attention": nil,
		"authorship": map[string]any{"declared": "ai_assisted", "declared_tools": []any{"Claude Code"}, "anchors": []any{map[string]any{"type": "pr_body"}}},
	}
	if pr.bot {
		summary["change_kind"] = "dependency"
		summary["authorship"] = map[string]any{"declared": "unknown", "declared_tools": []any{}, "anchors": []any{}}
	}
	summary["review_exchange"] = map[string]any{"summary": nil}
	if len(b.Conversation) > 0 {
		summary["review_exchange"] = map[string]any{"summary": "A reviewer asked what happens with an empty input; the author answered and added a test."}
	}
	raw, _ := json.Marshal(map[string]any{"schema": "baum.review/1", "summary": summary, "relations": []any{}, "sections": sections})
	return raw, previous
}

// mechanical groups the documentation and configuration hunks: a review
// has at most one mechanical section.
const mechanical = "docs-and-config"

func sectionTitle(dir string) string {
	if dir == mechanical {
		return "Docs and configuration"
	}
	base := path.Base(dir)
	return strings.ToUpper(base[:1]) + base[1:]
}

func plural(n int, word string) string {
	if n == 1 {
		return word
	}
	return word + "s"
}

func purpose(rng *rand.Rand) string {
	return []string{"handles incoming requests", "keeps the stored records", "runs the background jobs", "checks who may act",
		"formats what users see", "reads the configuration"}[rng.IntN(6)]
}

func reason(level int) string {
	return []string{"", "Comments and constants only.", "A small, local change with tests.", "Changes behaviour other code relies on.",
		"Changes how errors propagate to callers.", "Touches data every request reads."}[level]
}
