package uiserver_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/baum-ai/baum/agent"
	"github.com/baum-ai/baum/decisions"
	"github.com/baum-ai/baum/internal/appdb/appdbtest"
	"github.com/baum-ai/baum/internal/assemble/assembletest"
	"github.com/baum-ai/baum/internal/automatic"
	"github.com/baum-ai/baum/internal/config"
	"github.com/baum-ai/baum/internal/daemon"
	"github.com/baum-ai/baum/internal/decisionlog"
	"github.com/baum-ai/baum/internal/discovery"
	"github.com/baum-ai/baum/internal/doctor"
	"github.com/baum-ai/baum/internal/generate"
	"github.com/baum-ai/baum/internal/generate/generatetest"
	"github.com/baum-ai/baum/internal/github"
	"github.com/baum-ai/baum/internal/githubauth"
	"github.com/baum-ai/baum/internal/hookevents"
	"github.com/baum-ai/baum/internal/inboxes"
	"github.com/baum-ai/baum/internal/jobs"
	"github.com/baum-ai/baum/internal/livesessions"
	"github.com/baum-ai/baum/internal/localapp"
	"github.com/baum-ai/baum/internal/prupdate"
	"github.com/baum-ai/baum/internal/repos"
	"github.com/baum-ai/baum/internal/reviewgroups"
	"github.com/baum-ai/baum/internal/reviewindex"
	"github.com/baum-ai/baum/internal/store"
	"github.com/baum-ai/baum/internal/transcript"
	"github.com/baum-ai/baum/internal/uiserver"
	"github.com/baum-ai/baum/internal/uistate"
)

// The video fixture's people. All of them are fictional.
const videoLogin = "alex-m"

var videoRepositories = []string{"fernhill/api", "fernhill/mobile", "fernhill/web"}

// videoPull is one open pull request of the video fixture, as discovery and
// the fake GitHub report it.
type videoPull struct {
	discovery.PullRequest
	// Risk and minutes vary the saved reviews in the lists.
	risk, minutes int
	// age is how long ago it was opened.
	age time.Duration
}

// videoPulls are the fixture's open pull requests. head is each repository's
// pull request head; fix is fernhill/api#379's own head, the one the video
// fixes.
func videoPulls(head map[string]string, fix string) []videoPull {
	now := time.Now().UTC()
	write := true
	pr := func(repository string, number int, title, author, ref, review, checks, merge string, unresolved, adds, dels, files, risk, minutes int, age, updated time.Duration) videoPull {
		p := discovery.PullRequest{Repository: repository, Number: number, HeadSHA: head[repository], HeadRef: ref, HeadRepository: repository, BaseRef: "main",
			Additions: adds, Deletions: dels, ChangedFiles: files, Title: title, URL: fmt.Sprintf("https://github.com/%s/pull/%d", repository, number),
			Author: author, State: "OPEN", AuthoredByMe: author == videoLogin, AuthorHasWrite: &write, ViewerCanPush: &write, TeamMembershipKnown: true,
			Review: review, Checks: checks, Merge: merge, Unresolved: new(unresolved), UpdatedAt: now.Add(-updated)}
		return videoPull{PullRequest: p, risk: risk, minutes: minutes, age: age}
	}
	h := time.Hour
	pulls := []videoPull{
		pr("fernhill/web", 214, "Add saved filters to the orders table", "maya-r", "maya-r/saved-filters", "unreviewed", "passing", "blocked", 0, 312, 48, 9, 3, 14, 26*h, 2*h),
		pr("fernhill/api", 387, "Rate-limit the public search endpoint", "jonas-k", "jonas-k/search-rate-limit", "unreviewed", "passing", "blocked", 0, 184, 22, 6, 4, 11, 20*h, 3*h),
		pr("fernhill/mobile", 92, "Offline drafts for the notes screen", "priya-s", "priya-s/offline-drafts", "unreviewed", "running", "blocked", 0, 427, 91, 12, 3, 18, 30*h, 40*time.Minute),
		pr("fernhill/api", 381, "Move invoice PDFs to a background job", videoLogin, "alex-m/invoice-jobs", "approved", "running", "blocked", 0, 236, 87, 8, 2, 9, 3*24*h, 5*h),
		pr("fernhill/web", 209, "Lazy-load the dashboard charts", videoLogin, "alex-m/lazy-charts", "approved", "passing", "ready", 0, 94, 31, 4, 2, 6, 2*24*h, 7*h),
		pr("fernhill/api", 379, "Retry webhook deliveries with backoff", videoLogin, "alex-m/webhook-retries", "changes_requested", "failing", "blocked", 0, 268, 41, 7, 4, 12, 4*24*h, 90*time.Minute),
		pr("fernhill/web", 205, "Upgrade the date picker", videoLogin, "alex-m/date-picker", "approved", "passing", "blocked", 0, 158, 133, 11, 2, 8, 4*24*h, 26*h),
		pr("fernhill/mobile", 95, "Dark mode for the settings screen", "priya-s", "priya-s/settings-dark-mode", "commented", "passing", "blocked", 1, 203, 57, 10, 2, 7, 22*h, 4*h),
		pr("fernhill/web", 216, "Fix the focus ring in the sidebar", "jonas-k", "jonas-k/sidebar-focus", "approved", "passing", "ready", 0, 18, 6, 2, 2, 3, 9*h, 6*h),
		pr("fernhill/api", 390, "Draft: audit log export", "leo-t", "leo-t/audit-export", "unreviewed", "none", "draft", 0, 341, 12, 9, 3, 15, 8*h, 8*h),
	}
	for i := range pulls {
		p := &pulls[i].PullRequest
		switch p.Number {
		case 214, 387, 92:
			p.ReviewRequestedFromMe = true
		case 379:
			p.HeadSHA = fix
		case 205:
			p.Behind = true
		case 390:
			p.Draft = true
		}
	}
	return pulls
}

// videoDiscovery serves each fernhill repository's open pull requests.
type videoDiscovery map[string][]discovery.PullRequest

func (d videoDiscovery) Cached(_ context.Context, repository github.Repository) (discovery.Snapshot, error) {
	return discovery.Snapshot{Repository: repository.String(), PullRequests: d[repository.String()], FetchedAt: time.Now(), Rate: discovery.RateLimit{Remaining: new(5000)}}, nil
}
func (d videoDiscovery) Poll(ctx context.Context, repository github.Repository) (discovery.Snapshot, error) {
	return d.Cached(ctx, repository)
}
func (d videoDiscovery) PollNow(ctx context.Context, repository github.Repository) (discovery.Snapshot, error) {
	return d.Cached(ctx, repository)
}

// videoRepo is one fernhill clone: an assembletest repository whose origin
// names GitHub but, by a repository-local rewrite, fetches from and pushes
// to a local bare repository, so a fix runs to a real push.
type videoRepo struct {
	assembletest.Repo
	name, bare string
}

