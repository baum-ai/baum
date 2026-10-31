package jobs

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/baum-ai/baum/agent"
	"github.com/baum-ai/baum/internal/appdb/appdbtest"
	"github.com/baum-ai/baum/internal/config"
	"github.com/baum-ai/baum/internal/generate"
	"github.com/baum-ai/baum/internal/generate/generatetest"
	"github.com/baum-ai/baum/internal/github"
	"github.com/baum-ai/baum/internal/githubauth"
	"github.com/baum-ai/baum/internal/pricing"
	"github.com/baum-ai/baum/internal/repos"
	"github.com/baum-ai/baum/internal/store"
)

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func registeredClone(t *testing.T) (*repos.Registry, repos.Clone) {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{{"init"}, {"remote", "add", "origin", "https://github.com/owner/repo"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, e := cmd.CombinedOutput(); e != nil {
			t.Fatal(string(out), e)
		}
	}
	registry, e := repos.Open(filepath.Join(appdbtest.TempDir(t), "repos.sqlite"))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = registry.Close() })
	clone, e := registry.Add(context.Background(), dir, nil)
	if e != nil {
		t.Fatal(e)
	}
	return registry, clone
}

func TestExecutionFencesOnlySavedGitHubCredentials(t *testing.T) {
	for _, source := range []string{"saved", "config", "GITHUB_TOKEN", "GH_TOKEN", "enterprise"} {
		t.Run(source, func(t *testing.T) {
			t.Setenv("GITHUB_TOKEN", "")
			t.Setenv("GH_TOKEN", "")
			original := config.GitHubAuth
			t.Cleanup(func() { config.GitHubAuth = original })
			config.GitHubAuth = githubauth.New(&jobAccountStore{})
			registry, clone := registeredClone(t)
			local := localRunner(registry, "owner/repo", "write", 200)
			switch source {
			case "config":
				local.Config.GitHubToken = "explicit-token"
			case "GITHUB_TOKEN", "GH_TOKEN":
				t.Setenv(source, "explicit-token")
			case "enterprise":
				t.Setenv("GH_HOST", "git.example.com")
				t.Setenv("GH_ENTERPRISE_TOKEN", "enterprise-token")
				cmd := exec.Command("git", "remote", "set-url", "origin", "https://git.example.com/owner/repo")
				cmd.Dir = clone.Path
				if out, err := cmd.CombinedOutput(); err != nil {
					t.Fatal(err, string(out))
				}
				if _, err := registry.Add(context.Background(), clone.Path, nil); err != nil {
					t.Fatal(err)
				}
			}
			prepared, _, err := local.Prepare(context.Background(), Item{Clone: clone.ID, PR: 1})
			if err != nil {
				t.Fatal(err)
			}
			var executionCtx context.Context
			local.Client = github.New(func(ctx context.Context, _ string) (string, error) {
				if executionCtx == nil {
					executionCtx = ctx
					if err := config.GitHubAuth.Disconnect(context.Background()); err != nil {
						t.Fatal(err)
					}
					if source == "saved" {
						select {
						case <-ctx.Done():
						case <-time.After(time.Second):
							t.Fatal("saved-account work was not canceled")
						}
					}
				}
				return "fixture-token", nil
			}, github.Options{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
				if err := r.Context().Err(); err != nil {
					return nil, err
				}
				body := `{"state":"open","head":{"sha":"first","repo":{"full_name":"owner/repo"}},"user":{"login":"author"}}`
				if strings.HasSuffix(r.URL.Path, "/permission") {
					body = `{"permission":"write"}`
				}
				return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
			})})
			// The deliberately empty clone cannot generate a review. Inspect the
			// live request context during revalidation, before that later failure.
			_, _ = local.Execute(context.Background(), prepared, func(Progress) {})
			if executionCtx == nil {
				t.Fatal("execution did not revalidate credentials")
			}
			if canceled := executionCtx.Err() != nil; canceled != (source == "saved") {
				t.Fatalf("saved-account disconnect canceled %s work: %v", source, executionCtx.Err())
			}
		})
	}
}

