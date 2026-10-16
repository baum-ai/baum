package gitlocal

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/baum-ai/baum/bundle"
)

// hostedGit runs git in a bare mirror the way the hosted git mirror service
// does: a server environment with no GIT_ variables of its own, system and
// global configuration and system attributes off, and optional locks off.
func hostedGit(t *testing.T, mirror string, args ...string) []byte {
	t.Helper()
	var env []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "GIT_") {
			env = append(env, kv)
		}
	}
	env = append(env, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_ATTR_NOSYSTEM=1", "GIT_OPTIONAL_LOCKS=0")
	cmd := exec.Command("git", append([]string{"-C", mirror}, args...)...)
	cmd.Env = env
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("hosted git %s: %v: %s", args[0], err, stderr.String())
	}
	return out
}

// hostedMirror creates a bare mirror of clone as the hosted service does: an
// empty bare repository that fetches the branches into remote-tracking refs.
func hostedMirror(t *testing.T, clone string) string {
	t.Helper()
	mirror := filepath.Join(t.TempDir(), "mirror.git")
	hostedGit(t, filepath.Dir(mirror), "init", "-q", "--bare", mirror)
	hostedGit(t, mirror, "fetch", "-q", "--no-tags", clone, "+refs/heads/*:refs/remotes/origin/*")
	return mirror
}

// hostedCompare is the hosted comparison in its merge-base mode, with the
// hosted git invocations.
func hostedCompare(t *testing.T, mirror, base, head string) bundle.Comparison {
	t.Helper()
	mergeBase := strings.TrimSpace(string(hostedGit(t, mirror, "merge-base", base, head)))
	raw := hostedGit(t, mirror, "diff-tree", "-r", "--no-commit-id", "--raw", "-z", "-M", "-l1000", mergeBase, head)
	files, err := parseRawDiff(raw)
	if err != nil {
		t.Fatal(err)
	}
	applyNumstat(files, hostedGit(t, mirror, "diff-tree", "-r", "--no-commit-id", "--numstat", "-z", "-M", "-l1000", mergeBase, head))
	full := hostedGit(t, mirror, "diff", "--no-ext-diff", "--no-textconv", "--full-index", "-M", "-l1000", mergeBase, head)
	hashes, err := canonicalComparisonPatchHashesReader(bytes.NewReader(full))
	if err != nil || len(hashes) != len(files) {
		t.Fatalf("hosted patch hashes: %d for %d files, %v", len(hashes), len(files), err)
	}
	for i := range files {
		if files[i].Additions == nil || files[i].Deletions == nil {
			files[i].PatchHash = objectPatchHash(files[i])
		} else {
			files[i].PatchHash = hashes[i]
		}
	}
	return bundle.Comparison{BaseOID: mergeBase, HeadOID: head, MergeBaseOID: mergeBase, Files: files}
}

func hostedTextPatch(t *testing.T, mirror, base, head string, paths []string) []byte {
	t.Helper()
	args := []string{"diff", "--no-ext-diff", "--no-textconv", "--full-index", "-M", "-l1000", base, head, "--"}
	for _, p := range paths {
		args = append(args, ":(literal)"+p)
	}
	return hostedGit(t, mirror, args...)
}

func hostedBlob(t *testing.T, mirror, rev, path string) Blob {
	t.Helper()
	out := hostedGit(t, mirror, "ls-tree", "-z", "-l", rev, "--", ":(literal)"+path)
	meta, _, _ := strings.Cut(string(bytes.TrimSuffix(out, []byte{0})), "\t")
	fields := strings.Fields(meta)
	if len(fields) != 4 || fields[1] != "blob" {
		return Blob{}
	}
	return Blob{OID: fields[2], Mode: fields[0], Content: hostedGit(t, mirror, "cat-file", "blob", fields[2])}
}