func newVideoRepo(t *testing.T, name, remotes string) videoRepo {
	t.Helper()
	r := videoRepo{Repo: assembletest.NewRepo(t, assembletest.Options{}), name: name}
	assembletest.Git(t, r.Dir, "remote", "add", "origin", "https://github.com/"+name)
	// gitlocal runs git without the global configuration: the fix's commits
	// take their author from the clone's own.
	assembletest.Git(t, r.Dir, "config", "user.name", "Alex Morgan")
	assembletest.Git(t, r.Dir, "config", "user.email", "alex@example.com")
	r.bare = filepath.Join(remotes, name+".git")
	if err := os.MkdirAll(filepath.Dir(r.bare), 0o700); err != nil {
		t.Fatal(err)
	}
	assembletest.Git(t, filepath.Dir(r.bare), "init", "-q", "--bare", "-b", "main", r.bare)
	return r
}

// publish pushes main and each pull request's head branch to the bare
// repository.
func (r videoRepo) publish(t *testing.T, pulls []videoPull) {
	t.Helper()
	refspecs := []string{"main:refs/heads/main"}
	for _, p := range pulls {
		if p.Repository == r.name {
			refspecs = append(refspecs, p.HeadSHA+":refs/heads/"+p.HeadRef)
		}
	}
	assembletest.Git(t, r.Dir, append([]string{"push", "-q", r.bare}, refspecs...)...)
	assembletest.Git(t, r.Dir, "fetch", "-q", r.bare, "+refs/heads/*:refs/remotes/origin/*")
}

// redirect points origin at the bare repository with a repository-local
// rewrite. A fix accepts origin because its configured URL names the
// repository; the registry reads remotes as git rewrites them, so an ssh
// remote the rewrite leaves alone keeps the clone filed under the repository.
func (r videoRepo) redirect(t *testing.T) {
	t.Helper()
	assembletest.Git(t, r.Dir, "remote", "add", "upstream", "git@github.com:"+r.name+".git")
	assembletest.Git(t, r.Dir, "config", "url."+r.bare+".insteadOf", "https://github.com/"+r.name)
}

// webhookRetrySource is fernhill/api's webhook retry policy on #379's branch,
// before and after the fix.
func webhookRetrySource(fixed bool) string {
	body := "\treturn base << attempt\n"
	if fixed {
		body = "\tdelay := base << attempt\n\tif delay > maxDelay {\n\t\treturn maxDelay\n\t}\n\treturn delay\n"
	}
	return "package webhooks\n\nimport \"time\"\n\n// MaxAttempts is how many times a delivery is tried before it is kept for replay.\nconst MaxAttempts = 6\n\nconst (\n\tbase     = 2 * time.Second\n\tmaxDelay = 5 * time.Minute\n)\n\n// Backoff is the delay before the attempt'th retry.\nfunc Backoff(attempt int) time.Duration {\n" + body + "}\n"
}

// videoFixer is the video fixture's agent: it caps the webhook retry delay
// (or, on another branch, leaves a note), taking a few seconds, and answers
// every review thread it was given as addressed.
type videoFixer struct{ *generatetest.Backend }

var (
	videoThreadHeading = regexp.MustCompile(`(?m)^### Review thread (T[0-9]+)$`)
	videoAnswerFence   = regexp.MustCompile("(?m)^\\s*(```baum-threads [0-9a-f]+)$")
)

func (videoFixer) Fix(ctx context.Context, r agent.Request) (agent.Result, error) {
	steps := []agent.TranscriptEntry{
		{Kind: "message", Text: "Reading the failing test."},
		{Kind: "tool", ID: "t1", Tool: "Read", Title: "internal/webhooks/retry.go", Status: "completed"},
		{Kind: "message", Text: "The backoff grows without a bound, so the sixth retry waits over two minutes and the test's deadline passes. Capping the delay at five minutes."},
		{Kind: "tool", ID: "t2", Tool: "Edit", Title: "internal/webhooks/retry.go", Status: "completed"},
		{Kind: "tool", ID: "t3", Tool: "command", Title: "go test ./internal/webhooks/...", Status: "completed", Text: "ok  \tfernhill/api/internal/webhooks\t0.41s"},
	}
	for _, step := range steps {
		select {
		case <-ctx.Done():
			return agent.Result{}, ctx.Err()
		case <-time.After(900 * time.Millisecond):
		}
		r.Emit(step)
	}
	path := filepath.Join(r.Directory, "internal", "webhooks", "retry.go")
	content := webhookRetrySource(true)
	if _, err := os.Stat(path); err != nil {
		path, content = filepath.Join(r.Directory, "FIXED.md"), "fixed by the fixture\n"
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return agent.Result{}, err
	}
	summary := "Capped the webhook retry delay at five minutes so the sixth attempt stays inside the delivery deadline."
	if fence := videoAnswerFence.FindStringSubmatch(r.Prompt); fence != nil {
		var answers []string
		for _, ref := range videoThreadHeading.FindAllStringSubmatch(r.Prompt, -1) {
			answers = append(answers, fmt.Sprintf(`{"id":%q,"addressed":true,"summary":"Capped the delay at five minutes."}`, ref[1]))
		}
		summary += "\n\n" + fence[1] + "\n[" + strings.Join(answers, ",") + "]\n```"
	}
	return agent.Result{Document: []byte(summary), Calls: 4, Tokens: 18400}, nil
}

// videoThread is a review thread of fernhill/api#379.
type videoThread struct {
	id, path, login, body string
	line                  int
}

var videoThreads = []videoThread{
	{"RT_fernhill_api_379_1", "internal/webhooks/retry.go", "maya-r", "This doubles forever. Can we cap the delay so a long outage does not push retries out for hours?", 15},
	{"RT_fernhill_api_379_2", "internal/webhooks/retry.go", "jonas-k", "The test for the sixth attempt times out on CI; the delay there is over two minutes.", 6},
}

