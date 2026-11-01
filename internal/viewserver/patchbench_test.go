package viewserver

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/baum-ai/baum/internal/locallink"
)

// Diff-open cost: what opening one file's patch costs now that local reviews
// keep no hunk text. The repository is synthetic: 3000 files of 200 lines, a
// 5-commit history, then a pull request of 400 changed files with 3 hunks
// each. git/op counts the git processes of one open, through a PATH shim.

const (
	benchFiles   = 3000
	benchChanged = 400
)

func benchGit(b *testing.B, dir string, args ...string) string {
	b.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=Bench", "-c", "user.email=bench@example.com"}, args...)...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		b.Fatalf("git %v: %s %v", args, out, err)
	}
	return strings.TrimSpace(string(out))
}

func benchFile(i, rev int) string {
	var sb strings.Builder
	for l := 0; l < 200; l++ {
		line := fmt.Sprintf("func line%d_%d() { return %d } // padding padding padding", i, l, l)
		if rev == 1 && (l == 20 || l == 100 || l == 180) {
			line += " // CHANGED"
		}
		sb.WriteString(line + "\n")
	}
	return sb.String()
}

func benchRepo(b *testing.B) (dir, base, head string) {
	b.Helper()
	dir = b.TempDir()
	benchGit(b, dir, "init", "-q")
	benchGit(b, dir, "remote", "add", "origin", "https://github.com/acme/widgets")
	write := func(i, rev int) {
		p := filepath.Join(dir, fmt.Sprintf("pkg%d", i%50), fmt.Sprintf("f%d.go", i))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			b.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(benchFile(i, rev)), 0o644); err != nil {
			b.Fatal(err)
		}
	}
	for i := 0; i < benchFiles; i++ {
		write(i, 0)
	}
	benchGit(b, dir, "add", "-A")
	benchGit(b, dir, "commit", "-q", "-m", "base")
	for c := 0; c < 5; c++ {
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("hist%d.txt", c)), []byte("x\n"), 0o644); err != nil {
			b.Fatal(err)
		}
		benchGit(b, dir, "add", "-A")
		benchGit(b, dir, "commit", "-q", "-m", fmt.Sprintf("hist %d", c))
	}
	base = benchGit(b, dir, "rev-parse", "HEAD")
	for i := 0; i < benchChanged; i++ {
		write(i, 1)
	}
	benchGit(b, dir, "add", "-A")
	benchGit(b, dir, "commit", "-q", "-m", "pr")
	head = benchGit(b, dir, "rev-parse", "HEAD")
	return dir, base, head
}

func benchPath(i int) string {
	i %= benchChanged
	return fmt.Sprintf("pkg%d/f%d.go", i%50, i)
}

// gitCount runs fn with a git shim first on PATH and returns how many git
// processes it started.
func gitCount(b *testing.B, fn func()) int {
	b.Helper()
	realGit, err := exec.LookPath("git")
	if err != nil {
		b.Skip("git is not installed")
	}
	shim := b.TempDir()
	counter := filepath.Join(shim, "count")
	script := fmt.Sprintf("#!/bin/sh\necho x >> %q\nexec %q \"$@\"\n", counter, realGit)
	if err := os.WriteFile(filepath.Join(shim, "git"), []byte(script), 0o755); err != nil {
		b.Fatal(err)
	}
	original := os.Getenv("PATH")
	b.Setenv("PATH", shim+string(os.PathListSeparator)+original)
	defer b.Setenv("PATH", original)
	fn()
	counted, _ := os.ReadFile(counter)
	return strings.Count(string(counted), "x")
}

func BenchmarkDiffOpen(b *testing.B) {
	dir, base, head := benchRepo(b)
	req := func(i int) locallink.PatchRequest {
		return locallink.PatchRequest{Dir: dir, Repository: "acme/widgets", Head: head, Base: base, MergeBase: base, Path: benchPath(i)}
	}
	ctx := context.Background()
	check := func(r locallink.PatchResult, err error) {
		if err != nil || !r.Available || !strings.Contains(r.Patch, "CHANGED") {
			b.Fatalf("patch: %+v %v", r, err)
		}
	}

	// First open of a range: a new Patcher finds the changed files, then
	// reads the patch.
	b.Run("cold-first-open", func(b *testing.B) {
		n := gitCount(b, func() { check(new(locallink.Patcher).Patch(ctx, req(0))) })
		b.ReportAllocs()
		i := 0
		for b.Loop() {
			check(new(locallink.Patcher).Patch(ctx, req(i)))
			i++
		}
		b.ReportMetric(float64(n), "git/op")
	})
	// Another file of the same range: the changed-file set is kept, the
	// patch is read again.
	b.Run("cold-next-file", func(b *testing.B) {
		p := new(locallink.Patcher)
		check(p.Patch(ctx, req(0)))
		n := gitCount(b, func() { check(p.Patch(ctx, req(1))) })
		b.ReportAllocs()
		i := 2
		for b.Loop() {
			check(p.Patch(ctx, req(i)))
			i++
		}
		b.ReportMetric(float64(n), "git/op")
	})
	// A repeat open through the viewserver: served from its cache.
	b.Run("warm-http", func(b *testing.B) {
		p := new(locallink.Patcher)
		var hunks []string
		for i := 0; i < benchChanged; i++ {
			hunks = append(hunks, fmt.Sprintf(`{"id":"h%d","path":%q}`, i, benchPath(i)))
		}
		doc := fmt.Sprintf(`{"schema":"baum.review/1","review":{"repository":"acme/widgets","pull_request":7,"head":%q,"base":%q,"merge_base":%q},"index":{"hunks":[%s]}}`+"\n",
			head, base, base, strings.Join(hunks, ","))
		srv, err := New(Options{Document: []byte(doc), Warn: os.Stderr, Patch: func(ctx context.Context, r Review, path string) (locallink.PatchResult, error) {
			return p.Patch(ctx, locallink.PatchRequest{Dir: dir, Repository: r.Repository, Head: r.Head, Base: r.Base, MergeBase: r.MergeBase, Path: path})
		}})
		if err != nil {
			b.Fatal(err)
		}
		u, err := srv.Listen(0)
		if err != nil {
			b.Fatal(err)
		}
		sctx, cancel := context.WithCancel(ctx)
		defer cancel()
		go func() { _ = srv.Serve(sctx) }()
		root := strings.TrimSuffix(u, "/")
		fetch := func(i int) {
			resp, err := http.Get(root + "/api/patch?path=" + url.QueryEscape(benchPath(i)))
			if err != nil {
				b.Fatal(err)
			}
			body, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode != 200 || !strings.Contains(string(body), "CHANGED") {
				b.Fatalf("status %d %s", resp.StatusCode, body)
			}
		}
		fetch(0) // cold, fills the cache
		n := gitCount(b, func() { fetch(0) })
		b.ReportAllocs()
		for b.Loop() {
			fetch(0)
		}
		b.ReportMetric(float64(n), "git/op")
	})
}
