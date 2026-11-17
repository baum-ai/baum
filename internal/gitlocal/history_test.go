package gitlocal

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
)

type historyFixture struct {
	dir                string
	c1, c2, c3, c4, f5 string // c3 renames a.txt to b.txt; f5 is on feature
}

// newHistoryFixture builds main: c1 (a.txt, 2020), c2 (edits line 2), c3
// (renames to b.txt), c4 (edits line 3 of b.txt); and feature, from c2: f5
// (adds f.txt).
func newHistoryFixture(t *testing.T) historyFixture {
	t.Helper()
	requireGit(t)
	dir := initRepo(t)
	t.Setenv("GIT_AUTHOR_DATE", "2020-01-01T10:00:00Z")
	t.Setenv("GIT_COMMITTER_DATE", "2020-01-01T10:00:00Z")
	write(t, dir, "a.txt", "one\ntwo\nthree\n", 0o644)
	c1 := commit(t, dir, "first")
	t.Setenv("GIT_AUTHOR_DATE", "2024-03-01T10:00:00Z")
	t.Setenv("GIT_COMMITTER_DATE", "2024-03-01T10:00:00Z")
	write(t, dir, "a.txt", "one\nTWO\nthree\n", 0o644)
	c2 := commit(t, dir, "second\x01 with | odd subject")
	git(t, dir, "checkout", "-q", "-b", "feature")
	write(t, dir, "f.txt", "feature\n", 0o644)
	f5 := commit(t, dir, "feature work")
	git(t, dir, "checkout", "-q", "main")
	git(t, dir, "mv", "a.txt", "b.txt")
	c3 := commit(t, dir, "rename")
	write(t, dir, "b.txt", "one\nTWO\nTHREE\n", 0o644)
	c4 := commit(t, dir, "third")
	return historyFixture{dir: dir, c1: c1, c2: c2, c3: c3, c4: c4, f5: f5}
}

func TestLogFiltersFollowsRenamesAndBounds(t *testing.T) {
	f := newHistoryFixture(t)
	r := open(t, f.dir, Options{})
	ctx := context.Background()

	all, truncated, err := r.Log(ctx, f.c4, nil, 30, time.Time{})
	if err != nil || truncated {
		t.Fatal(all, truncated, err)
	}
	var got []string
	for _, c := range all {
		got = append(got, c.SHA)
	}
	if !slices.Equal(got, []string{f.c4, f.c3, f.c2, f.c1}) {
		t.Fatalf("log = %v", got)
	}
	if all[3].Parents == nil || len(all[3].Parents) != 0 || len(all[0].Parents) != 1 || all[0].Parents[0] != f.c3 {
		t.Fatalf("parents = %#v", all)
	}
	if all[2].Subject != "second\x01 with | odd subject" || all[2].Author != "Test" || all[2].Date != "2024-03-01T10:00:00Z" {
		t.Fatalf("commit = %#v", all[2])
	}

	// One path follows the rename: b.txt reaches back through a.txt.
	followed, _, err := r.Log(ctx, f.c4[:10], []string{"b.txt"}, 30, time.Time{})
	if err != nil || len(followed) != 4 {
		t.Fatal(followed, err)
	}
	// Two paths do not follow, so only the commits touching either name.
	two, _, err := r.Log(ctx, f.c4, []string{"b.txt", "nothing"}, 30, time.Time{})
	if err != nil || len(two) != 2 {
		t.Fatalf("%v %v", two, err)
	}

	limited, truncated, err := r.Log(ctx, f.c4, nil, 2, time.Time{})
	if err != nil || !truncated || len(limited) != 2 || limited[1].SHA != f.c3 {
		t.Fatal(limited, truncated, err)
	}
	exact, truncated, err := r.Log(ctx, f.c4, nil, 4, time.Time{})
	if err != nil || truncated || len(exact) != 4 {
		t.Fatal(exact, truncated, err)
	}

	recent, _, err := r.Log(ctx, f.c4, nil, 30, time.Date(2023, 1, 1, 0, 0, 0, 0, time.UTC))
	if err != nil || len(recent) != 3 {
		t.Fatal(recent, err)
	}

	// The other branch's history is its own.
	feature, _, err := r.Log(ctx, f.f5, nil, 30, time.Time{})
	if err != nil || len(feature) != 3 || feature[0].SHA != f.f5 {
		t.Fatal(feature, err)
	}
}

