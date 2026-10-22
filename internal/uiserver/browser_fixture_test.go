package uiserver_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/baum-ai/baum/agent"
	"github.com/baum-ai/baum/internal/assemble/assembletest"
	"github.com/baum-ai/baum/internal/automatic"
	"github.com/baum-ai/baum/internal/config"
	"github.com/baum-ai/baum/internal/daemon"
	"github.com/baum-ai/baum/internal/discovery"
	"github.com/baum-ai/baum/internal/doctor"
	"github.com/baum-ai/baum/internal/generate"
	"github.com/baum-ai/baum/internal/generate/generatetest"
	"github.com/baum-ai/baum/internal/github"
	"github.com/baum-ai/baum/internal/hookevents"
	"github.com/baum-ai/baum/internal/jobs"
	"github.com/baum-ai/baum/internal/livesessions"
	"github.com/baum-ai/baum/internal/localapp"
	"github.com/baum-ai/baum/internal/repos"
	"github.com/baum-ai/baum/internal/reviewindex"
	"github.com/baum-ai/baum/internal/store"
	"github.com/baum-ai/baum/internal/uiserver"
)

// browserDiscovery serves fixed pull requests. Once pushed reports true (the
// browser test writes a marker file), PR 10 has a new head: the push the
// automatic generation scheduler reacts to.
type browserDiscovery struct {
	snapshot discovery.Snapshot
	pushed   func() bool
	head     string
}

