package github

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/lastpersonlabs/baum/internal/config"
)

type stubClone struct {
	// remotes maps remote names to their fetch URLs.
	remotes                       map[string]string
	branch                        string
	upstreamRemote, upstreamMerge string
}

func (c stubClone) RemoteFetchURL(_ context.Context, name string) (string, error) {
	return c.remotes[name], nil
}
func (c stubClone) CurrentBranch(context.Context) (string, error) { return c.branch, nil }
func (c stubClone) BranchUpstream(_ context.Context, branch string) (string, string, error) {
	if branch != c.branch {
		return "", "", nil
	}
	return c.upstreamRemote, c.upstreamMerge, nil
}

func origin(url string) map[string]string { return map[string]string{"origin": url} }

func TestRepositoryFromRemote(t *testing.T) {
	for remote, want := range map[string]Repository{
		"https://github.com/acme/widgets.git":                       acme,
		"https://user:token@github.com/Acme/Widgets":                {Owner: "acme", Name: "widgets"},
		"git@github.com:acme/widgets.git":                           acme,
		"ssh://git@github.com/acme/widgets.git":                     acme,
		"ssh://git@ssh.github.com:443/acme/widgets.git":             acme,
		"github-work:acme/widgets.git":                              acme,
		"git@github-personal:acme/widgets":                          acme,
		"  https://github.com/acme/widgets.git/\n":                  acme,
		"https://github.com/acme/widgets.github.io.git":             {Owner: "acme", Name: "widgets.github.io"},
		"git@github.com:acme-labs/widgets_2.0.git":                  {Owner: "acme-labs", Name: "widgets_2.0"},
		"https://www.github.com/acme/widgets":                       acme,
		"ssh://git@github.com:22/acme/widgets":                      acme,
		"https://github.com/acme/widgets/":                          acme,
		"git://github.com/acme/widgets.git":                         acme,
		"http://github.com/acme/widgets":                            acme,
		"https://x-access-token:secret@github.com/acme/widgets.git": acme,
	} {
		got, err := RepositoryFromRemote(remote)
		if err != nil || got != want {
			t.Errorf("RepositoryFromRemote(%q) = %v, %v; want %v", remote, got, err, want)
		}
	}
	for _, remote := range []string{
		"", "https://gitlab.com/acme/widgets.git", "git@bitbucket.org:acme/widgets.git", "https://github.example.com/acme/widgets",
		"/srv/git/acme/widgets.git", "file:///srv/acme/widgets.git", "https://github.com/acme", "https://github.com/acme/widgets/pull/7",
	} {
		if got, err := RepositoryFromRemote(remote); !errors.Is(err, ErrNotGitHubRemote) {
			t.Errorf("RepositoryFromRemote(%q) = %v, %v; want ErrNotGitHubRemote", remote, got, err)
		}
	}
}

func TestParseRepository(t *testing.T) {
	if got, err := ParseRepository(" Acme/widgets.js "); err != nil || got != (Repository{Owner: "Acme", Name: "widgets.js"}) {
		t.Errorf("ParseRepository = %v, %v", got, err)
	}
	for _, value := range []string{"", "acme", "acme/", "/widgets", "acme/widgets/extra", "ac me/widgets", "acme/..", "-acme/widgets"} {
		if _, err := ParseRepository(value); err == nil {
			t.Errorf("ParseRepository(%q) accepted", value)
		}
	}
}

func TestResolvePRFromFlagsMakesNoRequest(t *testing.T) {
	c := newFakeGitHub(t, map[string]route{}).client(nil)
	ctx := context.Background()
	clone := stubClone{remotes: origin("git@github.com:acme/widgets.git"), branch: "feature"}
	if got, err := c.ResolvePR(ctx, clone, ResolveOptions{PR: 7}); err != nil || got != (Target{Repository: acme, Number: 7}) {
		t.Errorf("--pr on origin: %v, %v", got, err)
	}
	if got, err := c.ResolvePR(ctx, stubClone{}, ResolveOptions{Repo: "other/repo", PR: 3}); err != nil || got != (Target{Repository: Repository{Owner: "other", Name: "repo"}, Number: 3}) {
		t.Errorf("--repo and --pr: %v, %v", got, err)
	}
	for name, test := range map[string]struct {
		clone stubClone
		opts  ResolveOptions
		want  error
	}{
		"no origin":         {clone: stubClone{}, opts: ResolveOptions{PR: 7}, want: ErrNoRemote},
		"non-GitHub origin": {clone: stubClone{remotes: origin("https://gitlab.com/acme/widgets.git")}, opts: ResolveOptions{PR: 7}, want: ErrNotGitHubRemote},
		"detached HEAD":     {clone: stubClone{remotes: origin("git@github.com:acme/widgets.git")}, want: ErrDetachedHead},
	} {
		if _, err := c.ResolvePR(ctx, test.clone, test.opts); !errors.Is(err, test.want) {
			t.Errorf("%s: err = %v, want %v", name, err, test.want)
		}
	}
	if _, err := c.ResolvePR(ctx, clone, ResolveOptions{Repo: "not a repo", PR: 7}); err == nil {
		t.Error("an invalid --repo was accepted")
	}
}