// newParityFixture is a pull request with text, blank-line, binary, renamed,
// deleted, added, non-ASCII and submodule-pointer changes.
func newParityFixture(t *testing.T) fixture {
	t.Helper()
	requireGit(t)
	dir := initRepo(t)
	blank := "one\n\ntwo\nthree\n\nfour\n"
	write(t, dir, "a_first.txt", lines("first", 8), 0o644)
	write(t, dir, "blank.txt", blank, 0o644)
	write(t, dir, "rename_edit.txt", lines("rename edit", 12), 0o644)
	write(t, dir, "bin.dat", "before\x00\x01\x02", 0o644)
	write(t, dir, "del.txt", lines("del", 3), 0o644)
	write(t, dir, "z_last.txt", lines("last", 8), 0o644)
	write(t, dir, "typed.txt", "regular file\n", 0o644)
	git(t, dir, "add", "-A")
	git(t, dir, "update-index", "--add", "--cacheinfo", "160000,"+strings.Repeat("1", 40)+",sub")
	git(t, dir, "commit", "-q", "-m", "base")
	base := git(t, dir, "rev-parse", "HEAD")

	git(t, dir, "checkout", "-q", "-b", "feature")
	write(t, dir, "a_first.txt", strings.Replace(lines("first", 8), "first line 2\n", "first line two\n", 1), 0o644)
	write(t, dir, "blank.txt", strings.Replace(blank, "two\n", "TWO\n", 1), 0o644)
	git(t, dir, "mv", "rename_edit.txt", "renamed_edit.txt")
	write(t, dir, "renamed_edit.txt", strings.Replace(lines("rename edit", 12), "rename edit line 3\n", "changed\n", 1), 0o644)
	write(t, dir, "bin.dat", "after\x00\x03\x04\x05", 0o644)
	git(t, dir, "rm", "-q", "del.txt")
	write(t, dir, "z_last.txt", lines("last", 8)+"appended\n", 0o644)
	write(t, dir, "ünïcode.txt", "new\n", 0o644)
	git(t, dir, "add", "-A")
	git(t, dir, "update-index", "--add", "--cacheinfo", "160000,"+strings.Repeat("2", 40)+",sub")
	link := git(t, dir, "hash-object", "-w", "ünïcode.txt")
	git(t, dir, "update-index", "--add", "--cacheinfo", "120000,"+link+",typed.txt")
	git(t, dir, "commit", "-q", "-m", "head")
	head := git(t, dir, "rev-parse", "HEAD")
	// The symlink exists only in the index, so reset the working file to it.
	git(t, dir, "reset", "-q", "--hard")
	git(t, dir, "checkout", "-q", "main")
	return fixture{dir: dir, base: base, head: head}
}