type publicationProbe struct {
	generate.Versions
	called bool
}

func (p *publicationProbe) Put(context.Context, store.Key, store.Version, store.Version) error {
	p.called = true
	return nil
}

func TestPublicationFenceDoesNotAffectExplicitCredentials(t *testing.T) {
	manager := githubauth.New(&jobAccountStore{})
	ctx, cancel := manager.IdentityContext(context.Background())
	defer cancel()
	if err := manager.Disconnect(context.Background()); err != nil {
		t.Fatal(err)
	}
	probe := &publicationProbe{}
	if err := (identityVersions{probe, manager}).Put(ctx, store.Key{}, store.Version{}, store.Version{}); err == nil || probe.called {
		t.Fatal("saved-account work published after disconnect", err)
	}
	if err := (identityVersions{probe, nil}).Put(context.Background(), store.Key{}, store.Version{}, store.Version{}); err != nil || !probe.called {
		t.Fatal("explicit-credential work could not publish after disconnect", err)
	}
}
func localRunner(registry *repos.Registry, headRepository, permission string, status int) *Local {
	cfg := config.Default()
	cfg.Backend = config.BackendClaude
	cfg.Model = "claude-sonnet-4-5"
	client := github.New(func(context.Context, string) (string, error) { return "fixture-token", nil }, github.Options{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		body := `{"state":"open","head":{"sha":"first","repo":{"full_name":"` + headRepository + `"}},"user":{"login":"author"}}`
		code := 200
		if strings.HasSuffix(r.URL.Path, "/permission") {
			code = status
			body = `{"permission":"` + permission + `"}`
		}
		return &http.Response{StatusCode: code, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}, nil
	})})
	return &Local{Registry: registry, Config: cfg, Client: client, Backends: generate.Backends{Claude: func(string) generate.CheckedBackend { return &generatetest.Backend{Version: "9.9.9"} }}}
}
func TestLocalPrepareUsesGitHubEvidenceNotBrowserAuthorization(t *testing.T) {
	ctx := context.Background()
	registry, clone := registeredClone(t)
	for _, test := range []struct {
		name, headRepository, permission string
		status                           int
		restricted                       bool
	}{{"writer", "owner/repo", "write", 200, false}, {"fork writer", "other/repo", "write", 200, true}, {"reader", "owner/repo", "read", 200, true}, {"unknown permission", "owner/repo", "", 403, true}} {
		t.Run(test.name, func(t *testing.T) {
			local := localRunner(registry, test.headRepository, test.permission, test.status)
			p, version, e := local.Prepare(ctx, Item{Clone: clone.ID, PR: 1})
			if e != nil {
				t.Fatal(e)
			}
			if p.RequiresIsolationConfirmation != test.restricted || p.HeadSHA != "first" || p.ConfigDigest == "" || version == "" {
				t.Fatalf("incorrect authorization %+v", p)
			}
			local.Config.Budgets.MaxTokens++
			changed, _, e := local.Prepare(ctx, p.Item)
			if e != nil || changed.ConfigDigest == p.ConfigDigest {
				t.Fatal("budget change not fingerprinted", e)
			}
		})
	}
}

func TestProgressWithholdsProviderStringsUntilRedaction(t *testing.T) {
	var reports []Progress
	b := &trackedBackend{Backend: &generatetest.Backend{RunFunc: func(context.Context, agent.Request, int) (agent.Result, error) {
		return agent.Result{Model: "untrusted-model", Calls: 1, Tokens: 10, Usage: agent.Usage{Input: 6, Output: 4, CacheRead: 90, Speed: new("untrusted-speed")}}, nil
	}}, progress: func(p Progress) { reports = append(reports, p) }}
	if _, err := b.Run(context.Background(), agent.Request{}); err != nil {
		t.Fatal(err)
	}
	got := reports[len(reports)-1]
	if got.Accounting == nil || got.Accounting.Usage.CacheRead != 90 || got.Accounting.Usage.Input != 6 || got.Accounting.Usage.Speed != nil || got.Accounting.Attempts[0].Model != "" || got.Accounting.Attempts[0].Usage.Speed != nil || got.CostReason != "metadata_pending_redaction" {
		t.Fatalf("unsafe or lossy progress: %+v", got)
	}
}

