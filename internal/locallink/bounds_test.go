package locallink

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lastpersonlabs/baum/internal/gitlocal"
	"github.com/lastpersonlabs/baum/internal/locallink/linktest"
	"github.com/lastpersonlabs/baum/redact"
	"github.com/lastpersonlabs/baum/sessions"
)

func TestResolveAcceptsAbbreviatedIds(t *testing.T) {
	f := linktest.New(t)
	req := request(f)
	req.Head, req.Base = f.Head[:12], f.Base[:7]
	result := resolve(t, req)
	if !result.Available || len(result.Commits) != 2 || result.Commits[0].OID != f.Commits[0] {
		t.Fatalf("result = %+v", result)
	}
}

func TestResolvePrefersTheMergeBase(t *testing.T) {
	f := linktest.New(t)
	req := request(f)
	req.MergeBase = f.Commits[0] // the range then starts after the first commit
	result := resolve(t, req)
	if !result.Available || len(result.Commits) != 1 || result.Commits[0].OID != f.Commits[1] {
		t.Fatalf("result = %+v", result)
	}

	// A merge base the clone lacks falls back to the base.
	req.MergeBase = strings.Repeat("a", 40)
	if result := resolve(t, req); !result.Available || len(result.Commits) != 2 {
		t.Fatalf("fallback result = %+v", result)
	}
	// A merge base alone is enough.
	req.Base, req.MergeBase = "", f.Base
	if result := resolve(t, req); !result.Available || len(result.Commits) != 2 {
		t.Fatalf("merge base only result = %+v", result)
	}
}

func TestResolveKeepsTheNewestSnapshotOfASession(t *testing.T) {
	f := linktest.New(t)
	ctx := context.Background()
	earlier, _, err := sessions.Load(ctx, f.Repo, f.EarlierSnapshot(t))
	if err != nil || len(earlier) != 1 {
		t.Fatalf("earlier = %v, %v", earlier, err)
	}
	later, _, err := sessions.Load(ctx, f.Repo, f.Store)
	if err != nil || len(later) != 1 {
		t.Fatalf("later = %v, %v", later, err)
	}
	earlier[0].ModifiedAt = later[0].ModifiedAt.Add(-time.Hour)
	engine, err := redact.New()
	if err != nil {
		t.Fatal(err)
	}
	commits := commitsOf(t, f)
	for name, found := range map[string][]sessions.Session{
		"oldest first": {earlier[0], later[0]},
		"newest first": {later[0], earlier[0]},
	} {
		result, err := match(ctx, engine, found, commits)
		if err != nil {
			t.Fatal(err)
		}
		if len(result.Sessions) != 1 || len(result.Commits[0].Links) != 1 {
			t.Errorf("%s: the newer snapshot's commit ref must link: %+v", name, result)
		}
	}
}

func TestResolveSkipsSessionsOlderThanThePullRequest(t *testing.T) {
	f := linktest.New(t)
	old := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	err := filepath.WalkDir(f.Store.ClaudeProjectsDir, func(path string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			return os.Chtimes(path, old, old)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	result := resolve(t, request(f))
	if !result.Available || len(result.Sessions) != 0 || len(result.Commits) != 2 {
		t.Fatalf("result = %+v", result)
	}
}

func TestSessionsSince(t *testing.T) {
	dated := func(dates ...string) []gitlocalCommit {
		var out []gitlocalCommit
		for _, d := range dates {
			out = append(out, gitlocalCommit{CommittedAt: d})
		}
		return out
	}
	got, ok := sessionsSince(dated("2026-07-02T10:00:00Z", "2026-07-01T10:00:00+02:00"))
	if want := time.Date(2026, 6, 29, 8, 0, 0, 0, time.UTC); !ok || !got.Equal(want) {
		t.Errorf("since = %v, %v; want %v", got, ok, want)
	}
	if _, ok := sessionsSince(dated("2026-07-02T10:00:00Z", "not a date")); ok {
		t.Error("an unreadable date must load everything")
	}
	if _, ok := sessionsSince(nil); ok {
		t.Error("no commits has no bound")
	}
}

func TestResolveNamesTheRemotesOnAMismatch(t *testing.T) {
	f := linktest.New(t)
	req := request(f)
	req.Repository = "upstream/widgets"
	result := resolve(t, req)
	if result.Code != CodeRemoteMismatch || !strings.Contains(result.Reason, "origin \u2192 acme/widgets") || !strings.Contains(result.Reason, "upstream/widgets") {
		t.Errorf("reason = %q", result.Reason)
	}
	if strings.Contains(result.Reason, "192.0.2.1") {
		t.Errorf("reason leaks the URL: %q", result.Reason)
	}
}

func TestResolveLeavesNothingBehindWhenCancelled(t *testing.T) {
	f := linktest.New(t)
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Resolve(ctx, request(f)); err == nil {
		t.Error("a cancelled context must stop the work")
	}
	if left, _ := filepath.Glob(filepath.Join(tmp, "baum-git-*")); len(left) != 0 {
		t.Errorf("left behind: %v", left)
	}
}

type gitlocalCommit = gitlocal.Commit

func commitsOf(t *testing.T, f linktest.Fixture) []gitlocal.Commit {
	t.Helper()
	repo, err := gitlocal.Open(context.Background(), f.Repo, gitlocal.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = repo.Close() }()
	commits, err := repo.Commits(context.Background(), f.Base, f.Head)
	if err != nil {
		t.Fatal(err)
	}
	return commits
}