// TestHostedParity runs the hosted service's git invocations in a bare
// mirror and gitlocal in a clone whose configuration, attributes,
// .gitmodules, replace refs and environment would each change git's output
// if they reached a read, and requires identical results.
func TestHostedParity(t *testing.T) {
	f := newParityFixture(t)
	mirror := hostedMirror(t, f.dir)
	want := hostedCompare(t, mirror, f.base, f.head)
	wantFull := hostedGit(t, mirror, "diff", "--no-ext-diff", "--no-textconv", "--full-index", "-M", "-l1000", f.base, f.head)

	orderFile := filepath.Join(t.TempDir(), "order")
	write(t, filepath.Dir(orderFile), "order", "z_last.txt\nsub\nrenamed_edit.txt\n*\n", 0o644)
	for _, kv := range [][2]string{
		{"diff.orderFile", orderFile},
		{"diff.submodule", "log"},
		{"diff.ignoreSubmodules", "all"},
		{"core.bigFileThreshold", "1"},
		{"diff.suppressBlankEmpty", "true"},
		{"diff.noprefix", "true"},
		{"diff.mnemonicPrefix", "true"},
		{"diff.context", "9"},
		{"diff.algorithm", "patience"},
		{"diff.renames", "false"},
		{"color.ui", "always"},
		{"core.quotePath", "false"},
		{"diff.external", "false"},
	} {
		git(t, f.dir, "config", kv[0], kv[1])
	}
	write(t, filepath.Join(f.dir, ".git", "info"), "attributes", "* binary\n", 0o644)
	write(t, f.dir, ".gitattributes", "*.txt -diff\n", 0o644)
	write(t, f.dir, ".gitmodules", "[submodule \"sub\"]\n\tpath = sub\n\turl = ./sub\n\tignore = all\n", 0o644)
	replacement := git(t, f.dir, "hash-object", "-w", "--stdin")
	git(t, f.dir, "replace", git(t, f.dir, "rev-parse", f.head+":a_first.txt"), replacement)
	t.Setenv("GIT_LITERAL_PATHSPECS", "1")
	t.Setenv("GIT_DIFF_OPTS", "--unified=9")

	// The hostile settings do change git's output in the clone itself.
	cmd := exec.Command("git", "diff", "--no-ext-diff", "--no-textconv", "--full-index", "-M", "-l1000", f.base, f.head)
	cmd.Dir = f.dir
	if inClone, _ := cmd.Output(); bytes.Equal(inClone, wantFull) {
		t.Fatal("the clone's configuration does not change its own diff; the test proves nothing")
	}

	r := open(t, f.dir, Options{})
	ctx := context.Background()
	got, err := r.Compare(ctx, f.base, f.head)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("comparison differs from the hosted one:\n got %s\nwant %s", describe(got), describe(want))
	}
	var sawSubmodule bool
	for _, file := range got.Files {
		sawSubmodule = sawSubmodule || file.NewMode == "160000"
		paths := bundle.ChangePaths(file)
		patch, err := r.TextPatch(ctx, got.BaseOID, got.HeadOID, paths...)
		if err != nil {
			t.Fatalf("%v: %v", paths, err)
		}
		if wantPatch := hostedTextPatch(t, mirror, want.BaseOID, want.HeadOID, paths); !bytes.Equal(patch, wantPatch) || len(patch) == 0 {
			t.Errorf("%v patch differs:\n got %q\nwant %q", paths, patch, wantPatch)
		}
		if file.NewPath == "" || file.NewMode == "160000" {
			continue
		}
		blob, err := r.ReadBlob(ctx, f.head, file.NewPath)
		if err != nil {
			t.Fatalf("%s: %v", file.NewPath, err)
		}
		if wantBlob := hostedBlob(t, mirror, f.head, file.NewPath); !reflect.DeepEqual(blob, wantBlob) {
			t.Errorf("%s blob = %+v, want %+v", file.NewPath, blob, wantBlob)
		}
	}
	if !sawSubmodule {
		t.Error("the comparison has no submodule-pointer change")
	}
}

func describe(c bundle.Comparison) string {
	var b strings.Builder
	b.WriteString(c.BaseOID + ".." + c.HeadOID + "\n")
	for _, f := range c.Files {
		add, del := "-", "-"
		if f.Additions != nil {
			add = strings.Repeat("+", int(*f.Additions))
		}
		if f.Deletions != nil {
			del = strings.Repeat("-", int(*f.Deletions))
		}
		b.WriteString("  " + f.Status + " " + f.OldPath + " -> " + f.NewPath + " " + add + "/" + del + " " + f.PatchHash + "\n")
	}
	return b.String()
}