// TestLocalPrepareUsesBackendPathsWithoutFallingBackToPATH gives the runner a
// config naming a claude that is not there while a working claude is on PATH.
func TestLocalPrepareUsesBackendPathsWithoutFallingBackToPATH(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake claude is a shell script")
	}
	ctx := context.Background()
	dir := t.TempDir()
	for _, args := range [][]string{{"init"}, {"remote", "add", "origin", "https://github.com/owner/repo"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, e := cmd.CombinedOutput(); e != nil {
			t.Fatal(string(out), e)
		}
	}
	registry, e := repos.Open(filepath.Join(appdbtest.TempDir(t), "repos.sqlite"))
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = registry.Close() }()
	clone, e := registry.Add(ctx, dir, nil)
	if e != nil {
		t.Fatal(e)
	}
	bin := t.TempDir()
	if e := os.WriteFile(filepath.Join(bin, "claude"), []byte("#!/bin/sh\necho \"2.1.7 (Claude Code)\"\n"), 0o755); e != nil {
		t.Fatal(e)
	}
	gitPath, e := exec.LookPath("git")
	if e != nil {
		t.Fatal(e)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+filepath.Dir(gitPath))
	client := github.New(func(context.Context, string) (string, error) { return "fixture-token", nil }, github.Options{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		body := `{"state":"open","head":{"sha":"first","repo":{"full_name":"owner/repo"}},"user":{"login":"author"}}`
		if strings.HasSuffix(r.URL.Path, "/permission") {
			body = `{"permission":"write"}`
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}, nil
	})})
	cfg := config.Default()
	cfg.Backend = config.BackendClaude
	cfg.Model = "claude-sonnet-4-5"
	local := &Local{Registry: registry, Config: cfg, Client: client}
	if _, _, e := local.Prepare(ctx, Item{Clone: clone.ID, PR: 1}); e != nil {
		t.Fatalf("claude on PATH: %v", e)
	}
	local.Config.BackendPaths = map[config.Backend]string{config.BackendClaude: filepath.Join(t.TempDir(), "missing-claude")}
	_, _, e = local.Prepare(ctx, Item{Clone: clone.ID, PR: 1})
	if e == nil || !strings.Contains(e.Error(), "backend_paths.claude") {
		t.Fatalf("err = %v, want one naming backend_paths.claude", e)
	}
}

// The app's backend and model replace the config file's for every job, read
// when the job is prepared.
func TestPreferencesChooseTheBackendAndModel(t *testing.T) {
	ctx := context.Background()
	registry, clone := registeredClone(t)
	local := localRunner(registry, "owner/repo", "write", 200)
	local.Backends.CursorAgent = func(string) generate.CheckedBackend {
		return &generatetest.Backend{BackendName: agent.BackendCursorAgent, Version: "2026.10.16"}
	}
	backend, model := config.BackendCursorAgent, "app-model"
	if _, e := registry.UpdatePreferences(ctx, func(p repos.Preferences) repos.Preferences {
		p.Backend, p.Model = &backend, &model
		return p
	}); e != nil {
		t.Fatal(e)
	}
	p, _, e := local.Prepare(ctx, Item{Clone: clone.ID, PR: 1})
	if e != nil || p.Backend != agent.BackendCursorAgent || p.Model != "app-model" {
		t.Fatalf("prepared = %+v, %v", p, e)
	}
}

