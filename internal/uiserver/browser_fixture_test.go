package uiserver

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
	"testing"
	"time"

	"github.com/baum-ai/baum/agent"
	"github.com/baum-ai/baum/internal/assemble/assembletest"
	"github.com/baum-ai/baum/internal/config"
	"github.com/baum-ai/baum/internal/discovery"
	"github.com/baum-ai/baum/internal/generate"
	"github.com/baum-ai/baum/internal/generate/generatetest"
	"github.com/baum-ai/baum/internal/github"
	"github.com/baum-ai/baum/internal/jobs"
	"github.com/baum-ai/baum/internal/localapp"
	"github.com/baum-ai/baum/internal/repos"
	"github.com/baum-ai/baum/internal/reviewindex"
	"github.com/baum-ai/baum/internal/store"
)

type browserDiscovery struct{ snapshot discovery.Snapshot }

func (d browserDiscovery) Cached(_ context.Context, repository github.Repository) (discovery.Snapshot, error) {
	if repository.String() != "acme/demo" {
		return discovery.Snapshot{Repository: repository.String(), PullRequests: []discovery.PullRequest{{Repository: repository.String(), Number: 1, Title: "Other repository change", State: "OPEN", TeamMembershipKnown: true}}, FetchedAt: time.Now(), Rate: discovery.RateLimit{Remaining: new(5000)}}, nil
	}
	return d.snapshot, nil
}
func (d browserDiscovery) Poll(ctx context.Context, repository github.Repository) (discovery.Snapshot, error) {
	return d.Cached(ctx, repository)
}

type browserTransport func(*http.Request) (*http.Response, error)

func (f browserTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// TestLocalAppBrowserFixture is an opt-in process used by Playwright. Its HTTP
// boundary, registry, store, queue, job runner and generation publisher are real;
// only GitHub and the model backend are synthetic. No developer stores are read.
func TestLocalAppBrowserFixture(t *testing.T) {
	if os.Getenv("BAUM_APP_BROWSER_FIXTURE") != "1" {
		t.Skip("Playwright fixture process")
	}
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
	prs := []discovery.PullRequest{{Repository: "acme/demo", Number: 7, HeadSHA: repo.Head, Title: "Reuse the existing PR page", State: "OPEN", URL: "https://github.com/acme/demo/pull/7", ReviewRequestedFromMe: true, AuthorHasWrite: &write, TeamMembershipKnown: true}, {Repository: "acme/demo", Number: 8, HeadSHA: repo.Head, Title: "A pull request without a review", State: "OPEN", URL: "https://github.com/acme/demo/pull/8", AuthoredByMe: true, AuthorHasWrite: &write, TeamMembershipKnown: true}, {Repository: "acme/demo", Number: 9, HeadSHA: repo.Head, Title: "A fork requiring confirmation", State: "OPEN", URL: "https://github.com/acme/demo/pull/9", Fork: true, AuthorHasWrite: &write, TeamMembershipKnown: true}}
	disc := browserDiscovery{discovery.Snapshot{Repository: "acme/demo", PullRequests: prs, FetchedAt: time.Now(), Rate: discovery.RateLimit{Remaining: new(5000)}}}
	cfg := config.Default()
	cfg.Backend = config.BackendClaude
	cfg.Model = "claude-sonnet-4-5"
	client := github.New(func(context.Context, string) (string, error) { return "fixture-token", nil }, github.Options{Transport: browserTransport(func(r *http.Request) (*http.Response, error) {
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
	server, err := New(Options{App: localapp.Handler()})
	if err != nil {
		t.Fatal(err)
	}
	runner, err := jobs.Open(filepath.Join(home, "jobs.json"), &jobs.Local{Registry: registry, Config: cfg, Client: client, Backends: generate.Backends{Claude: func(string) generate.CheckedBackend { return backend }}}, 1, func(job jobs.Job) {
		_ = server.Publish("progress", map[string]any{"job": job})
		_ = server.Publish("index", map[string]any{})
	})
	if err != nil {
		t.Fatal(err)
	}
	defer runner.Close()
	index := &reviewindex.Service{Registry: registry, Discovery: disc}
	api := LocalAPI{Registry: registry, Config: cfg, PRs: DiscoveryHandler(registry, disc, nil), Index: index.Handler(), Jobs: runner.Handler()}.Handler()
	defer api.Close()
	server.opts.API = api
	url, err := server.Listen(4167)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(os.Getenv("BAUM_APP_LAUNCH_FILE"), []byte(url), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err = server.Serve(ctx); err != nil {
		t.Fatal(err)
	}
}
