package github

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/cli/go-gh/v2/pkg/repository"

	"github.com/baumai/baum/internal/config"
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

// standInSSH puts an ssh on PATH that resolves the aliases github-work and
// github-personal to github.com, as an ~/.ssh/config would, and every other
// host to itself. gh's configuration is isolated, and GH_HOST names
// ghe.example.com, so gh treats that host as GitHub. It returns the file
// each invocation's arguments are appended to.
func standInSSH(t *testing.T) string {
	t.Helper()
	t.Setenv("GH_CONFIG_DIR", t.TempDir())
	t.Setenv("GH_HOST", "ghe.example.com")
	dir := t.TempDir()
	log := filepath.Join(dir, "invocations")
	if runtime.GOOS == "windows" {
		return log
	}
	script := "#!/bin/sh\necho \"$*\" >> '" + log + "'\n" +
		"case \"$2\" in github-work|github-personal) echo \"hostname github.com\";; *) echo \"hostname $2\";; esac\n"
	if err := os.WriteFile(filepath.Join(dir, "ssh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	return log
}

// TestRemoteHostIsNeverAnSSHOption checks a remote whose host would read as
// an option of `ssh -G` is refused before ssh runs.
func TestRemoteHostIsNeverAnSSHOption(t *testing.T) {
	log := standInSSH(t)
	for _, remote := range []string{
		"ssh://-oProxyCommand=id/acme/widgets",
		"-oProxyCommand=id:acme/widgets",
		"ssh://git@-oProxyCommand=id/acme/widgets.git",
		"git@-oProxyCommand=id:acme/widgets.git",
	} {
		if got, err := RepositoryFromRemote(remote); !errors.Is(err, ErrNotGitHubRemote) {
			t.Errorf("RepositoryFromRemote(%q) = %v, %v; want ErrNotGitHubRemote", remote, got, err)
		}
	}
	if invocations, err := os.ReadFile(log); err == nil {
		t.Errorf("ssh ran: %q", invocations)
	}
	// The stand-in does run for an ordinary ssh remote.
	if runtime.GOOS != "windows" {
		if _, err := RepositoryFromRemote("git@github.com:acme/widgets.git"); err != nil {
			t.Fatal(err)
		}
		if invocations, err := os.ReadFile(log); err != nil || !strings.Contains(string(invocations), "-G github.com") {
			t.Errorf("ssh invocations = %q, %v; want one for github.com", invocations, err)
		}
	}
}

// TestUnknownHostsAreNeverSentAToken checks a --repo or repository on a host
// gh does not treat as GitHub is refused before any token is looked up, so
// GH_ENTERPRISE_TOKEN, which gh hands out for any non-github.com host, never
// leaves for it.
func TestUnknownHostsAreNeverSentAToken(t *testing.T) {
	t.Setenv("GH_CONFIG_DIR", t.TempDir())
	t.Setenv("GH_HOST", "")
	t.Setenv("GH_ENTERPRISE_TOKEN", "ENTERPRISE-SECRET")
	t.Setenv("GITHUB_ENTERPRISE_TOKEN", "ENTERPRISE-SECRET")
	for _, value := range []string{
		"evil.example/acme/widgets",
		"https://evil.example/acme/widgets",
		"attacker.github.com.evil.example/acme/widgets",
	} {
		t.Run(value, func(t *testing.T) {
			fake := newFakeGitHub(t, fixtureRoutes(t))
			client := fake.client(nil)
			t.Setenv("GH_HOST", "")
			if _, err := ParseRepository(value); !errors.Is(err, ErrUnknownHost) {
				t.Errorf("ParseRepository = %v, want ErrUnknownHost", err)
			}
			if _, err := client.ResolvePR(context.Background(), stubClone{}, ResolveOptions{Repo: value, PR: 7}); !errors.Is(err, ErrUnknownHost) {
				t.Errorf("ResolvePR = %v, want ErrUnknownHost", err)
			}
			repo, err := repository.ParseWithHost(value, config.GitHubHost)
			if err != nil {
				t.Fatal(err)
			}
			direct := Repository{Host: repo.Host, Owner: repo.Owner, Name: repo.Name}
			if _, err := client.Fetch(context.Background(), direct, 7, FetchOptions{}); !errors.Is(err, ErrUnknownHost) {
				t.Errorf("Fetch = %v, want ErrUnknownHost", err)
			}
			cfg, err := config.Load(config.Overrides{Path: filepath.Join(t.TempDir(), "absent.toml")})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := NewFromConfig(cfg, Options{Transport: fake.transport()}).Fetch(context.Background(), direct, 7, FetchOptions{}); !errors.Is(err, ErrUnknownHost) {
				t.Errorf("Fetch with the configured tokens = %v, want ErrUnknownHost", err)
			}
			if requests := fake.recorded(); len(requests) != 0 || len(fake.tokenHosts) != 0 {
				t.Errorf("sent %d requests and looked up tokens for %v", len(requests), fake.tokenHosts)
			}
		})
	}
}

func TestRepositoryFromRemote(t *testing.T) {
	standInSSH(t)
	ghe := Repository{Host: "ghe.example.com", Owner: "acme", Name: "widgets"}
	cases := map[string]Repository{
		"https://github.com/acme/widgets.git":                       acme,
		"https://user:token@github.com/Acme/Widgets":                {Host: "github.com", Owner: "Acme", Name: "Widgets"},
		"git@github.com:acme/widgets.git":                           acme,
		"ssh://git@github.com/acme/widgets.git":                     acme,
		"ssh://git@ssh.github.com:443/acme/widgets.git":             acme,
		"  https://github.com/acme/widgets.git/\n":                  acme,
		"https://github.com/acme/widgets.github.io.git":             {Host: "github.com", Owner: "acme", Name: "widgets.github.io"},
		"git@github.com:acme-labs/widgets_2.0.git":                  {Host: "github.com", Owner: "acme-labs", Name: "widgets_2.0"},
		"https://www.github.com/acme/widgets":                       acme,
		"ssh://git@github.com:22/acme/widgets":                      acme,
		"https://github.com/acme/widgets/":                          acme,
		"git://github.com/acme/widgets.git":                         acme,
		"http://github.com/acme/widgets":                            acme,
		"https://x-access-token:secret@github.com/acme/widgets.git": acme,
		"https://GHE.example.com/acme/widgets.git":                  ghe,
		"git@ghe.example.com:acme/widgets.git":                      ghe,
		"https://octo.ghe.com/acme/widgets":                         {Host: "octo.ghe.com", Owner: "acme", Name: "widgets"},
	}
	if runtime.GOOS != "windows" {
		cases["github-work:acme/widgets.git"] = acme
		cases["git@github-personal:acme/widgets"] = acme
	}
	for remote, want := range cases {
		got, err := RepositoryFromRemote(remote)
		if err != nil || got != want {
			t.Errorf("RepositoryFromRemote(%q) = %v, %v; want %v", remote, got, err, want)
		}
	}
	for _, remote := range []string{
		"", "https://gitlab.com/acme/widgets.git", "git@bitbucket.org:acme/widgets.git", "https://github.example.com/acme/widgets",
		"/srv/git/acme/widgets.git", "file:///srv/acme/widgets.git", "https://github.com/acme", "https://github.com/acme/widgets/pull/7",
		"acme/widgets", "https://x:secret@gitlab.com/acme/widgets.git", "https://x:secret@github.com/%zz/widgets",
	} {
		got, err := RepositoryFromRemote(remote)
		if !errors.Is(err, ErrNotGitHubRemote) {
			t.Errorf("RepositoryFromRemote(%q) = %v, %v; want ErrNotGitHubRemote", remote, got, err)
		}
		if err != nil && strings.Contains(err.Error(), "secret") {
			t.Errorf("RepositoryFromRemote(%q) put the credentials in its error: %v", remote, err)
		}
	}
}

func TestParseRepository(t *testing.T) {
	t.Setenv("GH_HOST", "ghe.example.com")
	for value, want := range map[string]Repository{
		" Acme/widgets.js ":               {Host: "github.com", Owner: "Acme", Name: "widgets.js"},
		"ghe.example.com/acme/widgets":    {Host: "ghe.example.com", Owner: "acme", Name: "widgets"},
		"https://github.com/acme/widgets": acme,
	} {
		if got, err := ParseRepository(value); err != nil || got != want {
			t.Errorf("ParseRepository(%q) = %v, %v; want %v", value, got, err, want)
		}
	}
	for _, value := range []string{
		"", "acme", "acme/", "/widgets", "acme/widgets/extra", "ac me/widgets", "acme/..", "-acme/widgets", "a.b/c/d/e",
		"https://x:secret@github.com/acme",
	} {
		_, err := ParseRepository(value)
		if !errors.Is(err, ErrInvalidRepository) {
			t.Errorf("ParseRepository(%q) = %v, want ErrInvalidRepository", value, err)
		}
		if err != nil && strings.Contains(err.Error(), "secret") {
			t.Errorf("ParseRepository(%q) put the credentials in its error: %v", value, err)
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
	if got, err := c.ResolvePR(ctx, stubClone{}, ResolveOptions{Repo: "other/repo", PR: 3}); err != nil || got != (Target{Repository: Repository{Host: "github.com", Owner: "other", Name: "repo"}, Number: 3}) {
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
	if _, err := c.ResolvePR(ctx, clone, ResolveOptions{Repo: "not a repo", PR: 7}); !errors.Is(err, ErrInvalidRepository) {
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
			want: Target{Repository: Repository{Host: "github.com", Owner: "upstream", Name: "widgets"}, Number: 12},
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
	upstreamWidgets := Repository{Host: "github.com", Owner: "upstream", Name: "widgets"}
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
			want:     Target{Repository: Repository{Host: "github.com", Owner: "other", Name: "repo"}, Number: 31},
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
// through config's precedence for github.com (GITHUB_TOKEN, GH_TOKEN, the
// config file, then gh's), sends it in the Authorization header, and
// asks for and sends an Enterprise host only that host's token.
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
	script := "#!/bin/sh\n[ \"$*\" = \"auth token --secure-storage --hostname github.com\" ] || exit 2\necho gh-cli-token\n"
	if err := os.WriteFile(filepath.Join(ghDir, "gh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	ghe := Repository{Host: "ghe.example.com", Owner: "acme", Name: "widgets"}

	for _, test := range []struct {
		name, githubToken, ghToken, enterpriseToken, path, want, url string
		repo                                                         Repository
		gh                                                           bool
	}{
		{name: "GITHUB_TOKEN first", githubToken: "env-token", ghToken: "gh-env-token", path: withFile, want: "env-token"},
		{name: "then GH_TOKEN", ghToken: "gh-env-token", path: withFile, want: "gh-env-token"},
		{name: "then the config file", path: withFile, gh: true, want: "file-token"},
		{name: "then gh auth token", path: withoutFile, gh: true, want: "gh-cli-token"},
		{name: "an Enterprise host gets its own token", githubToken: "env-token", enterpriseToken: "ghe-token", path: withFile,
			repo: ghe, want: "ghe-token", url: "https://ghe.example.com/api/graphql"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if test.gh && runtime.GOOS == "windows" {
				t.Skip("the stand-in gh is a shell script")
			}
			t.Setenv(config.EnvGitHubToken, test.githubToken)
			t.Setenv(config.EnvGHToken, test.ghToken)
			t.Setenv("GH_ENTERPRISE_TOKEN", test.enterpriseToken)
			t.Setenv("GITHUB_ENTERPRISE_TOKEN", "")
			t.Setenv("GH_PATH", "")
			t.Setenv("GH_CONFIG_DIR", t.TempDir())
			t.Setenv("GH_HOST", "ghe.example.com")
			if test.gh {
				t.Setenv("PATH", ghDir)
			} else {
				t.Setenv("PATH", t.TempDir())
			}
			cfg, err := config.Load(config.Overrides{Path: test.path})
			if err != nil {
				t.Fatal(err)
			}
			repo, url := acme, "https://api.github.com/graphql"
			if test.repo != (Repository{}) {
				repo, url = test.repo, test.url
			}
			if _, err := NewFromConfig(cfg, Options{Transport: fake.transport()}).pullRequest(context.Background(), repo, 7); err != nil {
				t.Fatal(err)
			}
			requests := fake.recorded()
			last := requests[len(requests)-1]
			if last.Authorization != "token "+test.want || last.URL != url {
				t.Errorf("request to %s with Authorization %q, want %s with the %s", last.URL, last.Authorization, url, test.want)
			}
		})
	}

	t.Run("no token anywhere", func(t *testing.T) {
		for _, name := range []string{config.EnvGitHubToken, config.EnvGHToken, "GH_ENTERPRISE_TOKEN", "GITHUB_ENTERPRISE_TOKEN", "GH_PATH"} {
			t.Setenv(name, "")
		}
		t.Setenv("GH_CONFIG_DIR", t.TempDir())
		t.Setenv("PATH", t.TempDir())
		cfg, err := config.Load(config.Overrides{Path: withoutFile})
		if err != nil {
			t.Fatal(err)
		}
		before := len(fake.recorded())
		if _, err := NewFromConfig(cfg, Options{Transport: fake.transport()}).pullRequest(context.Background(), acme, 7); !errors.Is(err, config.ErrNoGitHubToken) {
			t.Fatalf("err = %v, want ErrNoGitHubToken", err)
		}
		if len(fake.recorded()) != before {
			t.Error("a request was sent without a token")
		}
	})
}

// TestResolvePRReadsEveryPageOfAssociatedPullRequests checks the choice is
// made over the whole list, not its first page.
func TestResolvePRReadsEveryPageOfAssociatedPullRequests(t *testing.T) {
	pagesOf := func(first, second []any) route {
		return pages(t, map[string]func() fakeResponse{
			"": func() fakeResponse {
				return data(map[string]any{"repository": map[string]any{"ref": map[string]any{"associatedPullRequests": page(first, "associated-1")}}})
			},
			"associated-1": func() fakeResponse {
				return data(map[string]any{"repository": map[string]any{"ref": map[string]any{"associatedPullRequests": page(second, "")}}})
			},
		})
	}
	clone := stubClone{remotes: origin("https://github.com/acme/widgets.git"), branch: "feature"}

	fake := newFakeGitHub(t, map[string]route{"BranchPullRequests": pagesOf(
		[]any{associated(9, "MERGED", "feature", "acme/widgets")},
		[]any{associated(12, "OPEN", "feature", "acme/widgets")},
	)})
	if got, err := fake.client(nil).ResolvePR(context.Background(), clone, ResolveOptions{}); err != nil || got != (Target{Repository: acme, Number: 12}) {
		t.Errorf("open pull request on the second page: %v, %v", got, err)
	}
	if fake.callCount("BranchPullRequests") != 2 {
		t.Errorf("read %d pages, want 2", fake.callCount("BranchPullRequests"))
	}

	fake = newFakeGitHub(t, map[string]route{"BranchPullRequests": pagesOf(
		[]any{associated(12, "OPEN", "feature", "acme/widgets")},
		[]any{associated(14, "OPEN", "feature", "acme/widgets")},
	)})
	if _, err := fake.client(nil).ResolvePR(context.Background(), clone, ResolveOptions{}); !errors.Is(err, ErrAmbiguousPullRequest) {
		t.Errorf("open pull requests on two pages: err = %v, want ErrAmbiguousPullRequest", err)
	}
}

func TestResolvePROnAnEnterpriseHost(t *testing.T) {
	t.Setenv("GH_HOST", "ghe.example.com")
	fake := newFakeGitHub(t, branchRoutes(t, map[string][]any{
		"acme/widgets refs/heads/feature": {associated(5, "OPEN", "feature", "acme/widgets")},
	}))
	client := fake.client(nil)
	clone := stubClone{remotes: origin("https://ghe.example.com/acme/widgets.git"), branch: "feature"}
	got, err := client.ResolvePR(context.Background(), clone, ResolveOptions{})
	ghe := Repository{Host: "ghe.example.com", Owner: "acme", Name: "widgets"}
	if err != nil || got != (Target{Repository: ghe, Number: 5}) {
		t.Fatalf("ResolvePR = %v, %v", got, err)
	}
	requests := fake.recorded()
	if len(requests) != 1 || requests[0].URL != "https://ghe.example.com/api/graphql" {
		t.Errorf("requests = %+v, want one to the Enterprise host's GraphQL endpoint", requests)
	}
	if hosts := fake.tokenHosts; len(hosts) != 1 || hosts[0] != "ghe.example.com" {
		t.Errorf("tokens asked for %v, want only the Enterprise host", hosts)
	}
}
