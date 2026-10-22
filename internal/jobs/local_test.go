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

	"github.com/baum-ai/baum/agent"
	"github.com/baum-ai/baum/internal/config"
	"github.com/baum-ai/baum/internal/generate"
	"github.com/baum-ai/baum/internal/generate/generatetest"
	"github.com/baum-ai/baum/internal/github"
	"github.com/baum-ai/baum/internal/githubauth"
	"github.com/baum-ai/baum/internal/pricing"
	"github.com/baum-ai/baum/internal/repos"
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
	registry, e := repos.Open(filepath.Join(t.TempDir(), "repos.sqlite"))
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
			p, version, e := local.Prepare(ctx, Item{clone.ID, 1})
			if e != nil {
				t.Fatal(e)
			}
			if p.RequiresIsolationConfirmation != test.restricted || p.HeadSHA != "first" || p.EstimateUSD == nil || p.ConfigDigest == "" || version == "" {
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
	registry, e := repos.Open(filepath.Join(t.TempDir(), "repos.sqlite"))
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
	if _, _, e := local.Prepare(ctx, Item{clone.ID, 1}); e != nil {
		t.Fatalf("claude on PATH: %v", e)
	}
	local.Config.BackendPaths = map[config.Backend]string{config.BackendClaude: filepath.Join(t.TempDir(), "missing-claude")}
	_, _, e = local.Prepare(ctx, Item{clone.ID, 1})
	if e == nil || !strings.Contains(e.Error(), "backend_paths.claude") {
		t.Fatalf("err = %v, want one naming backend_paths.claude", e)
	}
}

// A cursor-agent job has no estimate at API rates, whatever its model is
// called: Cursor bills it through the user's subscription. It is not blocked:
// it says why the estimate is unknown. Its progress and recorded cost stay
// unknown for the same reason.
func TestCursorAgentJobsHaveNoListPriceEstimate(t *testing.T) {
	ctx := context.Background()
	registry, clone := registeredClone(t)
	local := localRunner(registry, "owner/repo", "write", 200)
	local.Config.Backend = config.BackendCursorAgent
	local.Backends.CursorAgent = func(string) generate.CheckedBackend {
		return &generatetest.Backend{BackendName: agent.BackendCursorAgent, Version: "2026.10.16"}
	}
	p, _, e := local.Prepare(ctx, Item{clone.ID, 1})
	if e != nil {
		t.Fatal(e)
	}
	if p.Backend != agent.BackendCursorAgent || p.Model != "claude-sonnet-4-5" || p.EstimateUSD != nil || p.EstimateReason != pricing.ReasonBilledByCursor || p.BlockedReason != "" {
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

func TestRecoveredJobReinstallsItsNewerPriceListInsteadOfFailing(t *testing.T) {
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
	confirmed, _, e := local.Prepare(ctx, Item{clone.ID, 1})
	if e != nil || confirmed.PriceVersion != "2999-01-01" {
		t.Fatal(confirmed, e)
	}

	// After a restart the compiled list is active until the signed list returns.
	pricing.Install(pricing.Embedded())
	if e = local.confirmed(ctx, confirmed); e == nil {
		t.Fatal("job ran against a price list it was not confirmed with")
	}
	refreshes := 0
	local.RefreshPrices = func(context.Context) error {
		refreshes++
		pricing.Install(newer)
		return nil
	}
	if e = local.confirmed(ctx, confirmed); e != nil || refreshes != 1 {
		t.Fatalf("recovered job failed instead of reinstalling its list: %v (refreshes %d)", e, refreshes)
	}
	if e = local.confirmed(ctx, confirmed); e != nil || refreshes != 1 {
		t.Fatal("refreshed although the confirmed list is active", e, refreshes)
	}

	stale := confirmed
	stale.PriceVersion = pricing.Embedded().Version()
	if e = local.confirmed(ctx, stale); e == nil || refreshes != 1 {
		t.Fatal("job confirmed against an older list ran or refreshed", e)
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
	p, _, e := local.Prepare(ctx, Item{clone.ID, 1})
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
	submitted, err := q.SubmitConfirmed(context.Background(), estimated.ID, Confirmation{Estimate: true, Isolation: true, Unpriced: true})
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
