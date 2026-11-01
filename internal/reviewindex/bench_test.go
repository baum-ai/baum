package reviewindex

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/baum-ai/baum/internal/benchcorpus"
	"github.com/baum-ai/baum/internal/discovery"
	"github.com/baum-ai/baum/internal/privatefs"
	"github.com/baum-ai/baum/internal/repos"
	"github.com/baum-ai/baum/internal/store"
)

// benchService reads the benchmark corpus through n clones that each stand for
// an independent store of the same size.
func benchService(b *testing.B, n int) (*Service, string) {
	b.Helper()
	root, label := benchcorpus.Root(b)
	s, err := store.Open("", root, false)
	if err != nil {
		b.Fatal(err)
	}
	rows, err := s.Snapshot(context.Background())
	if err != nil || len(rows) == 0 {
		b.Fatalf("corpus %s: %d rows, err=%v", label, len(rows), err)
	}
	repository := rows[0].Key.Repository
	prs := make([]discovery.PullRequest, 0, len(rows))
	for _, row := range rows {
		if row.Key.Repository == repository {
			prs = append(prs, discovery.PullRequest{Repository: repository, Number: row.Key.Number, HeadSHA: "head", State: "OPEN", Title: row.Title})
		}
	}
	clones := make(registry, n)
	for i := range clones {
		clones[i] = repos.Clone{ID: fmt.Sprintf("clone-%d", i), Repository: repository, CommonDir: fmt.Sprintf("/bench/clone-%d/.git", i), Available: true}
	}
	return &Service{
		Registry:  clones,
		Discovery: source{repository: {PullRequests: prs}},
		OpenStore: func(string) (Reader, error) { return store.Open("", root, false) },
	}, label
}