// TestOpenFindsTheObjects covers clones whose objects are not in
// .git/objects: a linked worktree, a clone borrowing objects through
// alternates, and a SHA-256 repository.
func TestOpenFindsTheObjects(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, f fixture) string
	}{
		{"linked worktree", func(t *testing.T, f fixture) string {
			dir := filepath.Join(t.TempDir(), "linked")
			git(t, f.dir, "worktree", "add", "-q", "--detach", dir, f.base)
			return dir
		}},
		{"alternates chain", func(t *testing.T, f fixture) string {
			first := filepath.Join(t.TempDir(), "first")
			git(t, f.dir, "clone", "-q", "--shared", "--no-checkout", f.dir, first)
			second := filepath.Join(t.TempDir(), "second")
			git(t, first, "clone", "-q", "--shared", first, second)
			return second
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t)
			dir := tt.setup(t, f)
			cmp, err := open(t, dir, Options{}).Compare(context.Background(), f.base, f.head)
			if err != nil || len(cmp.Files) != 7 {
				t.Fatalf("comparison = %+v, %v", cmp, err)
			}
		})
	}
	t.Run("sha256", func(t *testing.T) {
		requireGit(t)
		dir := t.TempDir()
		if out, err := exec.Command("git", "init", "-q", "--object-format=sha256", dir).CombinedOutput(); err != nil {
			t.Skipf("git cannot create a SHA-256 repository: %s", out)
		}
		write(t, dir, "a.txt", "a\n", 0o644)
		base := commit(t, dir, "base")
		write(t, dir, "a.txt", "b\n", 0o644)
		head := commit(t, dir, "head")
		r := open(t, dir, Options{})
		cmp, err := r.Compare(context.Background(), base, head)
		if err != nil || len(cmp.Files) != 1 || len(cmp.Files[0].NewOID) != 64 {
			t.Fatalf("comparison = %+v, %v", cmp, err)
		}
		if blob, err := r.ReadBlob(context.Background(), head, "a.txt"); err != nil || string(blob.Content) != "b\n" {
			t.Fatalf("blob = %+v, %v", blob, err)
		}
	})
}

// TestPartialCloneIsReportedNotFetched: a blobless clone lacks the pull
// request's blobs. Reads report ErrPartialClone and fetch nothing.
func TestPartialCloneIsReportedNotFetched(t *testing.T) {
	tests := []struct {
		name string
		// dir returns the repository to open, given the partial clone.
		dir func(t *testing.T, partial string) string
	}{
		{"partial clone", func(t *testing.T, partial string) string { return partial }},
		{"clone borrowing a partial clone's objects", func(t *testing.T, partial string) string {
			shared := filepath.Join(t.TempDir(), "shared")
			git(t, partial, "clone", "-q", "--shared", "--no-checkout", partial, shared)
			return shared
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t)
			git(t, f.dir, "config", "uploadpack.allowFilter", "true")
			partial := filepath.Join(t.TempDir(), "partial")
			git(t, f.dir, "clone", "-q", "--filter=blob:none", "file://"+filepath.ToSlash(f.dir), partial)
			git(t, partial, "fetch", "-q", "origin", "feature")
			dir := tt.dir(t, partial)
			// Any fetch from here on fails.
			if err := os.Rename(f.dir, f.dir+"-gone"); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Rename(f.dir+"-gone", f.dir) })
			objects := func() string {
				return tree(t, filepath.Join(partial, ".git", "objects")) + tree(t, filepath.Join(dir, ".git", "objects"))
			}
			before := objects()

			r := open(t, dir, Options{})
			ctx := context.Background()
			if _, err := r.Compare(ctx, f.base, f.head); !errors.Is(err, ErrPartialClone) {
				t.Fatalf("Compare err = %v, want ErrPartialClone", err)
			}
			if _, err := r.ReadBlob(ctx, f.head, "new.txt"); !errors.Is(err, ErrPartialClone) {
				t.Fatalf("ReadBlob err = %v, want ErrPartialClone", err)
			}
			if _, err := r.TextPatch(ctx, f.base, f.head, "new.txt"); !errors.Is(err, ErrPartialClone) || errors.Is(err, bundle.ErrPatchUnreadable) {
				t.Fatalf("TextPatch err = %v, want ErrPartialClone", err)
			}
			if rc, err := r.ExportCommit(ctx, f.head); err == nil {
				_, err = io.Copy(io.Discard, rc)
				_ = rc.Close()
				if !errors.Is(err, ErrPartialClone) {
					t.Fatalf("ExportCommit read err = %v, want ErrPartialClone", err)
				}
			} else if !errors.Is(err, ErrPartialClone) {
				t.Fatalf("ExportCommit err = %v, want ErrPartialClone", err)
			}
			// A blob the clone does have still reads.
			if blob, err := r.ReadBlob(ctx, f.base, "keep.txt"); err != nil || string(blob.Content) != "keep\n" {
				t.Fatalf("present blob = %+v, %v", blob, err)
			}
			if objects() != before {
				t.Fatal("objects were fetched")
			}
		})
	}
	t.Run("promisor configuration alone", func(t *testing.T) {
		requireGit(t)
		dir := initRepo(t)
		if hasPromisorConfig(context.Background(), dir, time.Minute) {
			t.Fatal("a plain repository reads as partial")
		}
		git(t, dir, "config", "remote.origin.promisor", "true")
		if !hasPromisorConfig(context.Background(), dir, time.Minute) {
			t.Fatal("remote.origin.promisor not detected")
		}
	})
}

