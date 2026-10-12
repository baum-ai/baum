package viewserver

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lastpersonlabs/baum/internal/locallink"
)

func TestOnlyBoundedPathsAreAnswered(t *testing.T) {
	atLimit := strings.Repeat("a", maxPatchPathBytes)
	overLimit := strings.Repeat("b", maxPatchPathBytes+1)
	p := &patcher{}
	_, base := start(t, Options{Document: []byte(patchDocument(headA, atLimit, overLimit)), Patch: p.fn})
	if resp, patch := fetchPatch(t, base, atLimit); resp.StatusCode != http.StatusOK || patch == nil {
		t.Errorf("a path of %d bytes: status %d", len(atLimit), resp.StatusCode)
	}
	if resp, _ := fetchPatch(t, base, overLimit); resp.StatusCode != http.StatusNotFound {
		t.Errorf("a path of %d bytes: status %d, want 404", len(overLimit), resp.StatusCode)
	}
	if n := p.calls.Load(); n != 1 {
		t.Errorf("calls = %d, want only the path within the limit read", n)
	}
}

func TestPatchReadsAreBoundedInNumber(t *testing.T) {
	const files = maxConcurrentPatches * 3
	paths := make([]string, files)
	for i := range paths {
		paths[i] = fmt.Sprintf("src/f%d.go", i)
	}
	var active, peak atomic.Int32
	release := make(chan struct{})
	p := &patcher{reply: func(_ int, path string) (locallink.PatchResult, error) {
		now := active.Add(1)
		for {
			seen := peak.Load()
			if now <= seen || peak.CompareAndSwap(seen, now) {
				break
			}
		}
		<-release
		active.Add(-1)
		return locallink.PatchResult{Available: true, Patch: "@@ -1 +1 @@\n-a\n+b\n"}, nil
	}}
	_, base := start(t, Options{Document: []byte(patchDocument(headA, paths...)), Patch: p.fn})

	var asked sync.WaitGroup
	for _, path := range paths {
		asked.Add(1)
		go func() {
			defer asked.Done()
			if resp, err := http.Get(base + "/api/patch?path=" + url.QueryEscape(path)); err == nil {
				_ = resp.Body.Close()
			}
		}()
	}
	deadline := time.Now().Add(wait)
	for active.Load() < maxConcurrentPatches && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(150 * time.Millisecond) // time for any read past the limit to start
	if got := peak.Load(); got != maxConcurrentPatches {
		t.Errorf("%d reads at once with %d asked for, want %d", got, files, maxConcurrentPatches)
	}
	close(release)
	asked.Wait()
	if n := p.calls.Load(); n != files {
		t.Errorf("calls = %d, want %d: every path is read in the end", n, files)
	}
}

// A request that arrives as the server stops is not answered with an empty
// 200, which the viewer would keep as "no patch".
func TestPatchDuringShutdownIsUnavailableNotNull(t *testing.T) {
	started := make(chan struct{})
	var once sync.Once
	fn := func(ctx context.Context, _ Review, _ string) (locallink.PatchResult, error) {
		once.Do(func() { close(started) })
		<-ctx.Done()
		return locallink.PatchResult{}, ctx.Err()
	}
	server, err := New(Options{Document: []byte(patchDocument(headA, "src/a.go")), Patch: fn})
	if err != nil {
		t.Fatal(err)
	}
	base, err := server.Listen(0)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	finished := make(chan error, 1)
	go func() { finished <- server.Serve(ctx) }()

	status := make(chan int, 1)
	go func() {
		resp, err := http.Get(base + "api/patch?path=src/a.go")
		if err != nil {
			status <- 0
			return
		}
		_ = resp.Body.Close()
		status <- resp.StatusCode
	}()
	select {
	case <-started:
	case <-time.After(wait):
		t.Fatal("the patch was never asked for")
	}
	cancel()
	select {
	case got := <-status:
		if got != http.StatusServiceUnavailable {
			t.Errorf("status %d, want 503", got)
		}
	case <-time.After(wait):
		t.Fatal("no answer")
	}
	<-finished
}