// A cursor-agent job is not blocked, whatever its model is called, and its
// recorded cost stays unknown: Cursor bills it through the user's
// subscription, so no API price describes it.
func TestCursorAgentJobsHaveNoListPrice(t *testing.T) {
	ctx := context.Background()
	registry, clone := registeredClone(t)
	local := localRunner(registry, "owner/repo", "write", 200)
	local.Config.Backend = config.BackendCursorAgent
	local.Backends.CursorAgent = func(string) generate.CheckedBackend {
		return &generatetest.Backend{BackendName: agent.BackendCursorAgent, Version: "2026.10.16"}
	}
	p, _, e := local.Prepare(ctx, Item{Clone: clone.ID, PR: 1})
	if e != nil {
		t.Fatal(e)
	}
	if p.Backend != agent.BackendCursorAgent || p.Model != "claude-sonnet-4-5" || p.BlockedReason != "" {
		t.Fatalf("prepared = %+v", p)
	}

	var reports []Progress
	run := func(context.Context, agent.Request, int) (agent.Result, error) {
		return agent.Result{Model: "Claude 4.5 Sonnet", ModelSource: "cli_reported", Calls: 2, Tokens: 1500, Usage: agent.Usage{Input: 1000, Output: 300, CacheCreate: 200}}, nil
	}
	b := &trackedBackend{Backend: &generatetest.Backend{BackendName: agent.BackendCursorAgent, RunFunc: run}, progress: func(p Progress) { reports = append(reports, p) }, redacted: true}
	if _, err := b.Run(ctx, agent.Request{}); err != nil {
		t.Fatal(err)
	}
	if got := reports[len(reports)-1]; got.CostReason != pricing.ReasonBilledByCursor {
		t.Fatalf("cost reason = %q", got.CostReason)
	}
}

// A job prepared against one price list still runs under another: a newer
// list installed meanwhile, or the compiled one after a restart, is not a
// changed input.
func TestJobRunsWhateverPriceListIsActive(t *testing.T) {
	ctx := context.Background()
	t.Cleanup(func() { pricing.Install(pricing.Embedded()) })
	registry, clone := registeredClone(t)
	local := localRunner(registry, "owner/repo", "write", 200)
	document, e := pricing.ParseDocument(pricing.EmbeddedDocument())
	if e != nil {
		t.Fatal(e)
	}
	document.Version = "2999-01-01"
	newer := pricing.NewList(document)
	pricing.Install(newer)
	prepared, _, e := local.Prepare(ctx, Item{Clone: clone.ID, PR: 1})
	if e != nil || prepared.PriceVersion != "2999-01-01" {
		t.Fatal(prepared, e)
	}
	// After a restart the compiled list is active until the signed list returns.
	pricing.Install(pricing.Embedded())
	if e = local.confirmed(ctx, prepared); e != nil {
		t.Fatalf("a job prepared with a newer list was refused: %v", e)
	}
	stale := prepared
	stale.PriceVersion = pricing.Embedded().Version()
	pricing.Install(newer)
	if e = local.confirmed(ctx, stale); e != nil {
		t.Fatalf("a job prepared with an older list was refused: %v", e)
	}
}

