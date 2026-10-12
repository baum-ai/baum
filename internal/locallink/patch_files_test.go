package locallink

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/lastpersonlabs/baum/internal/locallink/linktest"
)

// numbered is a file of n numbered lines.
func numbered(n int) string {
	var b strings.Builder
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&b, "line %02d\n", i)
	}
	return b.String()
}

// onFeature runs change on the feature branch, commits, and returns the new
// tip with main checked out again.
func onFeature(t *testing.T, f linktest.Fixture, change func()) string {
	t.Helper()
	linktest.Git(t, f.Repo, "checkout", "-q", "feature")
	change()
	linktest.Git(t, f.Repo, "add", "-A")
	linktest.Git(t, f.Repo, "commit", "-q", "-m", "More")
	head := linktest.Git(t, f.Repo, "rev-parse", "HEAD")
	linktest.Git(t, f.Repo, "checkout", "-q", "main")
	return head
}

func writeIn(t *testing.T, f linktest.Fixture, name, content string) {
	t.Helper()
	path := filepath.Join(f.Repo, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// A file the range renamed and edited shows what changed in it, found by the
// old name the range's file list gives, as the hosted service reads it.
func TestPatchOfARenamedFileShowsOnlyItsEdit(t *testing.T) {
	f := linktest.New(t)
	f.Base = onFeature(t, f, func() { writeIn(t, f, "old/name.txt", numbered(40)) })
	f.Head = onFeature(t, f, func() {
		if err := os.MkdirAll(filepath.Join(f.Repo, "new"), 0o755); err != nil {
			t.Fatal(err)
		}
		linktest.Git(t, f.Repo, "mv", "old/name.txt", "new/place.txt")
		writeIn(t, f, "new/place.txt", strings.Replace(numbered(40), "line 20\n", "line 20\nline 20b\n", 1))
	})

	result := patch(t, patchRequest(f, "new/place.txt"))
	want := "@@ -18,6 +18,7 @@ line 17\n line 18\n line 19\n line 20\n+line 20b\n line 21\n line 22\n line 23\n"
	if !result.Available || result.Patch != want {
		t.Fatalf("result = %+v\nwant patch %q", result, want)
	}
}

// Only a path the range changes has a patch: a directory has none, whether
// one file or several changed under it.
func TestPatchOfADirectoryIsUnavailable(t *testing.T) {
	f := linktest.New(t)
	f.Head = onFeature(t, f, func() {
		writeIn(t, f, "dir/one.txt", "one\n")
		writeIn(t, f, "dir/two.txt", "two\n")
		writeIn(t, f, "solo/only.txt", "only\n")
	})
	for _, name := range []string{"dir", "solo"} {
		if result := patch(t, patchRequest(f, name)); result.Available || result.Code != CodeUnchanged {
			t.Errorf("%s: %+v", name, result)
		}
	}
	if result := patch(t, patchRequest(f, "solo/only.txt")); !result.Available || result.Patch != "@@ -0,0 +1 @@\n+only\n" {
		t.Errorf("a file of it: %+v", result)
	}
}

// A file that became a symlink is diffed as a deletion and an addition of the
// one path, as the hosted service shows it.
func TestPatchOfATypeChangeHasBothSections(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on Windows")
	}
	f := linktest.New(t)
	f.Head = onFeature(t, f, func() {
		if err := os.Remove(filepath.Join(f.Repo, "a.txt")); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("b.txt", filepath.Join(f.Repo, "a.txt")); err != nil {
			t.Fatal(err)
		}
	})
	result := patch(t, patchRequest(f, "a.txt"))
	want := "@@ -1 +0,0 @@\n-a\n@@ -0,0 +1 @@\n+b.txt\n\\ No newline at end of file\n"
	if !result.Available || result.Patch != want {
		t.Fatalf("result = %+v\nwant patch %q", result, want)
	}
}

func TestPatchOfSeveralFilesIsNoFilesPatch(t *testing.T) {
	two := "diff --git a/x b/x\n--- a/x\n+++ b/x\n@@ -1 +1 @@\n-a\n+b\ndiff --git a/y b/y\n--- a/y\n+++ b/y\n@@ -1 +1 @@\n-a\n+b\n"
	for _, typeChanged := range []bool{false, true} {
		if result, _ := patchOf(two, typeChanged); result.Available || result.Code != CodeNoTextPatch {
			t.Errorf("two files (type change %v): %+v", typeChanged, result)
		}
	}
	same := "diff --git a/x b/x\ndeleted file mode 100644\n--- a/x\n+++ /dev/null\n@@ -1 +0,0 @@\n-a\ndiff --git a/x b/x\nnew file mode 120000\n--- /dev/null\n+++ b/x\n@@ -0,0 +1 @@\n+b\n"
	if result, _ := patchOf(same, false); result.Available {
		t.Errorf("two sections of one path without a type change: %+v", result)
	}
	if result, _ := patchOf(same, true); !result.Available {
		t.Errorf("a type change: %+v", result)
	}
}

// A submodule has no text patch, as in the hosted service.
func TestPatchOfASubmoduleIsUnavailable(t *testing.T) {
	f := linktest.New(t)
	f.Head = onFeature(t, f, func() {
		nested := filepath.Join(f.Repo, "vendor", "sub")
		writeIn(t, f, "vendor/sub/file.txt", "inside\n")
		linktest.Git(t, nested, "init", "-q", "-b", "main")
		linktest.Git(t, nested, "add", "-A")
		linktest.Git(t, nested, "commit", "-q", "-m", "Inside")
	})
	if result := patch(t, patchRequest(f, "vendor/sub")); result.Available || result.Code != CodeNoTextPatch {
		t.Fatalf("result = %+v", result)
	}
}