// videoGitHub is the fake GitHub of the video fixture: it answers for the
// repository and pull request each request names, from pulls.
func videoGitHub(pulls []videoPull) browserTransport {
	byKey := map[string]videoPull{}
	for _, p := range pulls {
		byKey[fmt.Sprintf("%s#%d", p.Repository, p.Number)] = p
	}
	var mu sync.Mutex
	resolved := map[string]bool{}
	pullPath := regexp.MustCompile(`^/repos/([^/]+)/([^/]+)/pulls/([0-9]+)$`)
	commitPath := regexp.MustCompile(`^/repos/([^/]+)/([^/]+)/commits/([0-9a-f]+)/`)
	return browserTransport(func(r *http.Request) (*http.Response, error) {
		authorization := strings.TrimPrefix(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "), "token ")
		if !strings.HasPrefix(authorization, "fixture-access-") {
			return &http.Response{StatusCode: http.StatusUnauthorized, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("unauthorized")), Request: r}, nil
		}
		path := strings.TrimPrefix(r.URL.Path, "/api/v3")
		var body any
		status := http.StatusOK
		fix := ""
		for _, p := range pulls {
			if p.Number == 379 {
				fix = p.HeadSHA
			}
		}
		switch {
		case r.Method == "GET" && pullPath.MatchString(path):
			m := pullPath.FindStringSubmatch(path)
			repository := m[1] + "/" + m[2]
			p, ok := byKey[repository+"#"+m[3]]
			if !ok {
				status, body = http.StatusNotFound, map[string]any{"message": "Not Found"}
				break
			}
			mergeableState := map[string]string{"ready": "clean", "conflict": "dirty", "draft": "draft"}[p.Merge]
			if p.Behind {
				mergeableState = "behind"
			}
			if mergeableState == "" {
				mergeableState = "blocked"
			}
			body = map[string]any{"state": "open", "title": p.Title, "draft": p.Draft, "html_url": p.URL, "mergeable": p.Merge != "conflict", "mergeable_state": mergeableState,
				"head": map[string]any{"sha": p.HeadSHA, "ref": p.HeadRef, "repo": map[string]any{"full_name": repository}},
				"base": map[string]any{"ref": "main", "repo": map[string]any{"full_name": repository}}, "user": map[string]any{"login": p.Author}}
		case r.Method == "GET" && strings.HasSuffix(path, "/permission"):
			body = map[string]any{"permission": "write"}
		case r.Method == "GET" && commitPath.MatchString(path) && strings.HasSuffix(path, "/check-runs"):
			if commitPath.FindStringSubmatch(path)[3] == fix {
				body = map[string]any{"total_count": 1, "check_runs": []any{map[string]any{"id": 1, "name": "test", "status": "completed", "conclusion": "failure",
					"output": map[string]any{"title": "1 test failed", "summary": "TestBackoffSixthAttempt: delivery deadline exceeded after 2m8s"}}}}
			} else {
				body = map[string]any{"total_count": 1, "check_runs": []any{map[string]any{"id": 2, "name": "test", "status": "completed", "conclusion": "success"}}}
			}
		case r.Method == "GET" && commitPath.MatchString(path) && strings.HasSuffix(path, "/status"):
			body = map[string]any{"statuses": []any{}}
		case r.Method == "GET" && strings.HasSuffix(path, "/actions/runs"):
			body = map[string]any{"workflow_runs": []any{}}
		case r.Method == "GET" && strings.Count(strings.Trim(path, "/"), "/") == 2 && strings.HasPrefix(path, "/repos/"):
			parts := strings.Split(strings.Trim(path, "/"), "/")
			body = map[string]any{"full_name": parts[1] + "/" + parts[2], "default_branch": "main", "permissions": map[string]any{"push": true}}
		case r.Method == "GET":
			status, body = http.StatusNotFound, map[string]any{"message": "Not Found"}
		default:
			var query struct {
				Query     string
				Variables struct {
					Owner, Name string
					Number      int
					Thread      string
				}
			}
			_ = json.NewDecoder(r.Body).Decode(&query)
			repository := query.Variables.Owner + "/" + query.Variables.Name
			p := byKey[fmt.Sprintf("%s#%d", repository, query.Variables.Number)]
			author := map[string]any{"login": p.Author, "__typename": "User"}
			switch {
			case strings.Contains(query.Query, "query PullRequest("):
				body = map[string]any{"data": map[string]any{"repository": map[string]any{"nameWithOwner": repository, "pullRequest": map[string]any{"number": query.Variables.Number, "state": "OPEN", "isDraft": p.Draft,
					"title": p.Title, "body": "", "bodyHTML": "", "createdAt": time.Now().Add(-p.age).UTC().Format(time.RFC3339), "author": author, "authorAssociation": "MEMBER",
					"baseRefName": "main", "headRefName": p.HeadRef, "headRefOid": p.HeadSHA, "merged": false}}}}
			case strings.Contains(query.Query, "query MergeOptions("):
				state := map[bool]string{true: "CLEAN", false: "BLOCKED"}[p.Merge == "ready"]
				body = map[string]any{"data": map[string]any{"repository": map[string]any{"mergeCommitAllowed": true, "squashMergeAllowed": true, "rebaseMergeAllowed": false, "viewerDefaultMergeMethod": "SQUASH",
					"pullRequest": map[string]any{"id": fmt.Sprintf("PR_%s_%d", strings.ReplaceAll(repository, "/", "_"), p.Number), "state": "OPEN", "isDraft": p.Draft, "headRefOid": p.HeadSHA, "mergeable": "MERGEABLE", "mergeStateStatus": state}}}}
			case strings.Contains(query.Query, "mutation MergePullRequest("):
				body = map[string]any{"data": map[string]any{"mergePullRequest": map[string]any{"pullRequest": map[string]any{"merged": true, "state": "MERGED"}}}}
			case strings.Contains(query.Query, "query FixReviewThreads("), strings.Contains(query.Query, "query PullRequestReviewThreads("):
				threads := []any{}
				if repository == "fernhill/api" && query.Variables.Number == 379 {
					mu.Lock()
					for _, th := range videoThreads {
						threads = append(threads, map[string]any{"id": th.id, "path": th.path, "line": th.line, "originalLine": th.line, "isResolved": resolved[th.id], "isOutdated": false, "viewerCanReply": true, "viewerCanResolve": true,
							"comments": map[string]any{"totalCount": 1, "nodes": []any{map[string]any{"state": "SUBMITTED", "authorAssociation": "MEMBER", "body": th.body,
								"url": p.URL + "#discussion_" + th.id, "author": map[string]any{"__typename": "User", "login": th.login}}}}})
					}
					mu.Unlock()
				}
				body = map[string]any{"data": map[string]any{"repository": map[string]any{"pullRequest": map[string]any{"headRefOid": p.HeadSHA, "viewerDidAuthor": p.AuthoredByMe,
					"reviewThreads": map[string]any{"pageInfo": map[string]any{"hasNextPage": false, "endCursor": nil}, "nodes": threads}}}}}
			case strings.Contains(query.Query, "query ReviewThreadState("):
				mu.Lock()
				body = map[string]any{"data": map[string]any{"node": map[string]any{"id": query.Variables.Thread, "isResolved": resolved[query.Variables.Thread], "viewerCanReply": true, "viewerCanResolve": true,
					"pullRequest": map[string]any{"number": 379, "repository": map[string]any{"nameWithOwner": "fernhill/api"}}}}}
				mu.Unlock()
			case strings.Contains(query.Query, "mutation ReplyToReviewThread("):
				body = map[string]any{"data": map[string]any{"addPullRequestReviewThreadReply": map[string]any{"comment": map[string]any{"id": "C_" + query.Variables.Thread}}}}
			case strings.Contains(query.Query, "mutation ResolveReviewThread("):
				mu.Lock()
				resolved[query.Variables.Thread] = true
				mu.Unlock()
				body = map[string]any{"data": map[string]any{"resolveReviewThread": map[string]any{"thread": map[string]any{"id": query.Variables.Thread, "isResolved": true}}}}
			case strings.Contains(query.Query, "CurrentName"):
				body = map[string]any{"data": map[string]any{"repository": map[string]any{"nameWithOwner": repository}}}
			case strings.Contains(query.Query, "query AuthoredPullRequests("):
				body = map[string]any{"data": map[string]any{"search": map[string]any{"nodes": []any{}, "pageInfo": map[string]any{"hasNextPage": false, "endCursor": nil}}}}
			case strings.Contains(query.Query, "query HeadChecks("):
				body = map[string]any{"data": map[string]any{"repository": map[string]any{"object": nil}}}
			case strings.Contains(query.Query, "query RequiredChecks("):
				body = map[string]any{"data": map[string]any{"repository": map[string]any{"ref": nil}}}
			default:
				field := "comments"
				for _, pair := range [][2]string{{"PullRequestReviewRequests", "reviewRequests"}, {"PullRequestCommits", "commits"}, {"PullRequestReviews", "reviews"}} {
					if strings.Contains(query.Query, "query "+pair[0]+"(") {
						field = pair[1]
					}
				}
				body = map[string]any{"data": map[string]any{"repository": map[string]any{"pullRequest": map[string]any{"headRefOid": p.HeadSHA, field: map[string]any{"nodes": []any{}, "pageInfo": map[string]any{"hasNextPage": false, "endCursor": nil}}}}}}
			}
		}
		b, _ := json.Marshal(body)
		return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(string(b))), Request: r}, nil
	})
}