func associated(number int, state, head, base string) map[string]any {
	return map[string]any{"number": number, "state": state, "headRefName": head, "baseRepository": map[string]any{"nameWithOwner": base}}
}

func TestResolvePRFromTheCurrentBranch(t *testing.T) {
	for name, test := range map[string]struct {
		ref  any
		want Target
		err  error
	}{
		"the open pull request, into a fork's upstream": {
			ref: map[string]any{"associatedPullRequests": map[string]any{"nodes": []any{
				associated(9, "MERGED", "feature", "acme/widgets"),
				associated(12, "OPEN", "feature", "upstream/widgets"),
				associated(13, "OPEN", "feature-two", "acme/widgets"),
			}}},
			want: Target{Repository: Repository{Owner: "upstream", Name: "widgets"}, Number: 12},
		},
		"the most recent closed one": {
			ref: map[string]any{"associatedPullRequests": map[string]any{"nodes": []any{
				associated(9, "MERGED", "feature", "acme/widgets"),
				associated(4, "CLOSED", "feature", "acme/widgets"),
			}}},
			want: Target{Repository: acme, Number: 9},
		},
		"several open": {
			ref: map[string]any{"associatedPullRequests": map[string]any{"nodes": []any{
				associated(12, "OPEN", "feature", "acme/widgets"), associated(14, "OPEN", "feature", "upstream/widgets"),
			}}},
			err: ErrAmbiguousPullRequest,
		},
		"none":              {ref: map[string]any{"associatedPullRequests": map[string]any{"nodes": []any{}}}, err: ErrNoPullRequest},
		"branch not pushed": {ref: nil, err: ErrNoPullRequest},
	} {
		t.Run(name, func(t *testing.T) {
			fake := newFakeGitHub(t, map[string]route{"BranchPullRequests": func(request gqlRequest, _ int) fakeResponse {
				if request.Variables["ref"] != "refs/heads/feature" || request.Variables["owner"] != "acme" || request.Variables["name"] != "widgets" {
					t.Errorf("variables = %v", request.Variables)
				}
				return data(map[string]any{"repository": map[string]any{"ref": test.ref}})
			}})
			got, err := fake.client(nil).ResolvePR(context.Background(), stubClone{remotes: origin("https://github.com/acme/widgets.git"), branch: "feature"}, ResolveOptions{})
			if test.err != nil {
				if !errors.Is(err, test.err) {
					t.Fatalf("err = %v, want %v", err, test.err)
				}
				return
			}
			if err != nil || got != test.want {
				t.Fatalf("ResolvePR = %v, %v; want %v", got, err, test.want)
			}
		})
	}
}

// branchRoutes answers BranchPullRequests from prs, keyed by
// "owner/name refs/heads/branch"; any other question has no pull request.
func branchRoutes(t *testing.T, prs map[string][]any) map[string]route {
	return map[string]route{"BranchPullRequests": func(request gqlRequest, _ int) fakeResponse {
		key := fmt.Sprintf("%v/%v %v", request.Variables["owner"], request.Variables["name"], request.Variables["ref"])
		nodes, ok := prs[key]
		if !ok {
			nodes = []any{}
		}
		return data(map[string]any{"repository": map[string]any{"ref": map[string]any{"associatedPullRequests": map[string]any{"nodes": nodes}}}})
	}}
}