// The execution-time recheck refuses an automatic job whose PR GitHub now
// reports as a draft, before any model call; a confirmed job runs a draft.
func TestLocalRecheckRefusesAnAutomaticJobOfADraft(t *testing.T) {
	ctx := context.Background()
	registry, clone := registeredClone(t)
	draft := false
	cfg := config.Default()
	cfg.Backend = config.BackendClaude
	cfg.Model = "claude-sonnet-4-5"
	client := github.New(func(context.Context, string) (string, error) { return "fixture-token", nil }, github.Options{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		body := `{"state":"open","draft":` + strconv.FormatBool(draft) + `,"head":{"sha":"first","repo":{"full_name":"owner/repo"}},"user":{"login":"author"}}`
		if strings.HasSuffix(r.URL.Path, "/permission") {
			body = `{"permission":"write"}`
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}, nil
	})})
	calls := 0
	local := &Local{Registry: registry, Config: cfg, Client: client, Backends: generate.Backends{Claude: func(string) generate.CheckedBackend {
		return &generatetest.Backend{Version: "9.9.9", RunFunc: func(context.Context, agent.Request, int) (agent.Result, error) {
			calls++
			return agent.Result{}, errors.New("no model calls in this test")
		}}
	}}}
	p, _, e := local.Prepare(ctx, Item{Clone: clone.ID, PR: 1})
	if e != nil || p.Draft {
		t.Fatal(p, e)
	}
	automatic := p
	automatic.RefuseDraft = true
	if e = local.confirmed(ctx, automatic); e != nil {
		t.Fatalf("ready PR refused: %v", e)
	}
	// Converted back to draft while the job waited.
	draft = true
	var refused *NotAutomaticError
	if _, e = local.Execute(ctx, automatic, func(Progress) {}); !errors.As(e, &refused) || refused.Reason != ReasonDraft {
		t.Fatalf("automatic job of a draft: %v", e)
	}
	if calls != 0 {
		t.Fatalf("model called %d times for a draft", calls)
	}
	if e = local.confirmed(ctx, p); e != nil {
		t.Fatalf("confirmed job of a draft refused: %v", e)
	}
}

// The queued confirmation is the durable origin fence, including the interval
// between worker admission and Execute capturing its cancellation context.
func TestQueuedJobCannotExecuteWithAnotherAccount(t *testing.T) {
	registry, clone := registeredClone(t)
	local := localRunner(registry, "owner/repo", "write", 200)
	source := "original-account"
	base := local.Client
	local.Client = github.New(func(context.Context, string) (string, error) { return source, nil }, github.Options{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		reader, err := base.REST(r.Context(), github.Repository{Host: "github.com", Owner: "owner", Name: "repo"})
		if err != nil {
			return nil, err
		}
		status, headers, body, err := reader.Read(r.Context(), r.URL.RequestURI(), "")
		if err != nil {
			return nil, err
		}
		return &http.Response{StatusCode: status, Header: headers, Body: io.NopCloser(strings.NewReader(string(body))), Request: r}, nil
	})})
	prepared, _, err := local.Prepare(context.Background(), Item{Clone: clone.ID, PR: 1})
	if err != nil {
		t.Fatal(err)
	}
	source = "replacement-account"
	changed, _, err := local.Prepare(context.Background(), prepared.Item)
	if err != nil {
		t.Fatal(err)
	}
	if prepared.ConfigDigest == changed.ConfigDigest {
		t.Fatal("credential identity absent from durable confirmation")
	}
	if _, err := local.Execute(context.Background(), prepared, func(Progress) {}); err == nil || !strings.Contains(err.Error(), "generation inputs changed") {
		t.Fatalf("old confirmation executed: %v", err)
	}
}

