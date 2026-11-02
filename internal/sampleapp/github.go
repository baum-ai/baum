package sampleapp

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/baum-ai/baum/internal/discovery"
	"github.com/baum-ai/baum/internal/github"
)

// The sample project's pull requests as discovery lists them: what each
// waits on, so the Inbox shows one of every kind.
func pullRequests(p project, now time.Time) []discovery.PullRequest {
	yes, zero, two := true, 0, 2
	pr := func(pl pull, review, checks, merge string) discovery.PullRequest {
		return discovery.PullRequest{Repository: repository, Number: pl.number, HeadSHA: p.heads[pl.number], HeadRef: pl.branch, BaseRef: "main",
			Title: pl.title, URL: "https://github.com/" + repository + "/pull/" + strconv.Itoa(pl.number), Author: pl.author.login,
			State: "OPEN", AuthoredByMe: pl.author.login == me, ReviewRequestedFromMe: pl.author.login != me,
			AuthorHasWrite: &yes, ViewerCanPush: &yes, TeamMembershipKnown: true, Review: review, Checks: checks, Merge: merge,
			Unresolved: &zero, UpdatedAt: now.Add(-pl.updated)}
	}
	prs := []discovery.PullRequest{
		pr(tags, "commented", "passing", "ready"),
		pr(accents, "approved", "passing", "ready"),
		pr(readme, "unreviewed", "running", "blocked"),
		pr(sortByEdit, "changes_requested", "failing", "blocked"),
	}
	prs[3].Unresolved = &two
	return prs
}

// listedPulls are the pull requests the feature log keeps, the merged one
// among them.
func listedPulls(p project, now time.Time) []github.ListedPull {
	var pulls []github.ListedPull
	for _, pl := range append([]pull{newestFirst}, openPulls...) {
		listed := github.ListedPull{Number: pl.number, Title: pl.title, Body: pl.body, State: "open", Author: pl.author.login,
			HeadRef: pl.branch, HeadSHA: p.heads[pl.number], BaseRef: "main", BaseSHA: p.main, DefaultBranch: "main",
			CreatedAt: now.Add(-pl.opened), UpdatedAt: now.Add(-pl.updated)}
		if pl.number == newestFirst.number {
			merged := now.Add(-pl.updated)
			listed.State, listed.MergedAt, listed.ClosedAt, listed.MergeCommitSHA = "closed", &merged, &merged, p.main
		}
		pulls = append(pulls, listed)
	}
	return pulls
}

// sampleDiscovery serves the fixed pull requests, as if just fetched.
type sampleDiscovery struct{ prs []discovery.PullRequest }

func (d sampleDiscovery) Cached(_ context.Context, r github.Repository) (discovery.Snapshot, error) {
	if r.String() != repository {
		return discovery.Snapshot{Repository: r.String(), FetchedAt: time.Now()}, nil
	}
	return discovery.Snapshot{Repository: repository, PullRequests: d.prs, FetchedAt: time.Now(), Rate: discovery.RateLimit{Remaining: new(5000)}}, nil
}
func (d sampleDiscovery) Poll(ctx context.Context, r github.Repository) (discovery.Snapshot, error) {
	return d.Cached(ctx, r)
}
func (d sampleDiscovery) PollNow(ctx context.Context, r github.Repository) (discovery.Snapshot, error) {
	return d.Cached(ctx, r)
}

// gitHub answers the reads the app's pull request panels make about the
// sample project: merge options, checks, review threads and the pull
// request itself. Writes never reach it; the sample refuses them first.
type gitHub struct {
	p   project
	prs map[int]discovery.PullRequest
}

var pullNumber = regexp.MustCompile(`/pulls/(\d+)`)

func (g gitHub) RoundTrip(r *http.Request) (*http.Response, error) {
	var body any
	path := r.URL.Path
	number := 0
	if m := pullNumber.FindStringSubmatch(path); m != nil {
		number, _ = strconv.Atoi(m[1])
	}
	switch {
	case r.Method != http.MethodGet && r.Method != http.MethodPost:
		return respond(r, http.StatusForbidden, map[string]any{"message": "The sample project is read-only."})
	case r.Method == http.MethodGet && strings.HasSuffix(path, "/actions/runs"):
		body = map[string]any{"workflow_runs": []any{}}
	case r.Method == http.MethodGet && strings.HasSuffix(path, "/permission"):
		body = map[string]any{"permission": "write"}
	case r.Method == http.MethodGet && strings.HasSuffix(path, "/check-runs"):
		body = map[string]any{"check_runs": g.checkRuns(path)}
	case r.Method == http.MethodGet && strings.HasSuffix(path, "/status"):
		body = map[string]any{"statuses": []any{}}
	case r.Method == http.MethodGet && strings.Count(strings.Trim(path, "/"), "/") == 2:
		body = map[string]any{"full_name": repository, "default_branch": "main", "permissions": map[string]any{"push": true}}
	case r.Method == http.MethodGet:
		pr := g.prs[number]
		body = map[string]any{"state": "open", "title": pr.Title, "mergeable": true, "mergeable_state": "clean",
			"head": map[string]any{"sha": pr.HeadSHA, "ref": pr.HeadRef, "repo": map[string]any{"full_name": repository}},
			"base": map[string]any{"ref": "main", "repo": map[string]any{"full_name": repository}}, "user": map[string]any{"login": pr.Author}}
	default:
		body = g.graphQL(r)
	}
	return respond(r, http.StatusOK, body)
}