func TestLogRejectsBadInputAndMissingCommits(t *testing.T) {
	f := newHistoryFixture(t)
	r := open(t, f.dir, Options{})
	ctx := context.Background()
	for name, call := range map[string]func() error{
		"limit zero":  func() error { _, _, err := r.Log(ctx, f.c4, nil, 0, time.Time{}); return err },
		"limit large": func() error { _, _, err := r.Log(ctx, f.c4, nil, 201, time.Time{}); return err },
		"bad path":    func() error { _, _, err := r.Log(ctx, f.c4, []string{"../x"}, 5, time.Time{}); return err },
		"option rev":  func() error { _, _, err := r.Log(ctx, "--all", nil, 5, time.Time{}); return err },
		"short rev":   func() error { _, _, err := r.Log(ctx, "abc", nil, 5, time.Time{}); return err },
		"many paths":  func() error { _, _, err := r.Log(ctx, f.c4, make([]string, 51), 5, time.Time{}); return err },
	} {
		if err := call(); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("%s: %v", name, err)
		}
	}
	_, _, err := r.Log(ctx, strings.Repeat("a", 40), nil, 5, time.Time{})
	if !errors.Is(err, ErrObjectNotFound) {
		t.Fatalf("missing commit: %v", err)
	}
}

func TestBlameAttributesLinesAcrossCommits(t *testing.T) {
	f := newHistoryFixture(t)
	r := open(t, f.dir, Options{})
	ctx := context.Background()

	lines, err := r.Blame(ctx, f.c4, "b.txt", 1, 3)
	if err != nil {
		t.Fatal(err)
	}
	want := []struct {
		sha, text, summary, date string
	}{
		{f.c1, "one", "first", "2020-01-01T10:00:00Z"},
		{f.c2, "TWO", "second\x01 with | odd subject", "2024-03-01T10:00:00Z"},
		{f.c4, "THREE", "third", "2024-03-01T10:00:00Z"},
	}
	if len(lines) != 3 {
		t.Fatalf("lines = %#v", lines)
	}
	for i, w := range want {
		l := lines[i]
		if l.Line != i+1 || l.SHA != w.sha || l.Text != w.text || l.Summary != w.summary || l.Date != w.date || l.Author != "Test" {
			t.Errorf("line %d = %#v, want %#v", i+1, l, w)
		}
	}

	// A commit that appears twice has its fields only on its first line.
	repeated, err := r.Blame(ctx, f.c1, "a.txt", 1, 3)
	if err != nil || len(repeated) != 3 {
		t.Fatal(repeated, err)
	}
	for _, l := range repeated {
		if l.SHA != f.c1 || l.Author != "Test" || l.Summary != "first" || l.Date == "" {
			t.Fatalf("repeated = %#v", repeated)
		}
	}
}

func TestBlameRejectsBadInput(t *testing.T) {
	f := newHistoryFixture(t)
	r := open(t, f.dir, Options{})
	ctx := context.Background()
	for name, call := range map[string]func() error{
		"start zero": func() error { _, err := r.Blame(ctx, f.c4, "b.txt", 0, 2); return err },
		"end before": func() error { _, err := r.Blame(ctx, f.c4, "b.txt", 3, 2); return err },
		"too many":   func() error { _, err := r.Blame(ctx, f.c4, "b.txt", 1, 2001); return err },
		"bad path":   func() error { _, err := r.Blame(ctx, f.c4, "/etc/passwd", 1, 2); return err },
		"no file":    func() error { _, err := r.Blame(ctx, f.c4, "a.txt", 1, 2); return err },
		"past end":   func() error { _, err := r.Blame(ctx, f.c4, "b.txt", 10, 12); return err },
	} {
		if err := call(); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := r.Blame(ctx, strings.Repeat("a", 40), "b.txt", 1, 2); !errors.Is(err, ErrObjectNotFound) {
		t.Fatalf("missing commit: %v", err)
	}
}

func TestChangedSinceUsesTheMergeBase(t *testing.T) {
	f := newHistoryFixture(t)
	r := open(t, f.dir, Options{})
	ctx := context.Background()

	// Main moved on since feature branched, so only feature's own file counts.
	mergeBase, files, truncated, err := r.ChangedSince(ctx, f.c4, f.f5)
	if err != nil || truncated || mergeBase != f.c2 || !slices.Equal(files, []string{"f.txt"}) {
		t.Fatal(mergeBase, files, truncated, err)
	}
	// A rename is listed under both names.
	_, files, _, err = r.ChangedSince(ctx, f.c2[:9], f.c3)
	if err != nil || !slices.Equal(files, []string{"a.txt", "b.txt"}) {
		t.Fatal(files, err)
	}
	_, files, _, err = r.ChangedSince(ctx, f.c4, f.c4)
	if err != nil || len(files) != 0 {
		t.Fatal(files, err)
	}
	if _, _, _, err := r.ChangedSince(ctx, "--all", f.c4); !errors.Is(err, ErrInvalidInput) {
		t.Fatal(err)
	}
	if _, _, _, err := r.ChangedSince(ctx, f.c4, strings.Repeat("b", 40)); !errors.Is(err, ErrObjectNotFound) {
		t.Fatal(err)
	}
}
