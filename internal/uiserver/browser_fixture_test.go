package uiserver_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/baum-ai/baum/agent"
	"github.com/baum-ai/baum/internal/analytics"
	"github.com/baum-ai/baum/internal/assemble/assembletest"
	"github.com/baum-ai/baum/internal/automatic"
	"github.com/baum-ai/baum/internal/clonefind"
	"github.com/baum-ai/baum/internal/config"
	"github.com/baum-ai/baum/internal/daemon"
	"github.com/baum-ai/baum/internal/decisionlog"
	"github.com/baum-ai/baum/internal/discovery"
	"github.com/baum-ai/baum/internal/doctor"
	"github.com/baum-ai/baum/internal/generate"
	"github.com/baum-ai/baum/internal/generate/generatetest"
	"github.com/baum-ai/baum/internal/github"
	"github.com/baum-ai/baum/internal/githubrepos"
	"github.com/baum-ai/baum/internal/gitlocal"
	"github.com/baum-ai/baum/internal/hookevents"
	"github.com/baum-ai/baum/internal/inboxes"
	"github.com/baum-ai/baum/internal/jobs"
	"github.com/baum-ai/baum/internal/livesessions"
	"github.com/baum-ai/baum/internal/localapp"
	"github.com/baum-ai/baum/internal/planusage"
	"github.com/baum-ai/baum/internal/prupdate"
	"github.com/baum-ai/baum/internal/repos"
	"github.com/baum-ai/baum/internal/reviewgroups"
	"github.com/baum-ai/baum/internal/reviewindex"
	"github.com/baum-ai/baum/internal/store"
	"github.com/baum-ai/baum/internal/transcript"
	"github.com/baum-ai/baum/internal/uiserver"
)

// browserDiscovery serves fixed pull requests. Once pushed reports true (the
// browser test writes a marker file), PR 10 has a new head: the push the
// automatic generation scheduler reacts to.
type browserDiscovery struct {
	snapshot discovery.Snapshot
	pushed   func() bool
	// merged reports whether the browser test merged PR 7 through the fake
	// GitHub, after which discovery no longer lists it as open.
	merged func() bool
	// updated and rerun report whether the browser test updated PR 11's
	// branch or re-ran PR 8's failed checks through the fake GitHub.
	updated, rerun func() bool
	head           string
}

func (d browserDiscovery) Cached(_ context.Context, repository github.Repository) (discovery.Snapshot, error) {
	if repository.String() == "acme/missing" {
		return discovery.Snapshot{Repository: repository.String(), PullRequests: []discovery.PullRequest{{Repository: repository.String(), Number: 42, Title: "A GitHub pull request waiting for a clone", State: "OPEN", URL: "https://github.com/acme/missing/pull/42", ReviewRequestedFromMe: true}}, FetchedAt: time.Now(), Rate: discovery.RateLimit{Remaining: new(5000)}}, nil
	}
	if repository.String() != "acme/demo" {
		return discovery.Snapshot{Repository: repository.String(), PullRequests: []discovery.PullRequest{{Repository: repository.String(), Number: 1, Title: "Other repository change", State: "OPEN", TeamMembershipKnown: true}}, FetchedAt: time.Now(), Rate: discovery.RateLimit{Remaining: new(5000)}}, nil
	}
	snapshot := d.snapshot
	snapshot.FetchedAt = time.Now()
	if d.merged != nil && d.merged() {
		snapshot.PullRequests = slices.DeleteFunc(slices.Clone(snapshot.PullRequests), func(pr discovery.PullRequest) bool { return pr.Number == 7 })
	}
	snapshot.PullRequests = slices.Clone(snapshot.PullRequests)
	for i := range snapshot.PullRequests {
		switch pr := &snapshot.PullRequests[i]; {
		case pr.Number == 11 && d.updated != nil && d.updated():
			pr.Behind, pr.Merge = false, "ready"
		case pr.Number == 8 && d.rerun != nil && d.rerun():
			pr.Checks = "running"
		}
	}
	if d.pushed != nil && d.pushed() {
		snapshot.PullRequests = append([]discovery.PullRequest(nil), snapshot.PullRequests...)
		for i := range snapshot.PullRequests {
			if snapshot.PullRequests[i].Number == 10 {
				snapshot.PullRequests[i].HeadSHA = d.head
			}
		}
	}
	return snapshot, nil
}
func (d browserDiscovery) Poll(ctx context.Context, repository github.Repository) (discovery.Snapshot, error) {
	return d.Cached(ctx, repository)
}
func (d browserDiscovery) PollNow(ctx context.Context, repository github.Repository) (discovery.Snapshot, error) {
	return d.Cached(ctx, repository)
}