// checkRuns are the checks of a commit: passing, except on the branch
// whose tests fail.
func (g gitHub) checkRuns(path string) []any {
	conclusion, title := "success", "All tests passed"
	if strings.Contains(path, g.p.heads[sortByEdit.number]) {
		conclusion, title = "failure", "1 test failed: TestListNewestFirst"
	}
	return []any{
		map[string]any{"id": 1, "name": "test", "status": "completed", "conclusion": conclusion, "output": map[string]any{"title": title}},
		map[string]any{"id": 2, "name": "lint", "status": "completed", "conclusion": "success", "output": map[string]any{"title": "No problems"}},
	}
}

func (g gitHub) graphQL(r *http.Request) any {
	var query struct {
		Query     string
		Variables struct{ Number int }
	}
	_ = json.NewDecoder(r.Body).Decode(&query)
	pr := g.prs[query.Variables.Number]
	repo := func(fields map[string]any) any { return map[string]any{"data": map[string]any{"repository": fields}} }
	switch {
	case strings.Contains(query.Query, "query PullRequest("):
		return repo(map[string]any{"nameWithOwner": repository, "pullRequest": map[string]any{"number": pr.Number, "state": "OPEN", "isDraft": false,
			"title": pr.Title, "body": describe(pr.Number), "bodyHTML": "", "createdAt": time.Now().Add(-day).UTC().Format(time.RFC3339),
			"author": map[string]any{"login": pr.Author, "__typename": "User"}, "authorAssociation": "MEMBER",
			"baseRefName": "main", "baseRefOid": g.p.main, "headRefName": pr.HeadRef, "headRefOid": pr.HeadSHA, "merged": false}})
	case strings.Contains(query.Query, "query MergeOptions("):
		state := "BLOCKED"
		if pr.Merge == "ready" {
			state = "CLEAN"
		}
		return repo(map[string]any{"mergeCommitAllowed": true, "squashMergeAllowed": true, "rebaseMergeAllowed": false, "viewerDefaultMergeMethod": "SQUASH",
			"pullRequest": map[string]any{"id": "PR_sample_" + strconv.Itoa(pr.Number), "state": "OPEN", "isDraft": false, "headRefOid": pr.HeadSHA, "mergeable": "MERGEABLE", "mergeStateStatus": state}})
	case strings.Contains(query.Query, "query FixReviewThreads("):
		threads := []any{}
		if pr.Number == sortByEdit.number {
			thread := func(id, path string, line int, text string) map[string]any {
				return map[string]any{"id": id, "path": path, "line": line, "isResolved": false, "isOutdated": false, "viewerCanReply": true, "viewerCanResolve": true,
					"comments": map[string]any{"totalCount": 1, "nodes": []any{map[string]any{"state": "SUBMITTED", "authorAssociation": "MEMBER", "body": text,
						"author": map[string]any{"__typename": "User", "login": riley.login}}}}}
			}
			threads = []any{thread("RT_sample_1", "store.go", 19, "Sort by Edited here too, or the list ignores edits."),
				thread("RT_sample_2", "note.go", 11, "Say in the comment that Edited is zero until the first edit.")}
		}
		return repo(map[string]any{"pullRequest": map[string]any{"headRefOid": pr.HeadSHA, "viewerDidAuthor": pr.AuthoredByMe,
			"reviewThreads": map[string]any{"pageInfo": map[string]any{"hasNextPage": false}, "nodes": threads}}})
	case strings.Contains(query.Query, "query HeadChecks("):
		return repo(map[string]any{"object": nil})
	case strings.Contains(query.Query, "query RequiredChecks("):
		return repo(map[string]any{"ref": nil})
	}
	return repo(map[string]any{"pullRequest": map[string]any{"headRefOid": pr.HeadSHA,
		"comments": map[string]any{"nodes": []any{}, "pageInfo": map[string]any{"hasNextPage": false, "endCursor": nil}}}})
}

func respond(r *http.Request, status int, body any) (*http.Response, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(string(raw))), Request: r}, nil
}

// describe is a pull request's description.
func describe(number int) string {
	for _, pl := range openPulls {
		if pl.number == number {
			return pl.body
		}
	}
	return ""
}
