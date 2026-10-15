package jobs

import (
	"context"
	"io"
	"net/http"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/baum-ai/baum/internal/config"
	"github.com/baum-ai/baum/internal/generate"
	"github.com/baum-ai/baum/internal/generate/generatetest"
	"github.com/baum-ai/baum/internal/github"
	"github.com/baum-ai/baum/internal/repos"
)

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestLocalPrepareUsesGitHubEvidenceNotBrowserAuthorization(t *testing.T) {
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
	for _, test := range []struct {
		name, headRepository, permission string
		status                           int
		restricted                       bool
	}{{"writer", "owner/repo", "write", 200, false}, {"fork writer", "other/repo", "write", 200, true}, {"reader", "owner/repo", "read", 200, true}, {"unknown permission", "owner/repo", "", 403, true}} {
		t.Run(test.name, func(t *testing.T) {
			cfg := config.Default()
			cfg.Backend = config.BackendClaude
			cfg.Model = "claude-sonnet-4-5"
			client := github.New(func(context.Context, string) (string, error) { return "fixture-token", nil }, github.Options{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
				body := `{"state":"open","head":{"sha":"first","repo":{"full_name":"` + test.headRepository + `"}},"user":{"login":"author"}}`
				status := 200
				if strings.HasSuffix(r.URL.Path, "/permission") {
					status = test.status
					body = `{"permission":"` + test.permission + `"}`
				}
				return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}, nil
			})})
			local := &Local{Registry: registry, Config: cfg, Client: client, Backends: generate.Backends{Claude: func(string) generate.CheckedBackend { return &generatetest.Backend{Version: "9.9.9"} }}}
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