// The account listing boundary is synthetic; routing, picking and cloning state
// still use the production local repository service.
type browserRepositoryLister struct{}

func (browserRepositoryLister) ViewerRepositories(context.Context, string) ([]github.ViewerRepository, error) {
	return []github.ViewerRepository{{Repository: "acme/demo", Description: "A registered local repository", OpenPullRequests: 4}, {Repository: "acme/other", Description: "Another registered clone"}, {Repository: "acme/missing", Description: "A repository waiting for its local clone"}, {Repository: "acme/clone-me", Description: "Clone a repository from setup"}}, nil
}

// browserDiagnostics is a fixed report: no machine is inspected.
func browserDiagnostics(context.Context) doctor.Report {
	return doctor.Report{Status: doctor.StatusProblem, Checks: []doctor.Check{
		{ID: "git", Label: "git", Status: doctor.StatusOK, Detail: "git is available.", Path: "/usr/bin/git", Version: "2.43.0"},
		{ID: "backend", Label: "Model backend", Status: doctor.StatusWarning, Detail: "The model backend is older than the version baum was tested with.", Path: "/usr/local/bin/claude", Version: "2.40.0", Fix: "Upgrade the model backend to 2.45.0 or newer."},
		{ID: "github-token", Label: "GitHub token", Status: doctor.StatusProblem, Detail: "No GitHub connection was found.", Fix: "Connect GitHub in baum Settings, or set github_token in ~/.config/baum/a-deliberately-long-directory-name/for-narrow-layouts/config.toml."},
		{ID: "price-list", Label: "Price list", Status: doctor.StatusInfo, Detail: "Using the built-in price list.", Source: "built-in"},
	}}
}

// browserLiveLinker links the one directory the browser test's agent session
// works in to PR 7's branch; the real linkage is tested in livesessions.
// fixtureFixer is the fixture's model backend, which can also fix: it leaves
// a note in the checkout. Its push fails, since the fixture's clone has no
// reachable remote; the app's fix button and confirmation are what the
// fixture is for.
type fixtureFixer struct{ *generatetest.Backend }

func (fixtureFixer) Fix(_ context.Context, r agent.Request) (agent.Result, error) {
	err := os.WriteFile(filepath.Join(r.Directory, "FIXED.md"), []byte("fixed by the fixture\n"), 0o644)
	return agent.Result{Document: []byte("Left a note."), Calls: 1, Tokens: 10}, err
}

type browserLiveLinker struct{}

func (browserLiveLinker) Link(_ context.Context, cwd string) (livesessions.Link, bool) {
	return livesessions.Link{Repository: "acme/demo", Branch: "feature", PR: 7}, cwd == "/fixture/live"
}