// videoPlanUsage answers api/plan-usage: Claude's 5-hour window 41% used,
// resetting in three hours, its week 63%, and Codex's week 28%.
var videoPlanUsage = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
	now := time.Now().UTC()
	at := func(d time.Duration) string { return now.Add(d).Format(time.RFC3339) }
	w.Header().Set("Content-Type", "application/json")
	_, _ = fmt.Fprintf(w, `{"plans":[
{"provider":"claude","available":true,"windows":[{"name":"5-hour","used_percent":41,"resets_at":%q},{"name":"weekly","used_percent":63,"resets_at":%q}],"fetched_at":%q},
{"provider":"codex","available":true,"plan":"pro","windows":[{"name":"weekly","used_percent":28,"resets_at":%q}],"fetched_at":%q}]}`,
		at(3*time.Hour), at(76*time.Hour), at(-time.Minute), at(110*time.Hour), at(-time.Minute))
})

// saveVideoReviews saves a current review of each open pull request, from
// the schema's review example, in its clone's store.
// videoSectionTitles replace the example review's dead-letter section titles
// with ones that read for any pull request.
var videoSectionTitles = map[string]string{
	"sec_fixed_delay": "Configuration", "sec_api": "API changes", "sec_deadletter": "Storage and migration",
	"sec_docs": "Docs and dependencies", "sec_mechanical": "Mechanical changes", "sec_ackfix": "Edge cases",
}

// videoReviewText is what the Inbox shows of each pull request's review.
var videoReviewText = map[int]struct{ summary, section, risk string }{
	214: {"Adds a Save view button to the orders table, so people can keep a set of filters and columns and open it again later. Saved views are stored per user.", "Saved views per user", "Saved views are read on every page load of the orders table, and the query has no index on the user id yet."},
	387: {"Limits the public search endpoint to 30 requests a minute per API key, and answers 429 with a Retry-After header once a client goes over.", "Per-key rate limit", "Requests without an API key share one bucket, so a single busy anonymous client can lock out every other anonymous caller."},
	92:  {"Keeps drafts of notes on the device while offline and uploads them when the connection comes back, newest first.", "Draft sync on reconnect", "Two devices editing the same draft offline keep the last upload, and the other edit is lost without a warning."},
	381: {"Renders invoice PDFs in a background job instead of during checkout, and emails the link once the PDF is ready.", "Background rendering", "Checkout no longer waits for the PDF, so an invoice that fails to render is only noticed when the email never arrives."},
	209: {"Loads the dashboard's charts only when they scroll into view, which halves the first load of the page.", "Lazy chart loading", "Charts below the fold now load on scroll, so a printed dashboard can come out with empty charts."},
	379: {"Retries failed webhook deliveries with exponential backoff, up to six attempts, before keeping them for replay.", "Retry policy and backoff", "The delay doubles without a cap, so after a long outage the sixth attempt lands past the delivery deadline and the event is dropped."},
	205: {"Upgrades the date picker to version 5 and moves the four forms that use it to the new range API.", "Date picker upgrade", "Version 5 reads dates in the browser's time zone, so a date picked late in the evening can be saved as the next day."},
	95:  {"Adds a dark theme to the settings screen that follows the system setting.", "Settings theme", "A few icons are drawn in a fixed dark grey and nearly disappear on the dark background."},
	216: {"Draws the sidebar's focus ring inside the item, so it is no longer cut off at the edge.", "Focus ring", "Only the ring's position changes; nothing else in the sidebar moves."},
	390: {"Starts an export of the audit log as CSV, for admins only.", "Audit log export", "The export loads the whole log into memory, which large accounts may not survive."},
}

func saveVideoReviews(t *testing.T, raw []byte, pulls []videoPull, clones map[string]repos.Clone, repo map[string]videoRepo) {
	t.Helper()
	for _, p := range pulls {
		var doc map[string]any
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatal(err)
		}
		r := repo[p.Repository]
		review := doc["review"].(map[string]any)
		review["repository"], review["pull_request"], review["version"], review["previous_version"] = p.Repository, p.Number, 1, nil
		review["pr_state"], review["head"], review["base"], review["merge_base"] = "open", p.HeadSHA, r.Base, r.MergeBase
		review["generated_at"] = time.Now().Add(-p.age / 2).UTC().Format(time.RFC3339)
		delete(review, "merged_commit")
		summary := doc["summary"].(map[string]any)
		summary["title"], summary["review_minutes"] = p.Title, p.minutes
		summary["risk_score"].(map[string]any)["score"] = p.risk
		// The example review is about dead letters; each pull request gets
		// what the Inbox shows of its own review instead: the summary, and
		// its top risk under a section of that name.
		if text, ok := videoReviewText[p.Number]; ok {
			summary["text"] = text.summary
			summary["risk_score"].(map[string]any)["reasons"] = []any{map[string]any{"section": "sec_retry", "risk": nil, "text": text.risk}}
			for _, section := range doc["sections"].([]any) {
				s := section.(map[string]any)
				if title, ok := videoSectionTitles[s["id"].(string)]; ok {
					s["title"] = title
				}
				if s["id"] == "sec_retry" {
					s["title"] = text.section
				}
			}
		}
		// Images need stored attachments; the video's reviews have none.
		doc["index"].(map[string]any)["attachments"] = []any{}
		for _, section := range doc["sections"].([]any) {
			s := section.(map[string]any)
			s["blocks"] = deleteImages(s["blocks"].([]any))
		}
		// As in the browser fixture, the renamed guide's hunk is the
		// repository's real one, so its excerpt comes from git.
		for _, hunk := range doc["index"].(map[string]any)["hunks"].([]any) {
			if h := hunk.(map[string]any); h["path"] == "docs/dead-letters.md" {
				h["path"], h["old_path"], h["change"], h["new_range"] = "docs/guide.md", "docs/old-guide.md", "renamed", []int{3, 7}
				h["additions"], h["deletions"] = 1, 1
				delete(h, "diff")
				delete(h, "github_url")
			}
		}
		document, _ := json.Marshal(doc)
		versions, err := store.Open(clones[p.Repository].CommonDir, "", true)
		if err != nil {
			t.Fatal(err)
		}
		next := store.Version{Metadata: store.Metadata{Version: 1, Head: p.HeadSHA, GeneratedAt: time.Now().UTC().Format(time.RFC3339), Backend: "claude", Model: "claude-sonnet-4-5"}, Document: document}
		if err := versions.Put(context.Background(), store.Key{Repository: p.Repository, Number: p.Number}, store.Version{}, next); err != nil {
			t.Fatal(err)
		}
	}
}

func deleteImages(blocks []any) []any {
	kept := blocks[:0]
	for _, b := range blocks {
		if b.(map[string]any)["kind"] != "images" {
			kept = append(kept, b)
		}
	}
	return kept
}

