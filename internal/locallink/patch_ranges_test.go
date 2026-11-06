package locallink

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/baum-ai/baum/internal/locallink/linktest"
)

// mirrors counts the private repositories open under tmp.
func mirrors(t *testing.T, tmp string) int {
	t.Helper()
	left, err := filepath.Glob(filepath.Join(tmp, "baum-git-*"))
	if err != nil {
		t.Fatal(err)
	}
	return len(left)
}

func twoChangedFiles(t *testing.T) linktest.Fixture {
	t.Helper()
	f := linktest.New(t)
	f.Head = onFeature(t, f, func() {
		writeIn(t, f, "one.txt", numbered(5))
		writeIn(t, f, "two.txt", numbered(6))
	})
	return f
}

func patchWith(t *testing.T, p *Patcher, req PatchRequest) PatchResult {
	t.Helper()
	result, err := p.Patch(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

// A Patcher keeps a range's opened clone between reads, so a second file of
// the range opens nothing more, and Close closes it.
func TestPatcherKeepsARangesOpenClone(t *testing.T) {
	f := twoChangedFiles(t)
	tmp := linktest.TempDir(t)
	p := new(Patcher)
	for _, path := range []string{"one.txt", "two.txt"} {
		if result := patchWith(t, p, patchRequest(f, path)); !result.Available {
			t.Fatalf("%s: %+v", path, result)
		}
		if n := mirrors(t, tmp); n != 1 {
			t.Fatalf("after %s: %d clones open, want the one kept", path, n)
		}
	}
	p.Close()
	if n := mirrors(t, tmp); n != 0 {
		t.Fatalf("%d clones open after Close", n)
	}
	// The Patcher is still usable after Close.
	if result := patchWith(t, p, patchRequest(f, "one.txt")); !result.Available {
		t.Fatalf("after Close: %+v", result)
	}
	p.Close()
}

// An unavailable answer a person can fix is not kept: once the clone gets a
// matching remote, the next request reads the patch, and nothing stays open
// for the answer that was unavailable.
func TestPatcherResolvesAnUnavailableRangeAgain(t *testing.T) {
	f := twoChangedFiles(t)
	tmp := linktest.TempDir(t)
	p := new(Patcher)
	defer p.Close()
	req := patchRequest(f, "one.txt")
	req.Repository = "acme/other"
	if result := patchWith(t, p, req); result.Code != CodeRemoteMismatch {
		t.Fatalf("no matching remote: %+v", result)
	}
	if n := mirrors(t, tmp); n != 0 {
		t.Fatalf("%d clones kept for an unavailable range", n)
	}
	linktest.Git(t, f.Repo, "remote", "add", "other", "https://github.com/acme/other")
	if result := patchWith(t, p, req); !result.Available {
		t.Fatalf("after adding the remote: %+v", result)
	}
}

// A kept clone closes once it has been idle for rangeIdle.
func TestPatcherClosesAnIdleRange(t *testing.T) {
	f := twoChangedFiles(t)
	tmp := linktest.TempDir(t)
	defer func(idle time.Duration) { rangeIdle = idle }(rangeIdle)
	rangeIdle = 50 * time.Millisecond
	p := new(Patcher)
	defer p.Close()
	if result := patchWith(t, p, patchRequest(f, "one.txt")); !result.Available {
		t.Fatalf("%+v", result)
	}
	deadline := time.Now().Add(5 * time.Second)
	for mirrors(t, tmp) != 0 {
		if time.Now().After(deadline) {
			t.Fatal("the idle clone was not closed")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if result := patchWith(t, p, patchRequest(f, "two.txt")); !result.Available || !strings.Contains(result.Patch, "line 06") {
		t.Fatalf("after closing: %+v", result)
	}
}

// holdRange opens and resolves a range as a read does, failing the test
// when it is not kept; the returned release ends the read.
func holdRange(t *testing.T, p *Patcher, req PatchRequest) (*openRange, func()) {
	t.Helper()
	r, release, result, err := p.openRange(context.Background(), req, req.Base)
	if err != nil || r == nil {
		t.Fatalf("range not opened: %+v %v", result, err)
	}
	return r, release
}

// With more ranges in use than a Patcher keeps, the ones it lets go stay
// open for their reads and close when the last one ends; nothing is left
// open once the kept ones are closed.
func TestEvictedRangesInUseCloseWithTheirLastRead(t *testing.T) {
	f := twoChangedFiles(t)
	tmp := linktest.TempDir(t)
	p := new(Patcher)
	const ranges = maxChangeSets + 3
	var releases []func()
	for i := range ranges {
		// Each spelling of the directory is its own range.
		req := patchRequest(f, "one.txt")
		req.Dir = f.Repo + strings.Repeat("/.", i)
		r, release := holdRange(t, p, req)
		if _, err := r.repo.ChangedFiles(context.Background(), r.oids[0], r.oids[1]); err != nil {
			t.Fatal(err)
		}
		releases = append(releases, release)
	}
	if n := mirrors(t, tmp); n != ranges {
		t.Fatalf("%d clones open with %d ranges in use", n, ranges)
	}
	for _, release := range releases {
		release()
	}
	if n := mirrors(t, tmp); n != maxChangeSets {
		t.Fatalf("%d clones open once released, want the %d kept", n, maxChangeSets)
	}
	p.Close()
	if n := mirrors(t, tmp); n != 0 {
		t.Fatalf("%d clones open after Close", n)
	}
}

// Close during a read leaves the read's clone usable until the read ends,
// then closes it.
func TestCloseDuringAReadClosesWhenItEnds(t *testing.T) {
	f := twoChangedFiles(t)
	tmp := linktest.TempDir(t)
	p := new(Patcher)
	r, release := holdRange(t, p, patchRequest(f, "one.txt"))
	p.Close()
	if n := mirrors(t, tmp); n != 1 {
		t.Fatalf("%d clones open during the read", n)
	}
	if _, err := r.repo.FilePatch(context.Background(), r.oids[0], r.oids[1], MaxPatchBytes, "one.txt"); err != nil {
		t.Fatalf("the read's clone was closed under it: %v", err)
	}
	release()
	if n := mirrors(t, tmp); n != 0 {
		t.Fatalf("%d clones open after the read", n)
	}
}

// A range kept for rangeMaxAge is opened again by its next read, however
// busy it was; the old clone closes.
func TestABusyRangeIsReopenedAfterItsMaxAge(t *testing.T) {
	f := twoChangedFiles(t)
	tmp := linktest.TempDir(t)
	defer func(age time.Duration) { rangeMaxAge = age }(rangeMaxAge)
	rangeMaxAge = 50 * time.Millisecond
	p := new(Patcher)
	defer p.Close()
	first, release := holdRange(t, p, patchRequest(f, "one.txt"))
	release()
	again, release := holdRange(t, p, patchRequest(f, "one.txt"))
	release()
	if again != first {
		t.Fatal("a young range was opened again")
	}
	time.Sleep(2 * rangeMaxAge)
	renewed, release := holdRange(t, p, patchRequest(f, "two.txt"))
	release()
	if renewed == first {
		t.Fatal("a range past its max age was kept")
	}
	if n := mirrors(t, tmp); n != 1 {
		t.Fatalf("%d clones open, want the renewed one", n)
	}
}

// A read cancelled by its own request does not drop the kept range.
func TestACancelledReadKeepsTheRange(t *testing.T) {
	f := twoChangedFiles(t)
	p := new(Patcher)
	defer p.Close()
	kept, release := holdRange(t, p, patchRequest(f, "one.txt"))
	release()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _ = p.readPatch(ctx, patchRequest(f, "two.txt"))
	again, release := holdRange(t, p, patchRequest(f, "one.txt"))
	release()
	if again != kept {
		t.Fatal("a cancelled read dropped the range")
	}
}