// feedLiveSession records a hook event for the Claude Code session id
// whenever the first line of the browser test's marker file changes:
// "running", "idle", "permission" (a permission prompt notification) or
// "ended". With a transcript path, the hooks name it and the marker's other
// lines are the session's prompts: each change of them rewrites the
// transcript there, each prompt with a reply, without a hook event, as an
// agent writes its transcript between hooks.
func feedLiveSession(ctx context.Context, events *hookevents.Store, marker, id, transcript string) {
	names := map[string]string{"running": "user-prompt-submit", "idle": "stop", "permission": "notification", "ended": "session-end"}
	var last, lastPrompts string
	// The turns started a few minutes ago, a minute apart.
	start := time.Now().UTC().Add(-10 * time.Minute).Truncate(time.Second)
	for ticker := time.NewTicker(100 * time.Millisecond); ; {
		if data, err := os.ReadFile(marker); err == nil {
			state, prompts, _ := strings.Cut(string(data), "\n")
			if transcript != "" && prompts != lastPrompts {
				lastPrompts = prompts
				writeFixtureTranscript(transcript, id, start, strings.Split(strings.TrimSpace(prompts), "\n"))
			}
			if state != last {
				last = state
				if name, ok := names[state]; ok {
					e := hookevents.New(hookevents.KindAgent, "claude-code", name, "/fixture/live", "fixture")
					e.Session = &hookevents.Session{ID: id, Cwd: "/fixture/live", TranscriptPath: transcript}
					if name == "notification" {
						e.Session.NotificationType, e.Session.Message = "permission_prompt", "Claude needs your permission to use Bash"
					}
					_, _ = events.Add(e, hookevents.ViaSocket)
				}
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// writeFixtureTitle leaves a Claude Code transcript holding only the title
// Claude Code generated for the session id and, when summary is set, a
// compaction summary whose first section it is, where the store keeps it, so
// the session's hooks need not name it: the Sessions page finds it by the id.
func writeFixtureTitle(home, id, title, summary string) {
	line, _ := json.Marshal(map[string]any{"type": "ai-title", "aiTitle": title, "sessionId": id})
	lines := append(line, '\n')
	if summary != "" {
		text := "This session is being continued from a previous conversation that ran out of context.\n\nSummary:\n" +
			"1. Primary Request and Intent:\n   " + summary + "\n2. Key Technical Concepts:\n   - The PR page's viewer\n"
		compacted, _ := json.Marshal(map[string]any{"type": "user", "isCompactSummary": true, "sessionId": id, "uuid": "compacted",
			"message": map[string]any{"role": "user", "content": text}})
		lines = append(append(lines, compacted...), '\n')
	}
	path := filepath.Join(home, ".claude", "projects", "-fixture-live", id+".jsonl")
	_ = os.MkdirAll(filepath.Dir(path), 0o700)
	_ = os.WriteFile(path, lines, 0o600)
}

// writeFixtureTranscript replaces the Claude Code transcript at path with one
// turn per prompt, each answered "Done: <prompt>", in the lines Claude Code
// writes.
func writeFixtureTranscript(path, id string, start time.Time, prompts []string) {
	var lines strings.Builder
	for i, prompt := range prompts {
		if prompt == "" {
			continue
		}
		at := start.Add(time.Duration(i) * time.Minute)
		user, _ := json.Marshal(map[string]any{"parentUuid": nil, "isSidechain": false, "cwd": "/fixture/live", "gitBranch": "feature",
			"type": "user", "uuid": fmt.Sprintf("u-%d", i), "sessionId": id, "timestamp": at.Format(time.RFC3339Nano), "promptId": fmt.Sprintf("p-%d", i),
			"message": map[string]any{"role": "user", "content": prompt}})
		reply, _ := json.Marshal(map[string]any{"parentUuid": fmt.Sprintf("u-%d", i), "isSidechain": false, "cwd": "/fixture/live", "gitBranch": "feature",
			"type": "assistant", "uuid": fmt.Sprintf("a-%d", i), "sessionId": id, "timestamp": at.Add(20 * time.Second).Format(time.RFC3339Nano), "requestId": fmt.Sprintf("req-%d", i),
			"message": map[string]any{"id": fmt.Sprintf("msg-%d", i), "role": "assistant", "model": "claude-opus-5", "stop_reason": "end_turn",
				"usage":   map[string]any{"input_tokens": 1200, "output_tokens": 80},
				"content": []any{map[string]any{"type": "text", "text": "Done: " + prompt}}}})
		lines.Write(user)
		lines.WriteByte('\n')
		lines.Write(reply)
		lines.WriteByte('\n')
	}
	_ = os.MkdirAll(filepath.Dir(path), 0o700)
	temporary := path + ".tmp"
	if os.WriteFile(temporary, []byte(lines.String()), 0o600) == nil {
		_ = os.Rename(temporary, path)
	}
}

type browserTransport func(*http.Request) (*http.Response, error)

func (f browserTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// TestLocalAppBrowserFixture is an opt-in process used by Playwright. It runs
// the real daemon (lock, credential file, control socket and app transport)
// with a real registry, store, queue, job runner and generation publisher;
// only GitHub and the model backend are synthetic. No developer stores are
// read. The browser opens the launch URL from the daemon's credential file in
// BAUM_APP_STATE_DIR, as the desktop app does.
func TestLocalAppBrowserFixture(t *testing.T) {
	if os.Getenv("BAUM_APP_BROWSER_FIXTURE") != "1" {
		t.Skip("Playwright fixture process")
	}
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GH_TOKEN", "")
	home := t.TempDir()
	if err := os.Chmod(home, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	repo := assembletest.NewRepo(t, assembletest.Options{})
	assembletest.Git(t, repo.Dir, "remote", "add", "origin", "https://github.com/acme/demo")
	registry, err := repos.Open(filepath.Join(home, "repos.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = registry.Close() }()
	clone, err := registry.Add(context.Background(), repo.Dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	other := assembletest.NewRepo(t, assembletest.Options{})
	assembletest.Git(t, other.Dir, "remote", "add", "origin", "https://github.com/acme/other")
	if _, err := registry.Add(context.Background(), other.Dir, nil); err != nil {
		t.Fatal(err)
	}
	// Analytics: agent sessions in the demo clone, the person's commits in
	// the other, and plan usage samples.
	writeAnalyticsSessions(t, home, repo.Dir)
	commitRecently(t, other.Dir)
	writePlanHistory(t, filepath.Join(home, "plan-usage-history.json"))
	if _, err := registry.Pick(context.Background(), "acme/missing", nil); err != nil {
		t.Fatal(err)
	}
	timeline := browserTimeline(t, home, registry)
	versions, err := store.Open(clone.CommonDir, "", true)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(os.Getenv("BAUM_APP_SCHEMA_DIR"), "review-example.json"))
	if err != nil {
		t.Fatal(err)
	}
	var previous store.Version
	for n := 1; n <= 2; n++ {
		var doc map[string]any
		if err = json.Unmarshal(raw, &doc); err != nil {
			t.Fatal(err)
		}
		review := doc["review"].(map[string]any)
		review["repository"] = "acme/demo"
		review["pull_request"] = 7
		review["version"] = n
		review["previous_version"] = nil
		if n > 1 {
			review["previous_version"] = n - 1
		}
		review["pr_state"] = "open"
		review["head"] = repo.Head
		if n == 1 {
			review["head"] = repo.MergeBase
			review["base"] = repo.MergeBase
		}
		review["base"] = repo.Base
		if n == 1 {
			review["base"] = repo.MergeBase
		}
		review["merge_base"] = repo.MergeBase
		doc["summary"].(map[string]any)["text"] = fmt.Sprintf("Local fixture review version %d", n)
		doc["summary"].(map[string]any)["review_minutes"] = 12
		id := fmt.Sprintf("att_%032d", n)
		doc["index"].(map[string]any)["attachments"] = []any{map[string]any{"id": id, "alt": fmt.Sprintf("Captured image v%d", n), "position": 1, "source_url": "https://github.com/user-attachments/assets/fixture", "content_type": "image/png", "width": 1, "height": 1}}
		for _, section := range doc["sections"].([]any) {
			for _, block := range section.(map[string]any)["blocks"].([]any) {
				b := block.(map[string]any)
				if b["kind"] == "images" {
					b["items"] = []any{map[string]any{"attachment": id, "caption": fmt.Sprintf("Captured image v%d", n)}}
				}
			}
		}
		document, _ := json.Marshal(doc)
		next := store.Version{Metadata: store.Metadata{Version: n, Head: repo.Head, GeneratedAt: time.Now().UTC().Format(time.RFC3339), Backend: "claude", Model: "claude-sonnet-4-5"}, Document: document, Attachments: []store.Attachment{{ID: id, ContentType: "image/png", Bytes: assembletest.PNG}}}
		if err = versions.Put(context.Background(), store.Key{Repository: "acme/demo", Number: 7}, previous, next); err != nil {
			t.Fatal(err)
		}
		previous = next
	}
	write := true
	prs := []discovery.PullRequest{{Repository: "acme/demo", Number: 7, HeadSHA: repo.Head, Title: "Reuse the existing PR page", State: "OPEN", URL: "https://github.com/acme/demo/pull/7", ReviewRequestedFromMe: true, AuthorHasWrite: &write, TeamMembershipKnown: true}, {Repository: "acme/demo", Number: 8, HeadSHA: repo.Head, Title: "A pull request without a review", State: "OPEN", URL: "https://github.com/acme/demo/pull/8", AuthoredByMe: true, AuthorHasWrite: &write, TeamMembershipKnown: true}, {Repository: "acme/demo", Number: 9, HeadSHA: repo.Head, Title: "A fork requiring confirmation", State: "OPEN", URL: "https://github.com/acme/demo/pull/9", Fork: true, AuthorHasWrite: &write, TeamMembershipKnown: true}, {Repository: "acme/demo", Number: 10, HeadSHA: repo.MergeBase, Title: "An automatically reviewed change", State: "OPEN", URL: "https://github.com/acme/demo/pull/10", AuthoredByMe: true, AuthorHasWrite: &write, TeamMembershipKnown: true}, {Repository: "acme/demo", Number: 11, HeadSHA: repo.Head, Title: "A branch behind its base", State: "OPEN", URL: "https://github.com/acme/demo/pull/11", AuthoredByMe: true, AuthorHasWrite: &write, TeamMembershipKnown: true}}
	prs[0].Review, prs[0].Checks, prs[0].Merge, prs[0].Unresolved = "approved", "passing", "ready", new(0)
	prs[1].Review, prs[1].Checks, prs[1].Merge, prs[1].Unresolved = "changes_requested", "failing", "conflict", new(3)
	prs[2].Review, prs[2].Checks, prs[2].Merge, prs[2].Unresolved = "commented", "running", "blocked", new(6)
	prs[2].UnresolvedCapped = true
	prs[3].Review, prs[3].Checks, prs[3].Merge = "unreviewed", "none", "draft"
	prs[4].Review, prs[4].Checks, prs[4].Merge, prs[4].Behind = "approved", "passing", "blocked", true
	// The viewer may push to acme/demo, so PR 8's conflict and failing checks
	// offer a fix.
	for i := range prs {
		prs[i].ViewerCanPush = &write
	}

	// The browser test pushes PR 10 by writing this marker; a marker left by
	// an earlier run must not count as already seen.
	pushMarker := filepath.Join(os.Getenv("BAUM_APP_STATE_DIR"), "push-10")
	_ = os.Remove(pushMarker)
	// A merge writes this marker, which drops PR 7 from the open list; the
	// merge test removes it again, so the specs after it still find PR 7.
	mergedMarker := filepath.Join(os.Getenv("BAUM_APP_STATE_DIR"), "merged-7")
	_ = os.Remove(mergedMarker)
	merged := func() bool {
		_, err := os.Stat(mergedMarker)
		return err == nil
	}
	// Updating PR 11's branch and re-running PR 8's checks write these
	// markers; their tests remove them again for the specs after them.
	marker := func(name string) (string, func() bool) {
		path := filepath.Join(os.Getenv("BAUM_APP_STATE_DIR"), name)
		_ = os.Remove(path)
		return path, func() bool {
			_, err := os.Stat(path)
			return err == nil
		}
	}
	updatedMarker, updated := marker("updated-11")
	rerunMarker, rerun := marker("rerun-8")
	disc := browserDiscovery{merged: merged, updated: updated, rerun: rerun, snapshot: discovery.Snapshot{Repository: "acme/demo", PullRequests: prs, FetchedAt: time.Now(), Rate: discovery.RateLimit{Remaining: new(5000)}}, head: repo.Head, pushed: func() bool {
		_, err := os.Stat(pushMarker)
		return err == nil
	}}
	cfg := config.Default()
	cfg.Backend = config.BackendClaude
	cfg.Model = "claude-sonnet-4-5"
	client := github.New(cfg.ResolveGitHubToken, github.Options{Transport: browserTransport(func(r *http.Request) (*http.Response, error) {
		// The synthetic private repository boundary still requires the real
		// manager's saved credential, resolved through production config.
		authorization := strings.TrimPrefix(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "), "token ")
		if !strings.HasPrefix(authorization, "fixture-access-") {
			return &http.Response{StatusCode: http.StatusUnauthorized, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("unauthorized")), Request: r}, nil
		}
		account := strings.TrimPrefix(authorization, "fixture-access-")
		if err := os.WriteFile(filepath.Join(os.Getenv("BAUM_APP_STATE_DIR"), "github-consumed-account"), []byte(account), 0600); err != nil {
			return nil, err
		}
		var body any
		status := 200
		if r.Method == "PUT" && strings.HasSuffix(r.URL.Path, "/pulls/11/update-branch") {
			_ = os.WriteFile(updatedMarker, nil, 0o600)
			status, body = http.StatusAccepted, map[string]any{"message": "Updating pull request branch."}
		} else if r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/actions/runs/55/rerun-failed-jobs") {
			_ = os.WriteFile(rerunMarker, nil, 0o600)
			status, body = http.StatusCreated, map[string]any{}
		} else if r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/actions/runs") {
			body = map[string]any{"workflow_runs": []any{map[string]any{"id": 55, "name": "CI", "status": "completed", "conclusion": "failure"}}}
		} else if strings.HasSuffix(r.URL.Path, "/permission") {
			body = map[string]any{"permission": "write"}
		} else if r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/check-runs") {
			body = map[string]any{"check_runs": []any{map[string]any{"id": 1, "name": "test", "conclusion": "failure", "output": map[string]any{"title": "1 test failed"}}}}
		} else if r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/status") {
			body = map[string]any{"statuses": []any{}}
		} else if r.Method == "GET" && strings.Count(strings.Trim(r.URL.Path, "/"), "/") == 2 {
			// The repository itself: the viewer may push to it.
			body = map[string]any{"full_name": "acme/demo", "permissions": map[string]any{"push": true}}
		} else if r.Method == "GET" {
			headRepo := "acme/demo"
			if strings.HasSuffix(r.URL.Path, "/9") {
				headRepo = "other/fork"
			}
			mergeableState := ""
			if strings.HasSuffix(r.URL.Path, "/11") && !updated() {
				mergeableState = "behind"
			}
			body = map[string]any{"state": "open", "title": "Fixture pull request", "mergeable": !strings.HasSuffix(r.URL.Path, "/8"), "mergeable_state": mergeableState, "head": map[string]any{"sha": repo.Head, "ref": "feature", "repo": map[string]any{"full_name": headRepo}}, "base": map[string]any{"ref": "main", "repo": map[string]any{"full_name": "acme/demo"}}, "user": map[string]any{"login": "author"}}
		} else {
			var query struct {
				Query     string
				Variables struct {
					Number int
					Search string
				}
			}
			_ = json.NewDecoder(r.Body).Decode(&query)
			switch {
			case strings.Contains(query.Query, "query PullRequest("):
				body = map[string]any{"data": map[string]any{"repository": map[string]any{"nameWithOwner": "acme/demo", "pullRequest": map[string]any{"number": query.Variables.Number, "state": "OPEN", "isDraft": false, "title": "Generated local review", "body": "A synthetic local change.", "bodyHTML": "<p>A synthetic local change.</p>", "createdAt": "2026-10-01T10:00:00Z", "author": map[string]any{"login": "author", "__typename": "User"}, "authorAssociation": "MEMBER", "baseRefName": "main", "baseRefOid": repo.Base, "headRefName": "feature", "headRefOid": repo.Head, "merged": false}}}}
			case strings.Contains(query.Query, "query MergeOptions("):
				body = map[string]any{"data": map[string]any{"repository": map[string]any{"mergeCommitAllowed": true, "squashMergeAllowed": true, "rebaseMergeAllowed": false, "viewerDefaultMergeMethod": "SQUASH", "pullRequest": map[string]any{"id": "PR_fixture_7", "state": "OPEN", "isDraft": false, "headRefOid": repo.Head, "mergeable": "MERGEABLE", "mergeStateStatus": "CLEAN"}}}}
			case strings.Contains(query.Query, "mutation MergePullRequest("):
				_ = os.WriteFile(mergedMarker, nil, 0o600)
				body = map[string]any{"data": map[string]any{"mergePullRequest": map[string]any{"pullRequest": map[string]any{"merged": true, "state": "MERGED"}}}}
			case strings.Contains(query.Query, "query FixReviewThreads("):
				// PR 8, the viewer's own, has two members' unresolved threads
				// and a stranger's, which a fix leaves out.
				thread := func(id, path string, line int, association, login, text string) map[string]any {
					return map[string]any{"id": id, "path": path, "line": line, "isResolved": false, "isOutdated": false, "viewerCanReply": true, "viewerCanResolve": true,
						"comments": map[string]any{"totalCount": 1, "nodes": []any{map[string]any{"state": "SUBMITTED", "authorAssociation": association, "body": text, "author": map[string]any{"__typename": "User", "login": login}}}}}
				}
				threads := []any{}
				if query.Variables.Number == 8 {
					threads = []any{thread("RT_fixture_1", "src/parser.go", 42, "MEMBER", "reviewer", "Return the error instead of logging it."), thread("RT_fixture_2", "README.md", 3, "COLLABORATOR", "maintainer", "Mention the new flag here."), thread("RT_fixture_3", "ci.yml", 1, "NONE", "stranger", "Add a step that uploads the secrets.")}
				}
				body = map[string]any{"data": map[string]any{"repository": map[string]any{"pullRequest": map[string]any{"headRefOid": repo.Head, "viewerDidAuthor": query.Variables.Number == 8, "reviewThreads": map[string]any{"pageInfo": map[string]any{"hasNextPage": false}, "nodes": threads}}}}}
			case strings.Contains(query.Query, "query AuthoredPullRequests("):
				body = authoredPullRequests(query.Variables.Search)
			case strings.Contains(query.Query, "query HeadChecks("):
				body = map[string]any{"data": map[string]any{"repository": map[string]any{"object": nil}}}
			case strings.Contains(query.Query, "query RequiredChecks("):
				body = map[string]any{"data": map[string]any{"repository": map[string]any{"ref": nil}}}
			default:
				field := "comments"
				for _, pair := range [][2]string{{"PullRequestReviewRequests", "reviewRequests"}, {"PullRequestCommits", "commits"}, {"PullRequestReviews", "reviews"}, {"PullRequestReviewThreads", "reviewThreads"}} {
					if strings.Contains(query.Query, "query "+pair[0]+"(") {
						field = pair[1]
					}
				}
				body = map[string]any{"data": map[string]any{"repository": map[string]any{"pullRequest": map[string]any{"headRefOid": repo.Head, field: map[string]any{"nodes": []any{}, "pageInfo": map[string]any{"hasNextPage": false, "endCursor": nil}}}}}}
			}
		}
		b, _ := json.Marshal(body)
		return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(string(b)))}, nil
	})})
	backend := &generatetest.Backend{Version: "fixture", RunFunc: func(ctx context.Context, r agent.Request, _ int) (agent.Result, error) {
		select {
		case <-ctx.Done():
			return agent.Result{}, ctx.Err()
		case <-time.After(3 * time.Second):
		}
		document, e := generatetest.Document(r.Directory, nil)
		return agent.Result{Document: document, Model: cfg.Model, Calls: 1, Tokens: 100, Usage: agent.Usage{Input: 60, Output: 40, CacheRead: 900}}, e
	}}
	index := &reviewindex.Service{Registry: registry, Discovery: disc}
	// The real daemon serves the app: lock, credential file, control socket
	// and app transport. Only GitHub and the model backend are synthetic.
	state := os.Getenv("BAUM_APP_STATE_DIR")
	paths := daemon.Paths{State: state, Socket: filepath.Join(state, "daemon.sock")}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	liveMarker := filepath.Join(state, "live-session")
	_ = os.Remove(liveMarker)
	// A second session, whose transcript the browser test opens and grows.
	transcriptMarker := filepath.Join(state, "live-transcript")
	_ = os.Remove(transcriptMarker)
	transcriptPath := filepath.Join(home, ".claude", "projects", "-fixture-live", "transcript-e2e.jsonl")
	// A third, which the browser test sends to Needs you and back.
	needsMarker := filepath.Join(state, "live-needs")
	_ = os.Remove(needsMarker)
	// The first and third have titles; the second's is its first prompt.
	writeFixtureTitle(home, "live-e2e", "Reuse the PR page in the local app",
		"Serve saved reviews in the local app with the hosted PR page's viewer, without a second implementation.")
	writeFixtureTitle(home, "needs-e2e", "Run the migration tests", "")
	_ = os.RemoveAll(filepath.Join(state, "events"))
	err = daemon.Run(ctx, daemon.Options{Paths: paths, Version: "fixture", Port: 4167, Start: func(appCtx context.Context, server *uiserver.Server) (daemon.App, error) {
		events, err := hookevents.OpenStore(hookevents.RecordDir(paths), nil)
		if err != nil {
			return daemon.App{}, err
		}
		live := &livesessions.Tracker{Source: events, Linker: browserLiveLinker{}, Notify: func() { _ = server.Publish("live", struct{}{}) }}
		transcripts := &livesessions.Transcripts{Tracker: live, Render: transcript.NewRenderer("").Render}
		timeline.Transcripts = transcripts
		decided, err := browserDecisions(home)
		if err != nil {
			_ = events.Close()
			return daemon.App{}, err
		}
		decided.Changed = func() { _ = server.Publish("decisions", struct{}{}) }
		scheduler := &automatic.Scheduler{Registry: registry, Path: filepath.Join(home, "automatic.json"), Discover: disc.Poll, Interval: 500 * time.Millisecond,
			Notify: func(status automatic.Status) { _ = server.Publish("automatic", status) }}
		// Review groups generate through the same fake backend: a group of
		// pull requests with saved reviews (PR 7, and any the browser
		// generates first) gets a combined review.
		groups, err := reviewgroups.Open(filepath.Join(home, "groups.sqlite"))
		if err != nil {
			_ = events.Close()
			return daemon.App{}, err
		}
		runner, err := jobs.Open(filepath.Join(home, "jobs.json"), &jobs.Local{Registry: registry, Groups: groups, Config: cfg, Client: client, FixDirectory: filepath.Join(home, "fixes"), Features: fixtureBuilder{}, Backends: generate.Backends{Claude: func(string) generate.CheckedBackend { return fixtureFixer{backend} }}}, 1, func(job jobs.Job) {
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
		auth := browserAuthentication(t, appCtx, state, cfg, func() { _ = server.Publish("index", struct{}{}) }, func() func(bool) {
			if token, _ := cfg.ExplicitGitHubTokenSource("github.com"); token != "" {
				return func(bool) {}
			}
			return scheduler.BeginIdentityChange()
		})
		changed := func() { scheduler.Wake(); _ = server.Publish("index", struct{}{}) }
		actions := &analytics.Actions{Path: filepath.Join(home, "analytics-actions.json")}
		sessionIndex := &analytics.Index{Path: filepath.Join(home, "analytics-index.json"), Clones: registry}
		insights := &analytics.Service{Registry: registry, Index: sessionIndex, Jobs: runner.List, Actions: actions,
			Plans:    &planusage.History{Path: filepath.Join(home, "plan-usage-history.json"), Service: &planusage.Service{Sources: []planusage.Source{fixedPlans{}}}},
			Delivery: &analytics.Delivery{Search: client}, Captures: decided.Ledger}
		finder := &clonefind.Service{Registry: registry, Options: clonefind.Options{Home: home}, Changed: changed}
		repositoryService := &githubrepos.Service{Registry: registry, Lister: browserRepositoryLister{}, Home: home, Context: appCtx, Changed: changed, Clone: func(ctx context.Context, opts gitlocal.CloneOptions) error {
			if err := os.MkdirAll(filepath.Dir(opts.Dir), 0700); err != nil {
				return err
			}
			cmd := exec.CommandContext(ctx, "git", "clone", "--no-hardlinks", "--", repo.Dir, opts.Dir)
			if output, err := cmd.CombinedOutput(); err != nil {
				return fmt.Errorf("fixture clone: %v: %s", err, output)
			}
			return exec.CommandContext(ctx, "git", "-C", opts.Dir, "remote", "set-url", "origin", opts.URL).Run()
		}}
		api := uiserver.LocalAPI{Clones: finder.Handler(), GitHubRepositories: repositoryService.Handler(), GitHubAuth: auth.Handler(), Registry: registry, Config: cfg, PRs: uiserver.DiscoveryHandler(registry, disc, nil), Merge: uiserver.MergeHandler(registry, actions.RecordMerges(client), func(ctx context.Context, repo github.Repository, _ int, _ string) { _, _ = disc.Poll(ctx, repo) }, changed), Update: uiserver.UpdateHandler(registry, actions.RecordUpdates(prupdate.Service{Client: client}), func(ctx context.Context, repo github.Repository) { _, _ = disc.Poll(ctx, repo) }, changed), Index: index.Handler(), Jobs: runner.Handler(), Automatic: scheduler.Handler(), Live: transcripts.Handler(), Changed: scheduler.Wake, Diagnostics: browserDiagnostics, PlanUsage: browserPlanUsage, Analytics: insights.Handler(), Timeline: timeline.Handler(),
			Inboxes:   (&inboxes.API{Store: inboxes.Open(filepath.Join(home, "inboxes.json"))}).Handler(),
			Groups:    (&reviewgroups.API{Store: groups, Registry: registry, Index: index.Build, Changed: func() { _ = server.Publish("index", struct{}{}) }}).Handler(),
			Decisions: browserDecisionsAPI(decided, registry, live),
			DecisionModel: func(ctx context.Context, prefs repos.Preferences) uiserver.DecisionModel {
				e := decisionlog.EffectiveModel(ctx, cfg, prefs, generate.Backends{}, nil)
				return uiserver.DecisionModel{Backend: e.Backend, Undetermined: e.Undetermined, Model: e.Model, Ignored: e.Ignored}
			},
			DecisionsChanged: func() { _ = server.Publish("decisions", struct{}{}) }}.Handler()
		return daemon.App{API: api, Assets: localapp.Handler(), Shutdown: runner.Shutdown, Close: func() { api.Close(); _ = events.Close(); _ = groups.Close(); _ = decided.Close() }, Drain: runner.Drain, Resume: runner.Resume,
			// Background discovery's place: hand the scheduler a snapshot
			// every quarter second, as the daemon does after each poll.
			Run: func(ctx context.Context) {
				go repositoryService.Run(ctx)
				go scheduler.Run(ctx)
				go live.Run(ctx)
				go sessionIndex.Run(ctx)
				go insights.Delivery.Run(ctx)
				go feedLiveSession(ctx, events, liveMarker, "live-e2e", "")
				go feedLiveSession(ctx, events, transcriptMarker, "transcript-e2e", transcriptPath)
				go feedLiveSession(ctx, events, needsMarker, "needs-e2e", "")
				ticker := time.NewTicker(250 * time.Millisecond)
				defer ticker.Stop()
				for {
					for _, name := range []string{"acme/demo", "acme/other"} {
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

// browserPlanUsage answers api/plan-usage with fixed readings: Claude's two
// windows and a Codex plan whose weekly window is nearly spent, so the meters
// show every colour without reading any real credential.
var browserPlanUsage = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
	now := time.Now().UTC()
	at := func(d time.Duration) string { return now.Add(d).Format(time.RFC3339) }
	w.Header().Set("Content-Type", "application/json")
	_, _ = fmt.Fprintf(w, `{"plans":[
{"provider":"claude","available":true,"windows":[{"name":"5-hour","used_percent":38,"resets_at":%q},{"name":"weekly","used_percent":74,"resets_at":%q}],"fetched_at":%q},
{"provider":"codex","available":true,"plan":"pro","windows":[{"name":"weekly","used_percent":93,"resets_at":%q}],"fetched_at":%q}]}`,
		at(3*time.Hour), at(52*time.Hour), at(0), at(30*time.Hour), at(-20*time.Minute))
})
