package locallink

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/baum-ai/baum/bundle"
	"github.com/baum-ai/baum/internal/locallink/linktest"
)

// A made-up key in Stripe's format, kept in pieces so the source does not
// trip a secret scanner.
var patchSecret = "sk_" + "live_" + "oHBvRPOIvGrv5iFlbCBFNOgmBjMtpsia"

// The patch a document leaves out is masked exactly as the document's own
// hunks are: the bundle's diff index and Redact over the same file give the
// same text, with the key masked, in both hunks of an edited file and in a
// file the range deletes.
func TestPatchIsRedactedAsTheStoredHunks(t *testing.T) {
	f := linktest.New(t)
	lines := strings.Split(strings.TrimSuffix(numbered(30), "\n"), "\n")
	linktest.Git(t, f.Repo, "checkout", "-q", "main")
	writeIn(t, f, "config.go", strings.Join(lines, "\n")+"\n")
	writeIn(t, f, "old.go", "token = \""+patchSecret+"\"\n")
	linktest.Git(t, f.Repo, "add", "-A")
	linktest.Git(t, f.Repo, "commit", "-q", "-m", "Config")
	base := linktest.Git(t, f.Repo, "rev-parse", "HEAD")
	linktest.Git(t, f.Repo, "checkout", "-q", "-b", "secrets")
	edited := append([]string(nil), lines...)
	edited[2] = "key = \"" + patchSecret + "\""
	edited[25] = "owner = dev@example.com"
	writeIn(t, f, "config.go", strings.Join(edited, "\n")+"\n")
	linktest.Git(t, f.Repo, "rm", "-q", "old.go")
	linktest.Git(t, f.Repo, "commit", "-q", "-am", "Secrets")
	head := linktest.Git(t, f.Repo, "rev-parse", "HEAD")
	linktest.Git(t, f.Repo, "checkout", "-q", "main")

	redactor, err := bundle.NewRedactor()
	if err != nil {
		t.Fatal(err)
	}
	for path, status := range map[string]string{"config.go": "modified", "old.go": "removed"} {
		result := patch(t, PatchRequest{Dir: f.Repo, Repository: linktest.Repository, Head: head, Base: base, Path: path})
		if !result.Available {
			t.Fatalf("%s: %+v", path, result)
		}
		raw := linktest.Git(t, f.Repo, "diff", "--no-color", "--unified=3", base, head, "--", path) + "\n"
		hunks, _, err := bundle.BuildDiffIndexWithin(bundle.Comparison{Files: []bundle.FileChange{{NewPath: path, Status: status}}},
			func(...string) ([]byte, error) { return []byte(raw), nil }, bundle.RoleForPath, bundle.DiffLimits{})
		if err != nil {
			t.Fatal(err)
		}
		stored := &bundle.Bundle{Diff: hunks}
		if err := bundle.Redact(context.Background(), stored, redactor); err != nil {
			t.Fatal(err)
		}
		var want strings.Builder
		for _, hunk := range stored.Diff {
			want.WriteString(hunk.Patch + "\n")
		}
		if result.Patch != want.String() {
			t.Errorf("%s: patch\n%s\nwant the stored hunks\n%s", path, result.Patch, want.String())
		}
		if strings.Contains(result.Patch, patchSecret) || strings.Contains(result.Patch, "dev@example.com") {
			t.Errorf("%s: a secret survives:\n%s", path, result.Patch)
		}
		if path == "config.go" && len(stored.Diff) != 2 {
			t.Errorf("config.go: %d hunks, want 2", len(stored.Diff))
		}
	}
}

// Requests for the same patch of the same clone share the read that is
// running, whichever document asked; a different path reads on its own, a
// later request reads again, and a request whose shared read failed reads
// itself.
func TestConcurrentPatchRequestsShareOneRead(t *testing.T) {
	var reads atomic.Int32
	hold := make(chan struct{})
	fail := atomic.Bool{}
	p := &Patcher{read: func(_ context.Context, req PatchRequest) (PatchResult, error) {
		reads.Add(1)
		<-hold
		if fail.Load() {
			return PatchResult{}, errors.New("git failed")
		}
		return PatchResult{Available: true, Patch: req.Path}, nil
	}}
	req := PatchRequest{Dir: "/clone", Repository: "acme/widgets", Head: "h1", Base: "b0", MergeBase: "m1", Path: "a.go"}
	// Two versions of the pull request with the same range: their requests
	// differ only in the base, which a merge base makes irrelevant.
	other := req
	other.Base = "b1"
	const askers = 8
	var done sync.WaitGroup
	for i := range askers {
		r := req
		if i%2 == 1 {
			r = other
		}
		done.Go(func() {
			result, err := p.Patch(context.Background(), r)
			if err != nil || result.Patch != "a.go" {
				t.Errorf("result %+v, err %v", result, err)
			}
		})
	}
	time.Sleep(100 * time.Millisecond) // time for every asker to reach the wait
	close(hold)
	done.Wait()
	if n := reads.Load(); n != 1 {
		t.Fatalf("read %d times, want 1", n)
	}
	b := req
	b.Path = "b.go"
	if _, err := p.Patch(context.Background(), b); err != nil || reads.Load() != 2 {
		t.Fatalf("another path: err %v, read %d times", err, reads.Load())
	}
	if _, err := p.Patch(context.Background(), req); err != nil || reads.Load() != 3 {
		t.Fatalf("a later request: err %v, read %d times", err, reads.Load())
	}

	fail.Store(true)
	hold = make(chan struct{})
	p.read = func(_ context.Context, req PatchRequest) (PatchResult, error) {
		n := reads.Add(1)
		if n == 4 {
			<-hold
			return PatchResult{}, errors.New("git failed")
		}
		return PatchResult{Available: true, Patch: req.Path}, nil
	}
	first := make(chan error, 1)
	go func() {
		_, err := p.Patch(context.Background(), req)
		first <- err
	}()
	for reads.Load() < 4 {
		time.Sleep(time.Millisecond)
	}
	second := make(chan PatchResult, 1)
	go func() {
		result, _ := p.Patch(context.Background(), req)
		second <- result
	}()
	time.Sleep(50 * time.Millisecond)
	close(hold)
	if err := <-first; err == nil {
		t.Error("the failure was not reported to the request that read")
	}
	if result := <-second; !result.Available || reads.Load() != 5 {
		t.Errorf("the waiting request: %+v after %d reads, want its own read", result, reads.Load())
	}
}