func (d browserDiscovery) Cached(_ context.Context, repository github.Repository) (discovery.Snapshot, error) {
	if repository.String() != "acme/demo" {
		return discovery.Snapshot{Repository: repository.String(), PullRequests: []discovery.PullRequest{{Repository: repository.String(), Number: 1, Title: "Other repository change", State: "OPEN", TeamMembershipKnown: true}}, FetchedAt: time.Now(), Rate: discovery.RateLimit{Remaining: new(5000)}}, nil
	}
	snapshot := d.snapshot
	snapshot.FetchedAt = time.Now()
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
type browserLiveLinker struct{}

func (browserLiveLinker) Link(_ context.Context, cwd string) (livesessions.Link, bool) {
	return livesessions.Link{Repository: "acme/demo", Branch: "feature", PR: 7}, cwd == "/fixture/live"
}

// feedLiveSession records a hook event for the agent session "live-e2e"
// whenever the browser test writes its marker file: "running", "idle" or
// "ended".
func feedLiveSession(ctx context.Context, events *hookevents.Store, marker string) {
	names := map[string]string{"running": "user-prompt-submit", "idle": "stop", "ended": "session-end"}
	var last string
	for ticker := time.NewTicker(100 * time.Millisecond); ; {
		if data, err := os.ReadFile(marker); err == nil && string(data) != last {
			last = string(data)
			if name, ok := names[last]; ok {
				e := hookevents.New(hookevents.KindAgent, "claude-code", name, "/fixture/live", "fixture")
				e.Session = &hookevents.Session{ID: "live-e2e", Cwd: "/fixture/live"}
				_, _ = events.Add(e, hookevents.ViaSocket)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
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
	prs := []discovery.PullRequest{{Repository: "acme/demo", Number: 7, HeadSHA: repo.Head, Title: "Reuse the existing PR page", State: "OPEN", URL: "https://github.com/acme/demo/pull/7", ReviewRequestedFromMe: true, AuthorHasWrite: &write, TeamMembershipKnown: true}, {Repository: "acme/demo", Number: 8, HeadSHA: repo.Head, Title: "A pull request without a review", State: "OPEN", URL: "https://github.com/acme/demo/pull/8", AuthoredByMe: true, AuthorHasWrite: &write, TeamMembershipKnown: true}, {Repository: "acme/demo", Number: 9, HeadSHA: repo.Head, Title: "A fork requiring confirmation", State: "OPEN", URL: "https://github.com/acme/demo/pull/9", Fork: true, AuthorHasWrite: &write, TeamMembershipKnown: true}, {Repository: "acme/demo", Number: 10, HeadSHA: repo.MergeBase, Title: "An automatically reviewed change", State: "OPEN", URL: "https://github.com/acme/demo/pull/10", AuthoredByMe: true, AuthorHasWrite: &write, TeamMembershipKnown: true}}
	prs[0].Review, prs[0].Checks, prs[0].Merge, prs[0].Unresolved = "approved", "passing", "ready", new(0)
	prs[1].Review, prs[1].Checks, prs[1].Merge, prs[1].Unresolved = "changes_requested", "failing", "conflict", new(3)
	prs[2].Review, prs[2].Checks, prs[2].Merge, prs[2].Unresolved = "commented", "running", "blocked", new(6)
	prs[2].UnresolvedCapped = true
	prs[3].Review, prs[3].Checks, prs[3].Merge = "unreviewed", "none", "draft"

	// The browser test pushes PR 10 by writing this marker; a marker left by
	// an earlier run must not count as already seen.
	pushMarker := filepath.Join(os.Getenv("BAUM_APP_STATE_DIR"), "push-10")
	_ = os.Remove(pushMarker)
	disc := browserDiscovery{snapshot: discovery.Snapshot{Repository: "acme/demo", PullRequests: prs, FetchedAt: time.Now(), Rate: discovery.RateLimit{Remaining: new(5000)}}, head: repo.Head, pushed: func() bool {
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
		if strings.HasSuffix(r.URL.Path, "/permission") {
			body = map[string]any{"permission": "write"}
		} else if r.Method == "GET" {
			headRepo := "acme/demo"
			if strings.HasSuffix(r.URL.Path, "/9") {
				headRepo = "other/fork"
			}
			body = map[string]any{"state": "open", "head": map[string]any{"sha": repo.Head, "repo": map[string]any{"full_name": headRepo}}, "user": map[string]any{"login": "author"}}
		} else {
			var query struct {
				Query     string
				Variables struct{ Number int }
			}
			_ = json.NewDecoder(r.Body).Decode(&query)
			switch {
			case strings.Contains(query.Query, "query PullRequest("):
				body = map[string]any{"data": map[string]any{"repository": map[string]any{"nameWithOwner": "acme/demo", "pullRequest": map[string]any{"number": query.Variables.Number, "state": "OPEN", "isDraft": false, "title": "Generated local review", "body": "A synthetic local change.", "bodyHTML": "<p>A synthetic local change.</p>", "createdAt": "2026-10-01T10:00:00Z", "author": map[string]any{"login": "author", "__typename": "User"}, "authorAssociation": "MEMBER", "baseRefName": "main", "baseRefOid": repo.Base, "headRefName": "feature", "headRefOid": repo.Head, "merged": false}}}}
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
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(string(b)))}, nil
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
	_ = os.RemoveAll(filepath.Join(state, "events"))
	err = daemon.Run(ctx, daemon.Options{Paths: paths, Version: "fixture", Port: 4167, Start: func(appCtx context.Context, server *uiserver.Server) (daemon.App, error) {
		events, err := hookevents.OpenStore(hookevents.RecordDir(paths), nil)
		if err != nil {
			return daemon.App{}, err
		}
		live := &livesessions.Tracker{Source: events, Linker: browserLiveLinker{}, Notify: func() { _ = server.Publish("live", struct{}{}) }}
		scheduler := &automatic.Scheduler{Registry: registry, Path: filepath.Join(home, "automatic.json"), Discover: disc.Poll, Interval: 500 * time.Millisecond,
			Notify: func(status automatic.Status) { _ = server.Publish("automatic", status) }}
		runner, err := jobs.Open(filepath.Join(home, "jobs.json"), &jobs.Local{Registry: registry, Config: cfg, Client: client, Backends: generate.Backends{Claude: func(string) generate.CheckedBackend { return backend }}}, 1, func(job jobs.Job) {
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
		api := uiserver.LocalAPI{GitHubAuth: auth.Handler(), Registry: registry, Config: cfg, PRs: uiserver.DiscoveryHandler(registry, disc, nil), Index: index.Handler(), Jobs: runner.Handler(), Automatic: scheduler.Handler(), Live: live.Handler(), Changed: scheduler.Wake, Diagnostics: browserDiagnostics}.Handler()
		return daemon.App{API: api, Assets: localapp.Handler(), Shutdown: runner.Shutdown, Close: func() { api.Close(); _ = events.Close() }, Drain: runner.Drain, Resume: runner.Resume,
			// Background discovery's place: hand the scheduler a snapshot
			// every quarter second, as the daemon does after each poll.
			Run: func(ctx context.Context) {
				go scheduler.Run(ctx)
				go live.Run(ctx)
				go feedLiveSession(ctx, events, liveMarker)
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