func TestResolvePRFromTheBranchUpstream(t *testing.T) {
	upstreamWidgets := Repository{Owner: "upstream", Name: "widgets"}
	remotes := map[string]string{
		"origin":   "https://github.com/acme/widgets.git",
		"upstream": "https://github.com/upstream/widgets.git",
		"fork":     "https://github.com/someone/widgets.git",
	}
	prs := map[string][]any{
		"someone/widgets refs/heads/sizes-upstream": {associated(12, "OPEN", "sizes-upstream", "upstream/widgets")},
		"acme/widgets refs/heads/sizes":             {associated(21, "OPEN", "sizes", "acme/widgets")},
		"other/repo refs/heads/sizes":               {associated(31, "OPEN", "sizes", "other/repo")},
	}
	for name, test := range map[string]struct {
		clone    stubClone
		opts     ResolveOptions
		want     Target
		requests int
	}{
		"gh pr checkout of a fork pull request": {
			clone: stubClone{remotes: remotes, branch: "sizes", upstreamRemote: "upstream", upstreamMerge: "refs/pull/42/head"},
			want:  Target{Repository: upstreamWidgets, Number: 42},
		},
		"an upstream given as a URL": {
			clone: stubClone{remotes: remotes, branch: "sizes", upstreamRemote: "git@github.com:upstream/widgets.git", upstreamMerge: "refs/pull/5/head"},
			want:  Target{Repository: upstreamWidgets, Number: 5},
		},
		"an upstream branch with another name": {
			clone:    stubClone{remotes: remotes, branch: "sizes", upstreamRemote: "fork", upstreamMerge: "refs/heads/sizes-upstream"},
			want:     Target{Repository: upstreamWidgets, Number: 12},
			requests: 1,
		},
		"no pull request through the upstream falls back to origin": {
			clone:    stubClone{remotes: remotes, branch: "sizes", upstreamRemote: "fork", upstreamMerge: "refs/heads/unknown"},
			want:     Target{Repository: acme, Number: 21},
			requests: 2,
		},
		"an upstream outside --repo falls back to --repo": {
			clone:    stubClone{remotes: remotes, branch: "sizes", upstreamRemote: "upstream", upstreamMerge: "refs/pull/42/head"},
			opts:     ResolveOptions{Repo: "other/repo"},
			want:     Target{Repository: Repository{Owner: "other", Name: "repo"}, Number: 31},
			requests: 1,
		},
		"a local upstream is ignored": {
			clone:    stubClone{remotes: remotes, branch: "sizes", upstreamRemote: ".", upstreamMerge: "refs/heads/main"},
			want:     Target{Repository: acme, Number: 21},
			requests: 1,
		},
	} {
		t.Run(name, func(t *testing.T) {
			fake := newFakeGitHub(t, branchRoutes(t, prs))
			got, err := fake.client(nil).ResolvePR(context.Background(), test.clone, test.opts)
			if err != nil || got != test.want {
				t.Fatalf("ResolvePR = %v, %v; want %v", got, err, test.want)
			}
			if n := len(fake.recorded()); n != test.requests {
				t.Errorf("made %d requests, want %d", n, test.requests)
			}
		})
	}

	// The same question is not asked twice when the upstream is origin's
	// branch of the same name.
	fake := newFakeGitHub(t, branchRoutes(t, nil))
	clone := stubClone{remotes: remotes, branch: "sizes", upstreamRemote: "origin", upstreamMerge: "refs/heads/sizes"}
	if _, err := fake.client(nil).ResolvePR(context.Background(), clone, ResolveOptions{}); !errors.Is(err, ErrNoPullRequest) || len(fake.recorded()) != 1 {
		t.Errorf("err = %v after %d requests; want ErrNoPullRequest after one", err, len(fake.recorded()))
	}
}

// TestNewFromConfigSendsTheResolvedToken checks the client takes its token
// through config's precedence: GITHUB_TOKEN, GH_TOKEN, the config file, then
// `gh auth token`.
func TestNewFromConfigSendsTheResolvedToken(t *testing.T) {
	fake := newFakeGitHub(t, map[string]route{"PullRequest": func(gqlRequest, int) fakeResponse { return data(pullRequestData(headSHA)) }})
	dir := t.TempDir()
	withFile := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(withFile, []byte("github_token = \"file-token\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	withoutFile := filepath.Join(dir, "absent.toml")
	ghDir := filepath.Join(dir, "bin")
	if err := os.MkdirAll(ghDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ghDir, "gh"), []byte("#!/bin/sh\n[ \"$*\" = \"auth token --hostname github.com\" ] || exit 2\necho gh-cli-token\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		name, githubToken, ghToken, path, want string
		gh                                     bool
	}{
		{name: "GITHUB_TOKEN first", githubToken: "env-token", ghToken: "gh-env-token", path: withFile, want: "env-token"},
		{name: "then GH_TOKEN", ghToken: "gh-env-token", path: withFile, want: "gh-env-token"},
		{name: "then the config file", path: withFile, gh: true, want: "file-token"},
		{name: "then gh auth token", path: withoutFile, gh: true, want: "gh-cli-token"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if test.gh && runtime.GOOS == "windows" {
				t.Skip("the stand-in gh is a shell script")
			}
			t.Setenv(config.EnvGitHubToken, test.githubToken)
			t.Setenv(config.EnvGHToken, test.ghToken)
			if test.gh {
				t.Setenv("PATH", ghDir)
			} else {
				t.Setenv("PATH", t.TempDir())
			}
			cfg, err := config.Load(config.Overrides{Path: test.path})
			if err != nil {
				t.Fatal(err)
			}
			client, err := NewFromConfig(context.Background(), cfg, Options{Endpoint: fake.server.URL})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := client.pullRequest(context.Background(), acme, 7); err != nil {
				t.Fatal(err)
			}
			requests := fake.recorded()
			last := requests[len(requests)-1]
			if last.Authorization != "Bearer "+test.want {
				t.Errorf("Authorization = %q, want the %s", last.Authorization, test.want)
			}
		})
	}

	t.Run("no token anywhere", func(t *testing.T) {
		t.Setenv(config.EnvGitHubToken, "")
		t.Setenv(config.EnvGHToken, "")
		t.Setenv("PATH", t.TempDir())
		cfg, err := config.Load(config.Overrides{Path: withoutFile})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := NewFromConfig(context.Background(), cfg, Options{}); !errors.Is(err, config.ErrNoGitHubToken) {
			t.Fatalf("err = %v, want ErrNoGitHubToken", err)
		}
	})
}