// videoDiagnostics finds nothing wrong, so no banner covers the video.
func videoDiagnostics(context.Context) doctor.Report {
	return doctor.Report{Status: doctor.StatusOK, Checks: []doctor.Check{
		{ID: "git", Label: "git", Status: doctor.StatusOK, Detail: "git is available.", Path: "/usr/bin/git", Version: "2.43.0"},
		{ID: "backend", Label: "Model backend", Status: doctor.StatusOK, Detail: "Claude Code is installed and signed in.", Path: "/usr/local/bin/claude", Version: "2.45.0"},
		{ID: "github-token", Label: "GitHub token", Status: doctor.StatusOK, Detail: "Connected to GitHub as " + videoLogin + "."},
	}}
}

// videoSession is an agent session of the video: where it works, what it
// is called and its turn, as its hooks and transcript tell it.
type videoSession struct {
	agent, id, cwd, title string
	repository, branch    string
	pr                    int
	// prompt is the user's; steps are the agent's tool calls and reply is its
	// latest message.
	prompt string
	steps  [][2]string
	reply  string
	// started and last are how long before startup the session started and
	// last did something; state is how it stands: "running", "permission",
	// "turn" (its turn finished) or "ended".
	started, last time.Duration
	state         string
	message       string
}

var videoSessions = []videoSession{
	{"claude-code", "7d0c5c1e-4f8a-4b6e-9a51-3c2f1e0b7a01", "/home/dev/code/fernhill-api", "Cap the webhook retry delay", "fernhill/api", "alex-m/webhook-retries", 379,
		"Cap the webhook retry delay at five minutes and make the sixth-attempt test pass.",
		[][2]string{{"Read", "internal/webhooks/retry.go"}, {"Edit", "internal/webhooks/retry.go"}, {"Bash", "go test ./internal/webhooks/..."}},
		"The delay now stops growing at five minutes. Running the webhook tests again.", 18 * time.Minute, 10 * time.Second, "running", ""},
	{"codex", "019f2a10-5b3c-7d21-8e4f-0a1b2c3d4e02", "/home/dev/code/fernhill-web", "Lazy-load the remaining dashboard charts", "fernhill/web", "alex-m/lazy-charts", 209,
		"Lazy-load the revenue and churn charts too, like the orders chart.",
		[][2]string{{"exec_command", "rg -n \"import .*Chart\" src/dashboard"}, {"apply_patch", "src/dashboard/RevenueChart.tsx"}, {"exec_command", "pnpm test dashboard"}},
		"Revenue and churn now load when they scroll into view. Checking the bundle size.", 12 * time.Minute, 30 * time.Second, "running", ""},
	{"claude-code", "7d0c5c1e-4f8a-4b6e-9a51-3c2f1e0b7a03", "/home/dev/code/fernhill-api-invoices", "Run the invoice job migration", "fernhill/api", "alex-m/invoice-jobs", 381,
		"Add the migration for the invoice jobs table and run the invoice tests.",
		[][2]string{{"Write", "migrations/0042_invoice_jobs.sql"}, {"Edit", "internal/invoices/job.go"}},
		"The migration is written. I need to run the invoice tests against it.", 9 * time.Minute, 2 * time.Minute, "permission", "Run go test ./internal/invoices/..."},
	{"codex", "019f2a10-5b3c-7d21-8e4f-0a1b2c3d4e04", "/home/dev/code/fernhill-mobile", "Handle offline draft conflicts", "fernhill/mobile", "priya-s/offline-drafts", 92,
		"When a draft changed on two devices while offline, keep the newest and show a notice.",
		[][2]string{{"exec_command", "rg -n syncDraft src/notes"}, {"apply_patch", "src/notes/sync.ts"}, {"exec_command", "pnpm test notes"}},
		"Conflicting drafts now keep the newest copy and show a notice. All notes tests pass.", 25 * time.Minute, 4 * time.Minute, "turn", ""},
	{"claude-code", "7d0c5c1e-4f8a-4b6e-9a51-3c2f1e0b7a05", "/home/dev/code/fernhill-web-datepicker", "Upgrade the date picker", "fernhill/web", "alex-m/date-picker", 205,
		"Upgrade the date picker to v7 and fix the breaking changes.",
		[][2]string{{"Bash", "pnpm up react-day-picker@7"}, {"Edit", "src/components/DateField.tsx"}, {"Bash", "pnpm test"}},
		"Upgraded to v7; the two call sites use the new props and the tests pass.", 2 * time.Hour, time.Hour, "ended", ""},
}

// videoLinker links each video session's folder to its repository, branch
// and pull request.
type videoLinker struct{}

func (videoLinker) Link(_ context.Context, cwd string) (livesessions.Link, bool) {
	for _, s := range videoSessions {
		if s.cwd == cwd {
			return livesessions.Link{Repository: s.repository, Branch: s.branch, PR: s.pr}, true
		}
	}
	return livesessions.Link{}, false
}

