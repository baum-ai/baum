package gitlocal

import (
	"context"
	"slices"
	"testing"
)

func TestRemoteURLs(t *testing.T) {
	f := newFixture(t)
	r := open(t, f.dir, Options{})
	if got, err := r.RemoteURLs(context.Background()); err != nil || len(got) != 0 {
		t.Fatalf("no remotes: %v, %v", got, err)
	}
	git(t, f.dir, "remote", "add", "origin", "git@github.com:someone/fork.git")
	git(t, f.dir, "remote", "add", "up.stream", "https://github.com/acme/widgets.git")
	got, err := r.RemoteURLs(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []Remote{{"origin", "git@github.com:someone/fork.git"}, {"up.stream", "https://github.com/acme/widgets.git"}}
	if !slices.Equal(got, want) {
		t.Errorf("remotes = %v, want %v", got, want)
	}
}

func TestNormalizeRepository(t *testing.T) {
	for remote, want := range map[string]string{
		"https://github.com/Acme/Widgets.git":            "acme/widgets",
		"https://github.com/acme/widgets":                "acme/widgets",
		"https://github.com/acme/widgets/":               "acme/widgets",
		"https://user:token@github.com/acme/widgets.git": "acme/widgets",
		"http://192.0.2.1:3000/acme/widgets.git":         "acme/widgets",
		"git@github.com:acme/widgets.git":                "acme/widgets",
		"github.com:acme/widgets":                        "acme/widgets",
		"ssh://git@github.com/acme/widgets.git":          "acme/widgets",
		"ssh://git@ssh.github.com:443/acme/widgets.git":  "acme/widgets",
		"git://github.com/acme/widgets.git":              "acme/widgets",
		"  https://github.com/acme/widgets.git\n":        "acme/widgets",
	} {
		if got, ok := NormalizeRepository(remote); !ok || got != want {
			t.Errorf("NormalizeRepository(%q) = %q, %v; want %q", remote, got, ok, want)
		}
	}
	for _, remote := range []string{
		"", "/srv/git/acme/widgets.git", "../widgets", "file:///srv/acme/widgets.git", `C:\src\acme\widgets`,
		"https://github.com/acme", "https://github.com/acme/widgets/tree/main", "ftp://github.com/acme/widgets.git", "widgets",
	} {
		if got, ok := NormalizeRepository(remote); ok {
			t.Errorf("NormalizeRepository(%q) = %q, want no repository", remote, got)
		}
	}
	if !SameRepository("git@github.com:ACME/Widgets.git", "acme/widgets") || SameRepository("git@github.com:acme/gadgets.git", "acme/widgets") {
		t.Error("SameRepository compares case-insensitively and exactly")
	}
}

func TestRemoteRepositoryReportsTheHost(t *testing.T) {
	for remote, want := range map[string][2]string{
		"https://user:token@GitHub.com/Acme/Widgets.git": {"github.com", "acme/widgets"},
		"git@github.com:acme/widgets.git":                {"github.com", "acme/widgets"},
		"github-work:acme/widgets":                       {"github-work", "acme/widgets"},
		"ssh://git@ssh.github.com:443/acme/widgets.git":  {"ssh.github.com", "acme/widgets"},
		"https://gitlab.example/acme/widgets":            {"gitlab.example", "acme/widgets"},
	} {
		host, repository, ok := RemoteRepository(remote)
		if !ok || host != want[0] || repository != want[1] {
			t.Errorf("RemoteRepository(%q) = %q, %q, %v; want %q, %q", remote, host, repository, ok, want[0], want[1])
		}
	}
}

func TestCurrentBranch(t *testing.T) {
	f := newFixture(t)
	r := open(t, f.dir, Options{})
	git(t, f.dir, "checkout", "-q", "-b", "topic/widgets")
	if got, err := r.CurrentBranch(context.Background()); err != nil || got != "topic/widgets" {
		t.Fatalf("on a branch: %q, %v", got, err)
	}
	git(t, f.dir, "checkout", "-q", "--detach")
	if got, err := r.CurrentBranch(context.Background()); err != nil || got != "" {
		t.Fatalf("detached: %q, %v", got, err)
	}
}

func TestRemoteFetchURLAppliesInsteadOf(t *testing.T) {
	f := newFixture(t)
	r := open(t, f.dir, Options{})
	git(t, f.dir, "config", "url.https://github.com/.insteadOf", "gh:")
	git(t, f.dir, "remote", "add", "origin", "gh:acme/widgets")
	ctx := context.Background()
	if got, err := r.RemoteFetchURL(ctx, "origin"); err != nil || got != "https://github.com/acme/widgets" {
		t.Errorf("origin = %q, %v; want the rewritten URL", got, err)
	}
	for _, name := range []string{"upstream", "", "--upload-pack=x"} {
		if got, err := r.RemoteFetchURL(ctx, name); err != nil || got != "" {
			t.Errorf("remote %q = %q, %v; want none", name, got, err)
		}
	}
}

func TestBranchUpstream(t *testing.T) {
	f := newFixture(t)
	r := open(t, f.dir, Options{})
	git(t, f.dir, "config", "branch.topic/sizes.remote", "upstream")
	git(t, f.dir, "config", "branch.topic/sizes.merge", "refs/pull/42/head")
	ctx := context.Background()
	if remote, merge, err := r.BranchUpstream(ctx, "topic/sizes"); err != nil || remote != "upstream" || merge != "refs/pull/42/head" {
		t.Errorf("upstream = %q, %q, %v", remote, merge, err)
	}
	if remote, merge, err := r.BranchUpstream(ctx, "main"); err != nil || remote != "" || merge != "" {
		t.Errorf("no upstream = %q, %q, %v", remote, merge, err)
	}
}
