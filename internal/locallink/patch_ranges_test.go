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
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
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
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
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
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
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