// BenchmarkBuild measures Service.Build with a static registry and discovery
// and real store readers: the store and index-assembly share of one build.
func BenchmarkBuild(b *testing.B) {
	for _, n := range []int{1, 4} {
		b.Run(fmt.Sprintf("clones=%d", n), func(b *testing.B) {
			service, label := benchService(b, n)
			ctx := context.Background()
			snapshot, err := service.Build(ctx)
			if err != nil || len(snapshot.Entries) == 0 {
				b.Fatalf("%d entries, err=%v", len(snapshot.Entries), err)
			}
			b.Logf("corpus=%s entries=%d", label, len(snapshot.Entries))
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				if _, err := service.Build(ctx); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkBuildGitProbe measures Service.Build with the real repos.Registry
// over n temporary git clones, so Repositories probes each clone with git.
// A git shim first on PATH counts the processes of one build (the git/op
// metric); the timed loop runs with the real git on PATH.
func BenchmarkBuildGitProbe(b *testing.B) {
	realGit, err := exec.LookPath("git")
	if err != nil {
		b.Skip("git is not installed")
	}
	for _, n := range []int{1, 4} {
		b.Run(fmt.Sprintf("clones=%d", n), func(b *testing.B) {
			service, label := benchService(b, n)
			repository := service.Registry.(registry)[0].Repository
			ctx := context.Background()
			private := filepath.Join(b.TempDir(), "private")
			if err := privatefs.MkdirAll(private); err != nil {
				b.Fatal(err)
			}
			reg, err := repos.Open(filepath.Join(private, "registry.sqlite"))
			if err != nil {
				b.Fatal(err)
			}
			b.Cleanup(func() { _ = reg.Close() })
			for range n {
				dir := b.TempDir()
				for _, args := range [][]string{
					{"init", "-q"},
					{"-c", "user.name=Bench", "-c", "user.email=bench@example.com", "commit", "-q", "--allow-empty", "-m", "init"},
					{"remote", "add", "origin", "https://github.com/" + repository},
				} {
					cmd := exec.Command(realGit, args...)
					cmd.Dir = dir
					if out, err := cmd.CombinedOutput(); err != nil {
						b.Fatalf("git %v: %s %v", args, out, err)
					}
				}
				if _, err := reg.Add(ctx, dir, nil); err != nil {
					b.Fatal(err)
				}
			}
			service.Registry = reg

			shim := b.TempDir()
			counter := filepath.Join(shim, "count")
			script := fmt.Sprintf("#!/bin/sh\necho x >> %q\nexec %q \"$@\"\n", counter, realGit)
			if err := os.WriteFile(filepath.Join(shim, "git"), []byte(script), 0o755); err != nil {
				b.Fatal(err)
			}
			original := os.Getenv("PATH")
			b.Setenv("PATH", shim+string(os.PathListSeparator)+original)
			if _, err := service.Build(ctx); err != nil {
				b.Fatal(err)
			}
			counted, err := os.ReadFile(counter)
			if err != nil {
				b.Fatal(err)
			}
			perBuild := strings.Count(string(counted), "x")
			b.Setenv("PATH", original)
			b.Logf("corpus=%s clones=%d git processes per Build=%d", label, n, perBuild)

			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				if _, err := service.Build(ctx); err != nil {
					b.Fatal(err)
				}
			}
			b.ReportMetric(float64(perBuild), "git/op")
		})
	}
}

// BenchmarkCachedBuild reads the corpus through a Cache with nothing
// changing: after the warm-up build every read is served from the cache, so
// builds/op, the builds per read, stays 0.
func BenchmarkCachedBuild(b *testing.B) {
	for _, n := range []int{1, 4} {
		b.Run(fmt.Sprintf("clones=%d", n), func(b *testing.B) {
			service, _ := benchService(b, n)
			ctx := context.Background()
			cache := NewCache(ctx, service)
			if _, err := cache.Snapshot(ctx); err != nil {
				b.Fatal(err)
			}
			warm := cache.buildCount()
			reads := 0
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				if _, err := cache.Snapshot(ctx); err != nil {
					b.Fatal(err)
				}
				reads++
			}
			b.ReportMetric(float64(cache.buildCount()-warm)/float64(reads), "builds/op")
		})
	}
}

// BenchmarkFingerprint measures the watcher's per-tick check: the stats over
// n clones, each with a store, and separately the registry read of four
// picked repositories.
func BenchmarkFingerprint(b *testing.B) {
	b.Run("registry", func(b *testing.B) {
		private := filepath.Join(b.TempDir(), "private")
		if err := privatefs.MkdirAll(private); err != nil {
			b.Fatal(err)
		}
		reg, err := repos.Open(filepath.Join(private, "app.sqlite"))
		if err != nil {
			b.Fatal(err)
		}
		b.Cleanup(func() { _ = reg.Close() })
		for i := range 4 {
			if _, err := reg.Pick(context.Background(), fmt.Sprintf("acme/r%d", i), nil); err != nil {
				b.Fatal(err)
			}
		}
		fingerprint := Fingerprint(reg.RepositoryRows, func() string { return "1" })
		ctx := context.Background()
		b.ReportAllocs()
		for b.Loop() {
			fingerprint(ctx)
		}
	})
	for _, n := range []int{1, 4} {
		b.Run(fmt.Sprintf("clones=%d", n), func(b *testing.B) {
			dir := b.TempDir()
			b.Setenv("XDG_DATA_HOME", filepath.Join(dir, "data"))
			clones := make([]repos.Clone, n)
			for i := range clones {
				path := filepath.Join(dir, fmt.Sprint(i))
				common := filepath.Join(path, ".git")
				if err := os.MkdirAll(filepath.Join(common, "baum", "reviews"), 0o700); err != nil {
					b.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(common, "baum", "reviews", "versions.sqlite"), nil, 0o600); err != nil {
					b.Fatal(err)
				}
				clones[i] = repos.Clone{ID: fmt.Sprint(i), Path: path, CommonDir: common}
			}
			picked := []repos.Repository{{Repository: "acme/a", Clones: clones}}
			fingerprint := Fingerprint(func(context.Context) ([]repos.Repository, error) { return picked, nil }, nil)
			ctx := context.Background()
			b.ReportAllocs()
			for b.Loop() {
				fingerprint(ctx)
			}
		})
	}
}
