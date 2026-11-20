package seeddata

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/baum-ai/baum/internal/discovery"
	"github.com/baum-ai/baum/internal/github"
	"github.com/baum-ai/baum/internal/inboxes"
	"github.com/baum-ai/baum/internal/prfilter"
	"github.com/baum-ai/baum/internal/repos"
	"github.com/baum-ai/baum/internal/reviewindex"
)

// small is a dataset small enough to write twice in a test.
func small(seed int64, home string) Options {
	return Options{Seed: seed, Home: home, Repositories: 4, PullRequests: 18, LargeDiffLines: 600, Inboxes: 12,
		ClaudeSessions: 2, CodexSessions: 1, LongTranscriptTurns: 40, DraftComments: 2}
}

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
}

// generate writes o into a fresh home at the same path every time, so the
// paths written into the data match.
func generate(t *testing.T, o Options) Result {
	t.Helper()
	if err := os.RemoveAll(o.Home); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(o.Home, 0o700); err != nil {
		t.Fatal(err)
	}
	result, err := Generate(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestSameSeedSameData(t *testing.T) {
	requireGit(t)
	home := filepath.Join(t.TempDir(), "home")
	first := generate(t, small(7, home))
	transcripts := readTranscripts(t, home)
	second := generate(t, small(7, home))
	if first.Digest != second.Digest || !slices.Equal(first.Manifest, second.Manifest) {
		for i := range min(len(first.Manifest), len(second.Manifest)) {
			if first.Manifest[i] != second.Manifest[i] {
				t.Fatalf("manifests differ at %d:\n%s\n%s", i, first.Manifest[i], second.Manifest[i])
			}
		}
		t.Fatalf("digests differ: %s %s", first.Digest, second.Digest)
	}
	again := readTranscripts(t, home)
	if len(again) != len(transcripts) || len(again) != 3 {
		t.Fatalf("transcripts: %d then %d, want 3", len(transcripts), len(again))
	}
	for path, text := range transcripts {
		if again[path] != text {
			t.Fatalf("%s differs between runs", path)
		}
	}
	other := generate(t, small(8, home))
	if other.Digest == first.Digest {
		t.Fatal("seeds 7 and 8 gave the same data")
	}
}

func readTranscripts(t *testing.T, home string) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, dir := range []string{".claude/projects", ".codex/sessions"} {
		_ = filepath.Walk(filepath.Join(home, dir), func(path string, info os.FileInfo, err error) error {
			if err == nil && strings.HasSuffix(path, ".jsonl") {
				raw, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				out[path] = string(raw)
			}
			return nil
		})
	}
	return out
}

// TestTheDaemonReadsIt reads the dataset back the way the daemon does:
// the registry's repositories, discovery's cache under the configured
// credential, the review index joining them with the saved reviews, and
// the inboxes.
func TestTheDaemonReadsIt(t *testing.T) {
	requireGit(t)
	home := filepath.Join(t.TempDir(), "home")
	result := generate(t, small(3, home))
	if result.Counts.LargeDiffLines != 600 {
		t.Errorf("large diff %d lines, want 600", result.Counts.LargeDiffLines)
	}
	if result.Counts.ReviewVersions == 0 || result.Counts.Sessions != 3 || result.Counts.LongestTranscript != 40 {
		t.Errorf("counts %+v", result.Counts)
	}
	ctx := context.Background()
	state := filepath.Join(home, ".local", "state", "baum")
	registry, err := repos.Open(filepath.Join(state, "app.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = registry.Close() }()
	picked, err := registry.Repositories(ctx)
	if err != nil || len(picked) != 4 {
		t.Fatalf("picked %d repositories: %v", len(picked), err)
	}
	prefs, err := registry.Preferences(ctx)
	if err != nil || prefs.Automatic {
		t.Fatalf("automatic reviews must be off: %+v %v", prefs, err)
	}
	disc := discovery.New(github.New(func(context.Context, string) (string, error) { return Token, nil }, github.Options{}), state)
	index, err := (&reviewindex.Service{Registry: registry, Discovery: disc}).Build(ctx)
	if err != nil {
		t.Fatal(err)
	}
	listed, reviewed := 0, 0
	for _, e := range index.Entries {
		if !e.Historical {
			listed++
		}
		if e.VersionCount > 0 {
			reviewed++
		}
	}
	if listed != result.Counts.PullRequests || reviewed != result.Counts.ReviewedPullRequests || len(index.Problems) > 0 {
		t.Fatalf("the index lists %d pull requests, %d reviewed, problems %v; want %d and %d", listed, reviewed, index.Problems,
			result.Counts.PullRequests, result.Counts.ReviewedPullRequests)
	}
	// Another credential reads none of it.
	other := discovery.New(github.New(func(context.Context, string) (string, error) { return "another", nil }, github.Options{}), state)
	if _, err := other.Cached(ctx, github.Repository{Host: "github.com", Owner: picked[0].Repository[:strings.Index(picked[0].Repository, "/")],
		Name: picked[0].Repository[strings.Index(picked[0].Repository, "/")+1:]}); err == nil {
		t.Fatal("another credential read the seeded cache")
	}
	saved, err := inboxes.Open(filepath.Join(state, "app.sqlite")).Read()
	if err != nil || len(saved.Inboxes) != 12 {
		t.Fatalf("%d inboxes: %v", len(saved.Inboxes), err)
	}
	for _, inbox := range saved.Inboxes {
		if problem := prfilter.Problem(inbox.Query); problem != "" {
			t.Errorf("inbox %s: %q does not read: %s", inbox.Name, inbox.Query, problem)
		}
	}
	if _, err := Generate(ctx, small(3, home)); err == nil {
		t.Fatal("Generate wrote over existing data")
	}
}

// TestDefaultsAreQuick writes the full default dataset, which the
// profiling recipe uses, and checks its size and that it takes well under
// a minute.
func TestDefaultsAreQuick(t *testing.T) {
	if testing.Short() {
		t.Skip("writes the full dataset")
	}
	requireGit(t)
	o := Defaults()
	o.Home = filepath.Join(t.TempDir(), "home")
	started := time.Now()
	result := generate(t, o)
	took := time.Since(started)
	t.Logf("defaults: %+v in %s", result.Counts, took)
	c := result.Counts
	if c.Repositories != 24 || c.PullRequests < 300 || c.LargeDiffLines != 5000 || c.Inboxes != 20 || c.LongestTranscript != 3000 {
		t.Fatalf("counts %+v", c)
	}
	if took > 50*time.Second {
		t.Fatalf("took %s", took)
	}
}