func TestWorkerRejectsIdentitySwitchAfterAdmissionBeforeExecute(t *testing.T) {
	registry, clone := registeredClone(t)
	local := localRunner(registry, "owner/repo", "write", 200)
	source := atomic.Value{}
	source.Store("original-account")
	local.Client = github.New(func(context.Context, string) (string, error) { return source.Load().(string), nil }, github.Options{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		body := `{"state":"open","head":{"sha":"first","repo":{"full_name":"owner/repo"}},"user":{"login":"author"}}`
		if strings.HasSuffix(r.URL.Path, "/permission") {
			body = `{"permission":"write"}`
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})})
	state := t.TempDir()
	if err := os.Chmod(state, 0700); err != nil {
		t.Fatal(err)
	}
	q, err := Open(filepath.Join(state, "jobs.json"), local, 1, func(job Job) {
		if job.State == "running" {
			source.Store("replacement-account")
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()
	estimated, err := q.Estimate(context.Background(), []Item{{Clone: clone.ID, PR: 1}}, "single")
	if err != nil {
		t.Fatal(err)
	}
	submitted, err := q.SubmitConfirmed(context.Background(), estimated.ID, Confirmation{Estimate: true, Isolation: true})
	if err != nil {
		t.Fatal(err)
	}
	failed := waitState(t, q, submitted[0].ID, "failed")
	if failed.Calls != 0 {
		t.Fatal("model ran under replacement account", failed.Calls)
	}
}

type jobAccountStore struct {
	mu         sync.Mutex
	credential githubauth.Credential
}

func (s *jobAccountStore) Load(context.Context) (githubauth.Credential, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.credential, nil
}
func (s *jobAccountStore) Save(_ context.Context, c githubauth.Credential) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.credential = c
	return nil
}
func (s *jobAccountStore) Delete(context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.credential = githubauth.Credential{}
	return nil
}

func TestBaumTokenRotationPreservesQueuedConfirmationButAccountSwitchDoesNot(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GH_TOKEN", "")
	original := config.GitHubAuth
	t.Cleanup(func() { config.GitHubAuth = original })
	store := &jobAccountStore{credential: githubauth.Credential{AccessToken: "first", Login: "alice", ID: 1}}
	config.GitHubAuth = githubauth.New(store)
	registry, clone := registeredClone(t)
	local := localRunner(registry, "owner/repo", "write", 200)
	local.Client = github.NewFromConfig(config.Config{}, github.Options{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		body := `{"state":"open","head":{"sha":"first","repo":{"full_name":"owner/repo"}},"user":{"login":"author"}}`
		if strings.HasSuffix(r.URL.Path, "/permission") {
			body = `{"permission":"write"}`
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})})
	prepared, _, err := local.Prepare(context.Background(), Item{Clone: clone.ID, PR: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(context.Background(), githubauth.Credential{AccessToken: "rotated", Login: "alice", ID: 1}); err != nil {
		t.Fatal(err)
	}
	current, _, err := local.Prepare(context.Background(), prepared.Item)
	if err != nil {
		t.Fatal(err)
	}
	if current.ConfigDigest != prepared.ConfigDigest {
		t.Fatal("same-account refresh invalidated queued confirmation")
	}
	if err := local.confirmed(context.Background(), prepared); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(context.Background(), githubauth.Credential{AccessToken: "replacement", Login: "bob", ID: 2}); err != nil {
		t.Fatal(err)
	}
	if _, err := local.Execute(context.Background(), prepared, func(Progress) {}); err == nil || !strings.Contains(err.Error(), "generation inputs changed") {
		t.Fatalf("replacement account executed old confirmation: %v", err)
	}
}

// An unknown or removed clone needs a clone, for preparing and executing.
func TestLocalWithoutAnAvailableCloneNeedsOne(t *testing.T) {
	ctx := context.Background()
	registry, clone := registeredClone(t)
	local := localRunner(registry, "owner/repo", "write", 200)
	if _, _, e := local.Prepare(ctx, Item{Clone: "unknown", PR: 1}); !errors.Is(e, ErrNeedsClone) {
		t.Fatalf("unknown clone: %v", e)
	}
	p, _, e := local.Prepare(ctx, Item{Clone: clone.ID, PR: 1})
	if e != nil {
		t.Fatal(e)
	}
	if e = registry.Remove(ctx, clone.ID); e != nil {
		t.Fatal(e)
	}
	if _, _, e = local.Prepare(ctx, p.Item); !errors.Is(e, ErrNeedsClone) {
		t.Fatalf("removed clone, prepare: %v", e)
	}
	if _, e = local.Execute(ctx, p, func(Progress) {}); !errors.Is(e, ErrNeedsClone) {
		t.Fatalf("removed clone, execute: %v", e)
	}
}