// TestExportMatchesHostedArchive: for a commit whose .gitattributes asks for
// conversions (CRLF line endings, ident expansion, a working-tree encoding)
// and for export-ignore and export-subst, the export is byte for
// byte what the hosted mirror's git archive --worktree-attributes produces
// in its bare mirror, for the whole tree and for single paths.
func TestExportMatchesHostedArchive(t *testing.T) {
	f := newFixture(t)
	git(t, f.dir, "checkout", "-q", "feature")
	write(t, f.dir, "ident.txt", "$Id$\nline\n", 0o644)
	write(t, f.dir, "utf16.txt", "plain\n", 0o644)
	write(t, f.dir, "ignored.md", "ignored\n", 0o644)
	write(t, f.dir, "subst.md", "$Format:%H$\n", 0o644)
	write(t, f.dir, ".gitmodules", "[submodule \"sub\"]\n\tpath = sub\n\turl = https://example.com/sub.git\n", 0o644)
	commit(t, f.dir, "files")
	// The attributes come last, so adding the files converts nothing.
	write(t, f.dir, ".gitattributes", "*.txt text eol=crlf ident\nutf16.txt working-tree-encoding=UTF-16LE\nignored.md export-ignore\nsubst.md export-subst\n", 0o644)
	head := commit(t, f.dir, "attributes")
	git(t, f.dir, "checkout", "-q", "main")
	mirror := hostedMirror(t, f.dir)
	// The clone's own attributes and configuration do not reach the export.
	write(t, filepath.Join(f.dir, ".git", "info"), "attributes", "* export-ignore text eol=crlf working-tree-encoding=UTF-32\n", 0o644)
	git(t, f.dir, "config", "core.autocrlf", "true")
	git(t, f.dir, "config", "filter.x.smudge", "false")
	r := open(t, f.dir, Options{})
	ctx := context.Background()

	check := func(name string, rc io.ReadCloser, err error, want []byte) {
		t.Helper()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		got, err := io.ReadAll(rc)
		if err != nil || rc.Close() != nil {
			t.Fatalf("%s: read: %v", name, err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("%s: export differs from the hosted archive (%d bytes, want %d)", name, len(got), len(want))
		}
	}
	whole := hostedGit(t, mirror, "archive", "--format=tar", "--worktree-attributes", head)
	rc, err := r.ExportCommit(ctx, head)
	check("whole tree", rc, err, whole)
	// Whatever the hosted archive does with these attributes, the export
	// does too. Record what that is, so a change in git shows up here.
	files := tarFiles(t, bytes.NewReader(whole))
	if files["ident.txt"] != "$Id$\nline\n" || files["ignored.md"] == "" || files["subst.md"] != "$Format:%H$\n" {
		t.Errorf("hosted archive no longer leaves eol, ident, export-ignore and export-subst alone: %q", files)
	}
	t.Logf("hosted utf16.txt (committed as %q) exports as %q", "plain\n", files["utf16.txt"])
	for _, path := range []string{"ident.txt", "utf16.txt", "ignored.md", "subst.md", ".gitmodules", ".gitattributes", "dir"} {
		want := hostedGit(t, mirror, "archive", "--format=tar", "--worktree-attributes", head, "--", ":(literal)"+path)
		rc, err := r.ExportCommitPath(ctx, head, path)
		check(path, rc, err, want)
	}
}