// videoTranscript writes the session's transcript where its agent keeps it
// and returns its path: its title, prompt, tool calls and latest message,
// spread from start to the session's last activity.
func videoTranscript(t *testing.T, home string, s videoSession, now time.Time) string {
	t.Helper()
	start, last := now.Add(-s.started), now.Add(-s.last)
	at := func(i, n int) string {
		return start.Add(time.Duration(i) * last.Sub(start) / time.Duration(n)).UTC().Format("2006-01-02T15:04:05.000Z")
	}
	var lines []any
	var path string
	n := len(s.steps) + 2
	if s.agent == "codex" {
		path = filepath.Join(home, ".codex", "sessions", start.Format("2006/01/02"), "rollout-"+start.UTC().Format("2006-01-02T15-04-05")+"-"+s.id+".jsonl")
		lines = append(lines,
			map[string]any{"timestamp": at(0, n), "type": "session_meta", "payload": map[string]any{"id": s.id, "cwd": s.cwd, "cli_version": "0.69.0", "model_provider": "openai"}},
			map[string]any{"timestamp": at(0, n), "type": "event_msg", "payload": map[string]any{"type": "user_message", "message": s.prompt}})
		for i, step := range s.steps {
			call := fmt.Sprintf("call_%d", i)
			if step[0] == "apply_patch" {
				lines = append(lines, map[string]any{"timestamp": at(i+1, n), "type": "response_item", "payload": map[string]any{"type": "custom_tool_call", "name": "apply_patch",
					"input": "*** Begin Patch\n*** Update File: " + s.cwd + "/" + step[1] + "\n@@\n+// updated\n*** End Patch", "call_id": call}})
			} else {
				args, _ := json.Marshal(map[string]any{"cmd": step[1], "workdir": s.cwd})
				lines = append(lines, map[string]any{"timestamp": at(i+1, n), "type": "response_item", "payload": map[string]any{"type": "function_call", "name": "exec_command", "arguments": string(args), "call_id": call}})
			}
			lines = append(lines, map[string]any{"timestamp": at(i+1, n), "type": "response_item", "payload": map[string]any{"type": "function_call_output", "call_id": call, "output": "ok"}})
		}
		lines = append(lines, map[string]any{"timestamp": at(n, n), "type": "response_item", "payload": map[string]any{"type": "message", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": s.reply}}}})
		index, _ := json.Marshal(map[string]any{"id": s.id, "thread_name": s.title, "updated_at": at(0, n)})
		indexPath := filepath.Join(home, ".codex", "session_index.jsonl")
		if err := os.MkdirAll(filepath.Dir(indexPath), 0o700); err != nil {
			t.Fatal(err)
		}
		f, err := os.OpenFile(indexPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = f.Write(append(index, '\n'))
		_ = f.Close()
	} else {
		path = filepath.Join(home, ".claude", "projects", strings.ReplaceAll(s.cwd, "/", "-"), s.id+".jsonl")
		common := func(kind, uuid, parent, ts string) map[string]any {
			return map[string]any{"type": kind, "uuid": uuid, "parentUuid": parent, "sessionId": s.id, "cwd": s.cwd, "gitBranch": s.branch, "isSidechain": false, "timestamp": ts}
		}
		line := common("user", "u0", "", at(0, n))
		line["message"] = map[string]any{"role": "user", "content": s.prompt}
		lines = append(lines, map[string]any{"type": "ai-title", "aiTitle": s.title, "sessionId": s.id}, line)
		parent := "u0"
		for i, step := range s.steps {
			input := map[string]any{"command": step[1]}
			if step[0] != "Bash" {
				input = map[string]any{"file_path": s.cwd + "/" + step[1]}
			}
			call := fmt.Sprintf("a%d", i)
			use := common("assistant", call, parent, at(i+1, n))
			use["message"] = map[string]any{"id": "msg_" + call, "role": "assistant", "model": "claude-opus-5", "stop_reason": "tool_use", "usage": map[string]any{"input_tokens": 1200, "output_tokens": 80},
				"content": []any{map[string]any{"type": "tool_use", "id": "toolu_" + call, "name": step[0], "input": input}}}
			result := common("user", "r"+call, call, at(i+1, n))
			result["message"] = map[string]any{"role": "user", "content": []any{map[string]any{"type": "tool_result", "tool_use_id": "toolu_" + call, "content": "ok"}}}
			lines = append(lines, use, result)
			parent = "r" + call
		}
		reply := common("assistant", "final", parent, at(n, n))
		reply["message"] = map[string]any{"id": "msg_final", "role": "assistant", "model": "claude-opus-5", "stop_reason": "end_turn", "usage": map[string]any{"input_tokens": 1200, "output_tokens": 80},
			"content": []any{map[string]any{"type": "text", "text": s.reply}}}
		lines = append(lines, reply)
	}
	var b strings.Builder
	for _, l := range lines {
		raw, _ := json.Marshal(l)
		b.Write(raw)
		b.WriteByte('\n')
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// recordVideoSessions records each session's hook events, back-dated, so the
// tracker finds them in the state the video wants from startup.
func recordVideoSessions(events *hookevents.Store, transcripts map[string]string, now time.Time) error {
	for _, s := range videoSessions {
		add := func(name string, ago time.Duration) error {
			e := hookevents.New(hookevents.KindAgent, s.agent, name, s.cwd, "fixture")
			e.Time = now.Add(-ago).UTC()
			e.Session = &hookevents.Session{ID: s.id, Cwd: s.cwd, TranscriptPath: transcripts[s.id]}
			if name == "notification" {
				e.Session.NotificationType, e.Session.Message = "permission_prompt", s.message
			}
			_, err := events.Add(e, hookevents.ViaSocket)
			return err
		}
		names := map[string]string{"running": "post-tool-use", "permission": "notification", "turn": "stop", "ended": "session-end"}
		for _, step := range []struct {
			name string
			ago  time.Duration
		}{{"session-start", s.started}, {"user-prompt-submit", s.started - time.Second}, {"post-tool-use", (s.started + s.last) / 2}, {names[s.state], s.last}} {
			if err := add(step.name, step.ago); err != nil {
				return err
			}
		}
	}
	return nil
}

// videoClone names the sessions' person.
type videoClone struct{}

func (videoClone) Person(context.Context) *decisionlog.Person {
	name := "Alex Morgan"
	return &decisionlog.Person{Email: "alex@example.com", Name: &name}
}

func (videoClone) Review(context.Context, int, string) (string, string) { return "", "none" }

// videoTimeline seeds the fernhill repositories' decision store: four
// summarized features, each with a session of decisions linked through a
// member pull request's commit.
func videoTimeline(t *testing.T, home string, registry *repos.Registry) *decisionlog.Timeline {
	t.Helper()
	ctx := context.Background()
	path := filepath.Join(home, "decisions", "decisions.sqlite")
	st, err := decisionlog.Open(path, decisionlog.OpenOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	now := time.Now()
	ago := func(d time.Duration) time.Time { return now.Add(-d) }
	h := time.Hour
	type decision struct{ title, rationale string }
	type pull struct {
		number        int
		title, author string
		merged        bool
		opened        time.Duration
	}
	features := []struct {
		id, repository, title, description, session string
		at                                          time.Time
		pulls                                       []pull
		decisions                                   []decision
		key                                         int
	}{
		{"feature-webhooks", "fernhill/api", "Webhook reliability", "Retries failed webhook deliveries and keeps the ones that still fail for replay.", "video-webhooks", ago(4 * 24 * h),
			[]pull{{372, "Store failed webhook deliveries for replay", videoLogin, true, 5 * 24 * h}, {379, "Retry webhook deliveries with backoff", videoLogin, false, 3 * h}},
			[]decision{
				{"Retry deliveries with exponential backoff, capped at 6 attempts", "Most receivers recover within minutes, and six attempts cover about an hour without hammering one that is down."},
				{"Keep failed deliveries for replay instead of dropping them", "A customer whose endpoint was down can get every missed event back once it is fixed."},
			}, 2},
		{"feature-filters", "fernhill/web", "Saved filters for orders", "Lets people save the orders table's filters and columns and come back to them.", "video-filters", ago(4 * 24 * h),
			[]pull{{198, "Orders table: column picker", "maya-r", true, 5 * 24 * h}, {214, "Add saved filters to the orders table", "maya-r", false, 26 * h}},
			[]decision{
				{"Store saved filters per user, not per team", "People filter the same table in different ways, and a shared list would grow noisy."},
				{"Encode filters in the URL", "A filtered view can be bookmarked and shared without saving it first."},
				{"Keep the column picker's choice with the saved filter", "A saved view should look the same when it is opened again."},
			}, 3},
		{"feature-invoices", "fernhill/api", "Background invoice generation", "Renders invoice PDFs in a background job instead of during the request.", "video-invoices", ago(3 * 24 * h),
			[]pull{{381, "Move invoice PDFs to a background job", videoLogin, false, 3 * 24 * h}},
			[]decision{
				{"Render invoice PDFs in a background job", "Large invoices took over ten seconds and timed out the checkout request."},
				{"Email the invoice link once the PDF is ready", "Customers no longer wait on the confirmation page for a file they rarely open there."},
			}, 2},
		{"feature-notes", "fernhill/mobile", "Offline notes on mobile", "Keeps notes and drafts on the device so the notes screen works without a connection.", "video-notes", ago(6 * 24 * h),
			[]pull{{88, "Local storage for notes", "priya-s", true, 7 * 24 * h}, {92, "Offline drafts for the notes screen", "priya-s", false, 30 * h}},
			[]decision{
				{"Store notes in SQLite on the device", "It survives app restarts and handles a few thousand notes without paging."},
				{"Sync drafts when the app is back online, last write wins", "Notes are edited by one person, so conflicts are rare and simple to settle."},
				{"Show a badge on notes not yet synced", "People should know which notes exist only on this phone."},
			}, 3},
	}
	for _, f := range features {
		var ds []decisions.Decision
		times := map[int]time.Time{}
		for i, d := range f.decisions {
			ds = append(ds, decisions.Decision{ID: fmt.Sprintf("%s-%d", f.id, i+1), Title: d.title, Summary: d.title + ".", Rationale: d.rationale,
				Category: decisions.CategoryArchitecture, Who: decisions.WhoHuman, Turns: []int{i + 1}, Files: []string{}})
			times[i+1] = f.at.Add(time.Duration(i) * 25 * time.Minute)
		}
		if err := st.Save(ctx, decisionlog.Extraction{Meta: decisionlog.Meta{Agent: "claude-code", ID: f.session, Repository: f.repository, Branch: "main"},
			ContentHash: "c-" + f.session, Decisions: ds, TurnTimes: times}); err != nil {
			t.Fatal(err)
		}
	}
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(10000)")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.Exec(query, args...); err != nil {
			t.Fatal(err)
		}
	}
	ms := func(at time.Time) int64 { return at.UnixMilli() }
	for _, f := range features {
		version, err := st.LiveVersion(ctx, f.repository)
		if err != nil {
			t.Fatal(err)
		}
		updated := f.at
		for i, p := range f.pulls {
			opened := ago(p.opened)
			state, merged := "open", any(nil)
			last := ago(p.opened / 3)
			if p.merged {
				state, merged, last = "closed", ms(opened.Add(20*h)), opened.Add(20*h)
			}
			if last.After(updated) {
				updated = last
			}
			exec(`INSERT INTO pull_request(repository, number, title, state, author, head_sha, base_sha, created_at, updated_at, merged_at)
VALUES (?,?,?,?,?,?,'base',?,?,?)`, f.repository, p.number, p.title, state, p.author, fmt.Sprintf("head-%d", p.number), ms(opened), ms(last), merged)
			exec(`INSERT INTO feature_pull(version_id, repository, number, feature_id, assigned_by, assigned_at) VALUES (?,?,?,?,'assigner',?)`,
				version, f.repository, p.number, f.id, ms(opened))
			if i == 0 {
				// The session's commit is in the first pull request.
				sha := fmt.Sprintf("commit-%d", p.number)
				exec(`INSERT INTO pull_commit(repository, number, sha, committed_at) VALUES (?,?,?,?)`, f.repository, p.number, sha, ms(f.at.Add(time.Hour)))
				exec(`INSERT INTO session_commit(agent, session_id, repository, sha, method, confidence) VALUES ('claude-code',?,?,?,'checkpoint',1)`, f.session, f.repository, sha)
			}
		}
		var keys []map[string]any
		for i := range f.key {
			keys = append(keys, map[string]any{"id": fmt.Sprintf("%s-%d", f.id, i+1), "agent": "claude-code", "session": f.session, "turn": i + 1})
		}
		raw, _ := json.Marshal(keys)
		exec(`INSERT INTO feature(id, version_id, repository, title, description, digest, key_decisions_json, membership_version, summarized_version,
changed_at, created_at, updated_at) VALUES (?,?,?,?,?,?,?,1,1,?,?,?)`, f.id, version, f.repository, f.title, f.description, f.description, string(raw), ms(updated), ms(f.at), ms(updated))
	}
	return &decisionlog.Timeline{Store: st, Registry: registry, Home: home,
		Clone: func(context.Context, string) decisionlog.TimelineClone { return videoClone{} },
		Login: func(context.Context) string { return videoLogin }}
}

// TestLocalAppVideoFixture is an opt-in process for recording the product
// video. Like TestLocalAppBrowserFixture it runs the real daemon with only
// GitHub and the model backend synthetic, but with the fictional fernhill
// organisation's repositories, pull requests, reviews and Timeline, on port
// 4168. Fixing fernhill/api#379 runs the real fix job to a push into a local
// bare repository. The launch URL is in the credential file in
// BAUM_APP_STATE_DIR.
func TestLocalAppVideoFixture(t *testing.T) {
	if os.Getenv("BAUM_APP_VIDEO_FIXTURE") != "1" {
		t.Skip("video fixture process")
	}
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GH_TOKEN", "")
	home := appdbtest.TempDir(t)
	if err := os.Chmod(home, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	state := os.Getenv("BAUM_APP_STATE_DIR")
	if err := os.MkdirAll(state, 0o700); err != nil {
		t.Fatal(err)
	}
	remotes := filepath.Join(state, "remotes")
	if err := os.RemoveAll(remotes); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	registry, err := repos.Open(filepath.Join(home, "app.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = registry.Close() }()

	repo := map[string]videoRepo{}
	head := map[string]string{}
	for _, name := range videoRepositories {
		repo[name] = newVideoRepo(t, name, remotes)
		head[name] = repo[name].Head
	}
	// fernhill/api#379 has its own head: the retry policy the video fixes,
	// on top of the shared feature branch, which merges cleanly with main.
	api := repo["fernhill/api"]
	assembletest.Git(t, api.Dir, "checkout", "-q", "-b", "alex-m/webhook-retries", api.Head)
	assembletest.Write(t, api.Dir, "internal/webhooks/retry.go", webhookRetrySource(false))
	assembletest.Git(t, api.Dir, "add", "-A")
	assembletest.Git(t, api.Dir, "commit", "-q", "--author", "Alex Morgan <alex@example.com>", "-m", "Retry webhook deliveries with backoff")
	fixHead := assembletest.Git(t, api.Dir, "rev-parse", "HEAD")
	assembletest.Git(t, api.Dir, "checkout", "-q", "main")

	pulls := videoPulls(head, fixHead)
	clones := map[string]repos.Clone{}
	for _, name := range videoRepositories {
		repo[name].publish(t, pulls)
		if clones[name], err = registry.Add(ctx, repo[name].Dir, nil); err != nil {
			t.Fatal(err)
		}
		repo[name].redirect(t)
	}
	raw, err := os.ReadFile(filepath.Join(os.Getenv("BAUM_APP_SCHEMA_DIR"), "review-example.json"))
	if err != nil {
		t.Fatal(err)
	}
	saveVideoReviews(t, raw, pulls, clones, repo)
	// Both plans show in the sidebar, as "Show Codex plan usage" leaves it.
	if _, err := registry.UpdatePreferences(ctx, func(p repos.Preferences) repos.Preferences {
		p.PlanUsage.Show = map[string]bool{"codex": true}
		return p
	}); err != nil {
		t.Fatal(err)
	}
	timeline := videoTimeline(t, home, registry)
	disc := videoDiscovery{}
	for _, p := range pulls {
		disc[p.Repository] = append(disc[p.Repository], p.PullRequest)
	}

	cfg := config.Default()
	cfg.Backend = config.BackendClaude
	cfg.Model = "claude-sonnet-4-5"
	// The signed-in GitHub account, whose token the fake GitHub accepts.
	credentials := &browserCredentialStore{mode: func() string { return "" }, credential: githubauth.Credential{AccessToken: "fixture-access-" + videoLogin, RefreshToken: "fixture-refresh",
		Login: videoLogin, ID: 7, ExpiresAt: time.Now().Add(24 * time.Hour).Unix(), RefreshExpiresAt: time.Now().Add(48 * time.Hour).Unix()}}
	manager := githubauth.NewWithOptions(credentials, githubauth.Options{Transport: browserTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/user" {
			return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"X-Oauth-Scopes": []string{"repo, read:org"}}, Body: io.NopCloser(strings.NewReader(`{"id":7,"login":"` + videoLogin + `"}`)), Request: r}, nil
		}
		return nil, fmt.Errorf("unexpected GitHub auth request: %s", r.URL.Path)
	})})
	previous := config.GitHubAuth
	config.GitHubAuth = manager
	defer func() { config.GitHubAuth = previous }()
	client := github.New(cfg.ResolveGitHubToken, github.Options{Transport: videoGitHub(pulls)})
	// Reviews are already current for every head; a regenerated one takes
	// a few seconds, as in the browser fixture.
	backend := &generatetest.Backend{Version: "fixture", RunFunc: func(ctx context.Context, r agent.Request, n int) (agent.Result, error) {
		select {
		case <-ctx.Done():
			return agent.Result{}, ctx.Err()
		case <-time.After(time.Second):
		}
		result, e := generatetest.StagedReplies(nil)(ctx, r, n)
		result.Model, result.Calls, result.Tokens = cfg.Model, 1, 100
		return result, e
	}}
	index := &reviewindex.Service{Registry: registry, Discovery: disc}
	paths := daemon.Paths{State: state, Socket: filepath.Join(state, "daemon.sock")}
	runCtx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	_ = os.RemoveAll(filepath.Join(state, "events"))
	// The agent sessions, as they stand when the fixture starts.
	started := time.Now()
	sessionTranscripts := map[string]string{}
	for _, s := range videoSessions {
		sessionTranscripts[s.id] = videoTranscript(t, home, s, started)
	}
	err = daemon.Run(runCtx, daemon.Options{Paths: paths, Version: "fixture", Port: 4168, Start: func(appCtx context.Context, server *uiserver.Server) (daemon.App, error) {
		events, err := hookevents.OpenStore(hookevents.RecordDir(paths), nil)
		if err != nil {
			return daemon.App{}, err
		}
		if err := recordVideoSessions(events, sessionTranscripts, started); err != nil {
			_ = events.Close()
			return daemon.App{}, err
		}
		live := &livesessions.Tracker{Source: events, Linker: videoLinker{}, Notify: func() { _ = server.Publish("live", struct{}{}) }}
		transcripts := &livesessions.Transcripts{Tracker: live, Render: transcript.NewRenderer("").Render}
		timeline.Transcripts = transcripts
		decided, err := decisionlog.Open(filepath.Join(home, "decisions.sqlite"), decisionlog.OpenOptions{})
		if err != nil {
			_ = events.Close()
			return daemon.App{}, err
		}
		scheduler := &automatic.Scheduler{Registry: registry, Path: filepath.Join(home, "app.sqlite"), Discover: disc.Poll, Interval: 500 * time.Millisecond,
			Notify: func(status automatic.Status) { _ = server.Publish("automatic", status) }}
		groups, err := reviewgroups.Open(filepath.Join(home, "app.sqlite"))
		if err != nil {
			_ = events.Close()
			return daemon.App{}, err
		}
		runner, err := jobs.Open(filepath.Join(home, "app.sqlite"), &jobs.Local{Registry: registry, Groups: groups, Config: cfg, Client: client, FixDirectory: filepath.Join(home, "fixes"), Features: fixtureBuilder{},
			Backends: generate.Backends{Claude: func(string) generate.CheckedBackend { return videoFixer{backend} }}}, 1, func(job jobs.Job) {
			_ = server.Publish("progress", map[string]any{"job": job})
			_ = server.Publish("index", map[string]any{})
			if job.Phase == "finished" {
				scheduler.Wake()
			}
		})
		if err != nil {
			return daemon.App{}, err
		}
		scheduler.Queue = runner
		runner.SetAutomaticGate(scheduler.Gate)
		auth := uiserver.NewGitHubAuthentication(appCtx, manager, cfg, func() { _ = server.Publish("index", struct{}{}) })
		changed := func() { scheduler.Wake(); _ = server.Publish("index", struct{}{}) }
		poll := func(ctx context.Context, repo github.Repository) { _, _ = disc.Poll(ctx, repo) }
		// The first-run tour would cover the pages the video records.
		uiState := &uistate.Store{Path: filepath.Join(home, "app.sqlite")}
		if _, err := uiState.Replace("tour", "", 0, "video-fixture:1", json.RawMessage(`{"seen":true}`)); err != nil {
			return daemon.App{}, err
		}
		api := uiserver.LocalAPI{GitHubAuth: auth.Handler(), Registry: registry, Config: cfg, PRs: uiserver.DiscoveryHandler(registry, disc, nil),
			Merge:  uiserver.MergeHandler(registry, client, func(ctx context.Context, repo github.Repository, _ int, _ string) { poll(ctx, repo) }, changed),
			Update: uiserver.UpdateHandler(registry, prupdate.Service{Client: client}, poll, changed),
			Index:  index.Handler(), Jobs: runner.Handler(), Chat: uiserver.ChatHandler(registry, runner), Automatic: scheduler.Handler(), Live: transcripts.Handler(), Changed: scheduler.Wake,
			Diagnostics: videoDiagnostics, PlanUsage: videoPlanUsage, Timeline: timeline.Handler(), UIState: uiState.Handler(),
			Inboxes:   (&inboxes.API{Store: inboxes.Open(filepath.Join(home, "app.sqlite"))}).Handler(),
			Groups:    (&reviewgroups.API{Store: groups, Registry: registry, Index: index.Build, Changed: func() { _ = server.Publish("index", struct{}{}) }}).Handler(),
			Decisions: browserDecisionsAPI(decided, registry, live),
			DecisionModel: func(ctx context.Context, prefs repos.Preferences) uiserver.DecisionModel {
				e := decisionlog.EffectiveModel(ctx, cfg, prefs, generate.Backends{}, nil)
				return uiserver.DecisionModel{Backend: e.Backend, Undetermined: e.Undetermined, Model: e.Model, Ignored: e.Ignored}
			},
			DecisionsChanged: func() { _ = server.Publish("decisions", struct{}{}) }}.Handler()
		return daemon.App{API: api, Assets: localapp.Handler(), Shutdown: runner.Shutdown, Close: func() { api.Close(); _ = events.Close(); _ = groups.Close(); _ = decided.Close() }, Drain: runner.Drain, Resume: runner.Resume,
			Run: func(ctx context.Context) {
				go scheduler.Run(ctx)
				go live.Run(ctx)
				ticker := time.NewTicker(250 * time.Millisecond)
				defer ticker.Stop()
				for {
					for _, name := range videoRepositories {
						if err := scheduler.Trigger(ctx, name); err != nil && ctx.Err() == nil {
							t.Log("automatic trigger:", err)
						}
					}
					select {
					case <-ctx.Done():
						return
					case <-ticker.C:
					}
				}
			}}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
}
