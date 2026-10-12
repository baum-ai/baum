package locallink

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/lastpersonlabs/baum/internal/locallink/linktest"
)

func patch(t *testing.T, req PatchRequest) PatchResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	result, err := Patch(ctx, req)
	if err != nil {
		t.Fatalf("Patch: %v", err)
	}
	return result
}

func patchRequest(f linktest.Fixture, path string) PatchRequest {
	return PatchRequest{Dir: f.Repo, Repository: linktest.Repository, Head: f.Head, Base: f.Base, Path: path}
}

// extend commits files on the fixture's feature branch and returns the new
// tip, leaving main checked out as the fixture does.
func extend(t *testing.T, f linktest.Fixture, files map[string][]byte) string {
	t.Helper()
	linktest.Git(t, f.Repo, "checkout", "-q", "feature")
	for name, content := range files {
		path := filepath.Join(f.Repo, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, content, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	linktest.Git(t, f.Repo, "add", "-A")
	linktest.Git(t, f.Repo, "commit", "-q", "-m", "More")
	head := linktest.Git(t, f.Repo, "rev-parse", "HEAD")
	linktest.Git(t, f.Repo, "checkout", "-q", "main")
	return head
}

func TestPatchIsTheFilesUnifiedDiffFromItsFirstHunk(t *testing.T) {
	f := linktest.New(t)
	result := patch(t, patchRequest(f, "b.txt"))
	want := "@@ -0,0 +1 @@\n+b\n"
	if !result.Available || result.Patch != want {
		t.Fatalf("result = %+v, want patch %q", result, want)
	}
}

func TestPatchUsesThreeLinesOfContextAndTheMergeBase(t *testing.T) {
	f := linktest.New(t)
	var original strings.Builder
	for i := 1; i <= 20; i++ {
		original.WriteString("line " + string(rune('a'+i-1)) + "\n")
	}
	linktest.Git(t, f.Repo, "checkout", "-q", "main")
	extendMain := func(content string) string {
		if err := os.WriteFile(filepath.Join(f.Repo, "long.txt"), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		linktest.Git(t, f.Repo, "add", "-A")
		linktest.Git(t, f.Repo, "commit", "-q", "-m", "Long")
		return linktest.Git(t, f.Repo, "rev-parse", "HEAD")
	}
	base := extendMain(original.String())
	linktest.Git(t, f.Repo, "checkout", "-q", "-b", "topic")
	changed := strings.Replace(original.String(), "line j\n", "line J\n", 1)
	if err := os.WriteFile(filepath.Join(f.Repo, "long.txt"), []byte(changed), 0o644); err != nil {
		t.Fatal(err)
	}
	linktest.Git(t, f.Repo, "add", "-A")
	linktest.Git(t, f.Repo, "commit", "-q", "-m", "Edit")
	head := linktest.Git(t, f.Repo, "rev-parse", "HEAD")
	// The base branch moves on after the merge base; the patch must not show
	// that change.
	linktest.Git(t, f.Repo, "checkout", "-q", "main")
	moved := strings.Replace(original.String(), "line a\n", "line A\n", 1)
	if err := os.WriteFile(filepath.Join(f.Repo, "long.txt"), []byte(moved), 0o644); err != nil {
		t.Fatal(err)
	}
	linktest.Git(t, f.Repo, "add", "-A")
	linktest.Git(t, f.Repo, "commit", "-q", "-m", "Moved on")
	movedBase := linktest.Git(t, f.Repo, "rev-parse", "HEAD")

	req := PatchRequest{Dir: f.Repo, Repository: linktest.Repository, Head: head, Base: movedBase, MergeBase: base, Path: "long.txt"}
	result := patch(t, req)
	want := "@@ -7,7 +7,7 @@ line f\n line g\n line h\n line i\n-line j\n+line J\n line k\n line l\n line m\n"
	if !result.Available || result.Patch != want {
		t.Fatalf("result = %+v\nwant patch %q", result, want)
	}
	// Without the merge base the document's base is used.
	req.MergeBase = ""
	if result := patch(t, req); !strings.Contains(result.Patch, "-line A") {
		t.Errorf("from the moved base, patch = %q; want the base branch's change in it", result.Patch)
	}
}

func TestPatchOfAFileTheRangeDoesNotChangeIsUnavailable(t *testing.T) {
	f := linktest.New(t)
	result := patch(t, patchRequest(f, "a.txt"))
	if result.Available || result.Code != CodeUnchanged || !result.Stable() {
		t.Fatalf("result = %+v", result)
	}
}

func TestPatchOfABinaryFileIsUnavailable(t *testing.T) {
	f := linktest.New(t)
	f.Head = extend(t, f, map[string][]byte{"logo.bin": append([]byte("\x00\x01\x02binary"), make([]byte, 64)...)})
	result := patch(t, patchRequest(f, "logo.bin"))
	if result.Available || result.Code != CodeNoTextPatch {
		t.Fatalf("result = %+v", result)
	}
}

func TestPatchOverTheCapIsUnavailable(t *testing.T) {
	f := linktest.New(t)
	big := strings.Repeat("0123456789abcdef0123456789abcdef\n", MaxPatchBytes/32+1024)
	f.Head = extend(t, f, map[string][]byte{"big.txt": []byte(big), "small.txt": []byte("small\n")})
	result := patch(t, patchRequest(f, "big.txt"))
	if result.Available || result.Code != CodeTooLarge {
		t.Fatalf("big: %+v", result)
	}
	if result := patch(t, patchRequest(f, "small.txt")); !result.Available {
		t.Fatalf("a small file beside it: %+v", result)
	}
}

func TestPatchWithoutTheCommitsIsUnavailable(t *testing.T) {
	f := linktest.New(t)
	missing := strings.Repeat("e", 40)
	for name, mutate := range map[string]func(*PatchRequest){
		"head":       func(r *PatchRequest) { r.Head = missing },
		"merge base": func(r *PatchRequest) { r.MergeBase = missing },
		"base":       func(r *PatchRequest) { r.Base = missing },
	} {
		req := patchRequest(f, "b.txt")
		mutate(&req)
		if name == "base" {
			req.MergeBase = ""
		}
		result := patch(t, req)
		if result.Available || result.Code != CodeCommitsMissing || result.Stable() {
			t.Errorf("%s missing: %+v", name, result)
		}
	}
}

func TestPatchFromAnotherRepositoryIsUnavailable(t *testing.T) {
	f := linktest.New(t)
	req := patchRequest(f, "b.txt")
	req.Repository = "other/project"
	if result := patch(t, req); result.Available || result.Code != CodeRemoteMismatch {
		t.Fatalf("result = %+v", result)
	}
	req = patchRequest(f, "b.txt")
	req.Dir = t.TempDir()
	if result := patch(t, req); result.Available || result.Code != CodeNotAClone {
		t.Fatalf("outside a clone: %+v", result)
	}
}

func TestPatchRejectsPathsThatAreNotPlainRepositoryPaths(t *testing.T) {
	f := linktest.New(t)
	outside := filepath.Join(t.TempDir(), "evil.patch")
	for _, name := range []string{"--output=" + outside, "-p", "../b.txt", "/etc/passwd", "a/../b.txt", ":(top)b.txt", "*.txt", ""} {
		result := patch(t, patchRequest(f, name))
		if result.Available {
			t.Errorf("path %q: %+v", name, result)
		}
	}
	if _, err := os.Stat(outside); err == nil {
		t.Error("an option in the path wrote a file")
	}
	// A glob is a literal name, not a pattern: nothing is read for it.
	if result := patch(t, patchRequest(f, "*.txt")); result.Available || result.Code != CodeUnchanged {
		t.Errorf("a glob: %+v", result)
	}
}

// The clone's own configuration and attributes are never read: a diff driver,
// a text conversion, a filter, a pager or a rewritten diff setting in them
// does nothing.
func TestPatchIgnoresTheClonesConfigurationAndAttributes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the hostile commands are shell scripts")
	}
	f := linktest.New(t)
	marker := filepath.Join(t.TempDir(), "ran")
	script := filepath.Join(t.TempDir(), "hostile.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\ntouch '"+marker+"'\ncat \"$@\" 2>/dev/null\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, kv := range [][2]string{
		{"diff.external", script},
		{"diff.evil.command", script},
		{"diff.evil.textconv", script},
		{"diff.evil.cachetextconv", "false"},
		{"core.pager", script},
		{"pager.diff", script},
		{"core.fsmonitor", script},
		{"core.attributesFile", filepath.Join(f.Repo, "attributes")},
		{"filter.evil.clean", script},
		{"filter.evil.smudge", script},
		{"color.ui", "always"},
		{"color.diff", "always"},
		{"diff.noprefix", "true"},
		{"diff.context", "0"},
		{"diff.algorithm", "patience"},
		{"diff.renames", "copies"},
		{"diff.wordRegex", "."},
		{"diff.submodule", "log"},
	} {
		linktest.Git(t, f.Repo, "config", kv[0], kv[1])
	}
	for name, content := range map[string]string{
		"attributes":     "*.txt diff=evil filter=evil\n",
		".gitattributes": "*.txt diff=evil filter=evil\n",
		filepath.Join(".git", "info", "attributes"): "*.txt diff=evil filter=evil\n",
	} {
		if err := os.WriteFile(filepath.Join(f.Repo, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("GIT_EXTERNAL_DIFF", script)
	t.Setenv("GIT_PAGER", script)
	t.Setenv("PAGER", script)
	t.Setenv("GIT_DIFF_OPTS", "--unified=0")
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "diff.external")
	t.Setenv("GIT_CONFIG_VALUE_0", script)

	result := patch(t, patchRequest(f, "b.txt"))
	if !result.Available || result.Patch != "@@ -0,0 +1 @@\n+b\n" {
		t.Fatalf("result = %+v", result)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("a command from the clone's configuration ran")
	}
}

// A partial clone that lacks the blobs is not fetched from: the answer is
// unavailable, the promisor remote is not contacted and the blobs stay absent.
func TestPatchNeverFetchesIntoAPartialClone(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the upload-pack probe is a shell script")
	}
	f := linktest.New(t)
	linktest.Git(t, f.Repo, "config", "uploadpack.allowFilter", "true")
	linktest.Git(t, f.Repo, "config", "uploadpack.allowAnySHA1InWant", "true")
	partial := filepath.Join(t.TempDir(), "partial")
	linktest.Git(t, t.TempDir(), "clone", "-q", "--filter=blob:none", "--no-checkout", "file://"+filepath.ToSlash(f.Repo), partial)
	// Everything the pull request needs but its blobs: branches arrive as
	// remote-tracking refs only for the default branch, so fetch the feature
	// commits' trees by name, then drop to the clone's own blobless state.
	linktest.Git(t, partial, "fetch", "-q", "--filter=blob:none", "origin", f.Head)
	marker := filepath.Join(t.TempDir(), "contacted")
	probe := filepath.Join(t.TempDir(), "upload-pack.sh")
	if err := os.WriteFile(probe, []byte("#!/bin/sh\ntouch '"+marker+"'\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	// From here any contact with the promisor remote is recorded, and the
	// clone gets a remote that names the document's repository.
	linktest.Git(t, partial, "config", "remote.origin.uploadpack", probe)
	linktest.Git(t, partial, "remote", "add", "github", linktest.UnreachableOrigin)

	req := PatchRequest{Dir: partial, Repository: linktest.Repository, Head: f.Head, Base: f.Base, Path: "b.txt"}
	result := patch(t, req)
	if result.Available || result.Code != CodePartialClone {
		t.Fatalf("result = %+v, want unavailable %s", result, CodePartialClone)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("the promisor remote was contacted")
	}
}
