package bundle

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func patchOf(path string, content []byte) map[string][]byte { return map[string][]byte{path: content} }

func fetcher(patches map[string][]byte) PatchFetcher {
	return func(paths ...string) ([]byte, error) { return patches[paths[len(paths)-1]], nil }
}

const hunkA = "@@ -10,3 +10,4 @@ func foo() {\n context\n-old line\n+new line\n+another line\n"
const hunkB = "@@ -50,2 +51,2 @@ func bar() {\n-old bar\n+new bar\n"

func TestBuildDiffIndexCoversEveryFileOnce(t *testing.T) {
	comparison := Comparison{Files: []FileChange{
		{NewPath: "a.go", Status: "modified"},
		{NewPath: "b.go", Status: "modified"},
	}}
	hunks, err := BuildDiffIndex(comparison, fetcher(map[string][]byte{
		"a.go": []byte(hunkA),
		"b.go": []byte(hunkB),
	}), RoleForPath)
	if err != nil {
		t.Fatalf("BuildDiffIndex: %v", err)
	}
	if len(hunks) != 2 {
		t.Fatalf("want 2 hunks, got %d", len(hunks))
	}
	paths := map[string]bool{}
	for _, h := range hunks {
		paths[h.Path] = true
	}
	if !paths["a.go"] || !paths["b.go"] {
		t.Fatalf("expected both files covered, got %+v", hunks)
	}
}

func TestHunkIDSurvivesContextShiftAndRebase(t *testing.T) {
	// Same logical change (the -old/+new/+another lines), but the hunk header
	// line numbers differ (a "rebase") and one extra unrelated context line
	// is prepended (a "context shift"): the ID must be identical because it
	// hashes only the changed ("+"/"-") lines, not headers or context.
	original := "@@ -10,3 +10,4 @@ func foo() {\n context\n-old line\n+new line\n+another line\n"
	rebased := "@@ -87,4 +90,5 @@ func foo() {\n unrelated context line\n context\n-old line\n+new line\n+another line\n"

	comparison := Comparison{Files: []FileChange{{NewPath: "a.go", Status: "modified"}}}
	before, err := BuildDiffIndex(comparison, fetcher(patchOf("a.go", []byte(original))), RoleForPath)
	if err != nil {
		t.Fatal(err)
	}
	after, err := BuildDiffIndex(comparison, fetcher(patchOf("a.go", []byte(rebased))), RoleForPath)
	if err != nil {
		t.Fatal(err)
	}
	if before[0].ID != after[0].ID {
		t.Fatalf("hunk ID changed across context shift/rebase: %q vs %q", before[0].ID, after[0].ID)
	}
	if before[0].ID == "" {
		t.Fatal("hunk ID must not be empty")
	}
}

func TestHunkIDDiffersForDifferentFiles(t *testing.T) {
	comparison := Comparison{Files: []FileChange{
		{NewPath: "a.go", Status: "modified"}, {NewPath: "b.go", Status: "modified"},
	}}
	hunks, err := BuildDiffIndex(comparison, fetcher(map[string][]byte{
		"a.go": []byte(hunkA), "b.go": []byte(hunkA), // identical content, different path
	}), RoleForPath)
	if err != nil {
		t.Fatal(err)
	}
	if hunks[0].ID == hunks[1].ID {
		t.Fatalf("hunks in different files must not share an ID: %q", hunks[0].ID)
	}
}

func TestRepeatedHunkContentGetsOrdinalSuffix(t *testing.T) {
	patch := hunkA + hunkA // two byte-identical hunks in the same file
	comparison := Comparison{Files: []FileChange{{NewPath: "a.go", Status: "modified"}}}
	hunks, err := BuildDiffIndex(comparison, fetcher(patchOf("a.go", []byte(patch))), RoleForPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(hunks) != 2 {
		t.Fatalf("want 2 hunks, got %d", len(hunks))
	}
	if hunks[0].ID == hunks[1].ID {
		t.Fatalf("repeated content must get an ordinal suffix, got equal IDs %q", hunks[0].ID)
	}
	if !strings.HasSuffix(hunks[1].ID, "-2") {
		t.Fatalf("expected second occurrence to carry -2 suffix, got %q", hunks[1].ID)
	}
	base := strings.TrimSuffix(hunks[1].ID, "-2")
	if hunks[0].ID != base {
		t.Fatalf("first occurrence should be the unsuffixed base %q, got %q", base, hunks[0].ID)
	}
}

func TestBinaryChangeYieldsOneTextlessHunk(t *testing.T) {
	comparison := Comparison{Files: []FileChange{{NewPath: "image.png", Status: "modified"}}}
	hunks, err := BuildDiffIndex(comparison, fetcher(patchOf("image.png", []byte("Binary files a/image.png and b/image.png differ\n"))), RoleForPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(hunks) != 1 {
		t.Fatalf("want exactly one textless hunk, got %d", len(hunks))
	}
	if !hunks[0].Textless || hunks[0].Patch != "" {
		t.Fatalf("expected a textless hunk with no patch, got %+v", hunks[0])
	}
}

func TestBinaryPatchFormatsYieldOneTextlessHunk(t *testing.T) {
	patches := map[string]string{
		"plain":  "diff --git a/x.bin b/x.bin\nindex 1a23e4b..2e56c62 100644\nBinary files a/x.bin and b/x.bin differ\n",
		"binary": "diff --git a/x.bin b/x.bin\nindex 1a23e4b..2e56c62 100644\nGIT binary patch\nliteral 4\nLcmYdfNa6wj0#*Rd\n\nliteral 4\nLcmYdhNZ|qi0%8Et\n\n",
	}
	for name, patch := range patches {
		comparison := Comparison{Files: []FileChange{{NewPath: "x.bin", Status: "modified"}}}
		hunks, err := BuildDiffIndex(comparison, fetcher(patchOf("x.bin", []byte(patch))), RoleForPath)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(hunks) != 1 || !hunks[0].Textless || hunks[0].Patch != "" || hunks[0].TextlessReason != "binary" {
			t.Fatalf("%s: want one textless hunk with reason binary, got %+v", name, hunks)
		}
	}
}

// TestTextPatchQuotingBinaryMarkersKeepsHunks covers a text file whose added
// lines quote git's binary markers: only header lines classify a patch.
func TestTextPatchQuotingBinaryMarkersKeepsHunks(t *testing.T) {
	patch := "diff --git a/g.go b/g.go\nnew file mode 100644\n--- /dev/null\n+++ b/g.go\n" +
		"@@ -0,0 +1,3 @@\n" +
		"+// git prints \"Binary files a/x and b/y differ\" for binaries.\n" +
		"+var m = \"GIT binary patch\"\n" +
		"+Binary files are skipped\n"
	comparison := Comparison{Files: []FileChange{{NewPath: "g.go", Status: "added"}}}
	hunks, err := BuildDiffIndex(comparison, fetcher(patchOf("g.go", []byte(patch))), RoleForPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(hunks) != 1 {
		t.Fatalf("want one hunk, got %d", len(hunks))
	}
	h := hunks[0]
	if h.Textless || h.TextlessReason != "" || h.Patch == "" {
		t.Fatalf("text patch must keep its hunk text, got %+v", h)
	}
	if h.StartLine != 1 || h.LineCount != 3 || h.Additions != 3 || h.Deletions != 0 {
		t.Fatalf("wrong ranges, got %+v", h)
	}
}

// TestPatchLineOverScannerLimitYieldsOneTextlessHunk covers a patch line
// past the scanner's 16MiB buffer (the "textless hunk" contract
// for content that cannot be read): scanner.Scan stops with
// bufio.ErrTooLong partway through, and the fix must fail the whole file
// closed as textless rather than silently keeping the partial hunks already
// parsed before the failure (which would carry wrong addition/deletion
// counts and missing content, not a complete patch).
func TestPatchLineOverScannerLimitYieldsOneTextlessHunk(t *testing.T) {
	giant := strings.Repeat("x", 17<<20) // 17MiB, past the 16MiB scanner buffer.
	patch := "@@ -1,1 +1,2 @@\n-old line\n+" + giant + "\n+trailing line\n"
	comparison := Comparison{Files: []FileChange{{NewPath: "minified.js", Status: "modified"}}}
	hunks, err := BuildDiffIndex(comparison, fetcher(patchOf("minified.js", []byte(patch))), RoleForPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(hunks) != 1 || !hunks[0].Textless || hunks[0].Patch != "" {
		t.Fatalf("want one textless hunk with no patch, got %+v", hunks)
	}
}

func TestEmptyPatchYieldsOneTextlessHunk(t *testing.T) {
	// A nil/empty patch (e.g. an unreadable file) must still produce exactly
	// one assignable hunk, not zero, so the change stays visible in the
	// review's hunk index.
	comparison := Comparison{Files: []FileChange{{NewPath: "unreadable.bin", Status: "modified"}}}
	hunks, err := BuildDiffIndex(comparison, fetcher(patchOf("unreadable.bin", nil)), RoleForPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(hunks) != 1 || !hunks[0].Textless {
		t.Fatalf("want one textless hunk, got %+v", hunks)
	}
}

// TestUnreadablePatchIsOneTextlessHunk covers one file over the patch size
// limit and one git cannot diff: each is a single textless hunk with a
// truncation note, the other files keep their hunks, and any other fetch
// failure still fails.
func TestUnreadablePatchIsOneTextlessHunk(t *testing.T) {
	comparison := Comparison{Files: []FileChange{
		{NewPath: "package-lock.json", Status: "modified"},
		{NewPath: "broken.go", Status: "modified"},
		{NewPath: "a.go", Status: "modified"},
	}}
	fetch := func(paths ...string) ([]byte, error) {
		switch paths[0] {
		case "package-lock.json":
			return nil, fmt.Errorf("reading file patch: %w", ErrOutputTooLarge)
		case "broken.go":
			return nil, fmt.Errorf("repository service: %w", ErrPatchUnreadable)
		}
		return []byte(hunkA), nil
	}
	hunks, notes, err := buildDiffIndex(comparison, fetch, RoleForPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(hunks) != 3 {
		t.Fatalf("want one hunk per file, got %+v", hunks)
	}
	for i := range 2 {
		if h := hunks[i]; !h.Textless || h.TextlessReason != "binary" || h.Patch != "" || h.ID != hunkID(h.Path, "") {
			t.Fatalf("hunk %d = %+v, want one textless binary hunk", i, h)
		}
	}
	if hunks[2].Textless || hunks[2].Patch == "" {
		t.Fatalf("readable file lost its patch: %+v", hunks[2])
	}
	if len(notes) != 2 || !strings.Contains(notes[0], "1 file(s) over the patch size limit") || !strings.Contains(notes[1], "1 file(s) git could not diff") {
		t.Fatalf("omission notes = %q", notes)
	}

	_, err = BuildDiffIndex(comparison, func(...string) ([]byte, error) { return nil, errors.New("repository service unavailable") }, RoleForPath)
	if err == nil {
		t.Fatal("an unavailable repository service built a diff index")
	}
}

// TestRenamedFileFetchesBothPaths: a single-path pathspec
// hides the rename from git, which then reports the whole file as added.
func TestRenamedFileFetchesBothPaths(t *testing.T) {
	comparison := Comparison{Files: []FileChange{
		{OldPath: "old/a.go", NewPath: "new/a.go", Status: "renamed"},
		{OldPath: "b.go", NewPath: "b.go", Status: "modified"},
	}}
	var calls [][]string
	hunks, err := BuildDiffIndex(comparison, func(paths ...string) ([]byte, error) {
		calls = append(calls, paths)
		return []byte(hunkB), nil
	}, RoleForPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 2 || strings.Join(calls[0], ",") != "old/a.go,new/a.go" || strings.Join(calls[1], ",") != "b.go" {
		t.Fatalf("fetch paths = %q", calls)
	}
	if len(hunks) != 2 || hunks[0].Path != "new/a.go" || hunks[0].OldPath != "old/a.go" || hunks[0].Change != "renamed" || hunks[0].Additions != 1 {
		t.Fatalf("renamed hunk = %+v", hunks)
	}
}

func TestPureRenameYieldsOneTextlessRenameHunk(t *testing.T) {
	comparison := Comparison{Files: []FileChange{{OldPath: "a.go", NewPath: "b.go", Status: "renamed"}}}
	patch := "diff --git a/a.go b/b.go\nsimilarity index 100%\nrename from a.go\nrename to b.go\n"
	hunks, err := BuildDiffIndex(comparison, fetcher(patchOf("b.go", []byte(patch))), RoleForPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(hunks) != 1 || !hunks[0].Textless || hunks[0].TextlessReason != "rename_only" || hunks[0].OldPath != "a.go" {
		t.Fatalf("want one textless rename hunk, got %+v", hunks)
	}
}

// A rename plus a mode change with no text edits must not pass for a pure
// rename.
func TestRenameWithModeChangeIsNotRenameOnly(t *testing.T) {
	comparison := Comparison{Files: []FileChange{{OldPath: "a.sh", NewPath: "b.sh", Status: "renamed"}}}
	patch := "diff --git a/a.sh b/b.sh\nold mode 100644\nnew mode 100755\nsimilarity index 100%\nrename from a.sh\nrename to b.sh\n"
	hunks, err := BuildDiffIndex(comparison, fetcher(patchOf("b.sh", []byte(patch))), RoleForPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(hunks) != 1 || !hunks[0].Textless || hunks[0].TextlessReason != "mode_change" || hunks[0].OldPath != "a.sh" {
		t.Fatalf("want one textless mode_change hunk, got %+v", hunks)
	}
}

func TestUnreadableRenameStaysBinary(t *testing.T) {
	comparison := Comparison{Files: []FileChange{{OldPath: "a.go", NewPath: "b.go", Status: "renamed"}}}
	hunks, err := BuildDiffIndex(comparison, fetcher(nil), RoleForPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(hunks) != 1 || hunks[0].TextlessReason != "binary" {
		t.Fatalf("want one binary hunk, got %+v", hunks)
	}
}

func TestDiffURLIsStableForPath(t *testing.T) {
	url1 := diffURL("internal/foo.go", 42)
	url2 := diffURL("internal/foo.go", 42)
	if url1 != url2 {
		t.Fatalf("diffURL must be deterministic: %q vs %q", url1, url2)
	}
	if !strings.HasPrefix(url1, "diff-") || !strings.HasSuffix(url1, "R42") {
		t.Fatalf("unexpected diff URL shape: %q", url1)
	}
}

func TestRoleForPath(t *testing.T) {
	cases := []struct {
		path string
		want string
	}{
		{"internal/review/builder.go", RoleCode},
		{"internal/review/builder_test.go", RoleTest},
		{"ui/src/components/Foo.test.tsx", RoleTest},
		{"db/migrations/000035_add_evidence.up.sql", RoleMigration},
		{"internal/db/sqlc/reviews.sql.go", RoleGenerated},
		{"go.sum", RoleGenerated},
		{"docs/architecture.md", RoleDocs},
		{"README.md", RoleDocs},
		{"config.toml", RoleConfig},
		{".github/workflows/ci.yml", RoleConfig},
		{"vendor/github.com/foo/bar.go", RoleVendored},
		{"ui/node_modules/react/index.js", RoleVendored},
	}
	for _, c := range cases {
		if got := RoleForPath(c.path); got != c.want {
			t.Errorf("RoleForPath(%q) = %q, want %q", c.path, got, c.want)
		}
	}
}

func TestTriplePrefixSourceLinesCountAndChangeIdentity(t *testing.T) {
	comparison := Comparison{Files: []FileChange{{NewPath: "a.go", Status: "modified"}}}
	patch := "--- a/a.go\n+++ b/a.go\n@@ -1 +1 @@\n---counter;\n+++counter;\n"
	before, err := BuildDiffIndex(comparison, fetcher(patchOf("a.go", []byte(patch))), RoleForPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(before) != 1 || before[0].Additions != 1 || before[0].Deletions != 1 {
		t.Fatalf("incorrect triple-prefix counts: %+v", before)
	}
	after, err := BuildDiffIndex(comparison, fetcher(patchOf("a.go", []byte(strings.Replace(patch, "+++counter;", "+++other;", 1)))), RoleForPath)
	if err != nil {
		t.Fatal(err)
	}
	if before[0].ID == after[0].ID {
		t.Fatal("different executable changes have the same hunk ID")
	}
}

func TestDiffLimitsFailNamingTheCheckoutBudget(t *testing.T) {
	comparison := Comparison{Files: []FileChange{
		{NewPath: "a.go", Status: "modified"},
		{NewPath: "b.go", Status: "modified"},
	}}
	fetched := 0
	fetch := func(paths ...string) ([]byte, error) {
		fetched++
		return []byte(hunkA), nil
	}
	if _, _, err := BuildDiffIndexWithin(comparison, fetch, RoleForPath, DiffLimits{MaxFiles: 1}); err == nil || err.Error() != BudgetExceededReason("diff files") || fetched != 0 {
		t.Fatalf("file limit: %v after %d fetches", err, fetched)
	}
	if _, _, err := BuildDiffIndexWithin(comparison, fetch, RoleForPath, DiffLimits{MaxFiles: 2, MaxPatchBytes: int64(len(hunkA)) + 1}); err == nil || err.Error() != BudgetExceededReason("diff bytes") || fetched != 2 {
		t.Fatalf("byte limit: %v after %d fetches", err, fetched)
	}
	if hunks, _, err := BuildDiffIndexWithin(comparison, fetch, RoleForPath, DiffLimits{MaxFiles: 2, MaxPatchBytes: 2 * int64(len(hunkA))}); err != nil || len(hunks) != 2 {
		t.Fatalf("within limits: %v %+v", err, hunks)
	}
}

// Parsing keeps a bounded margin past the hunk cap, so redaction reads what
// the cap runs through and the later cap and its note behave as on the full
// text, while IDs and counts still cover every line.
func TestParsingKeepsAMarginPastTheHunkCap(t *testing.T) {
	var patch strings.Builder
	patch.WriteString("@@ -1,5000 +1,5000 @@\n")
	for i := range 5000 {
		fmt.Fprintf(&patch, "-old %d\n+new %d\n", i, i)
	}
	comparison := Comparison{Files: []FileChange{{NewPath: "a.go", Status: "modified"}}}
	full, err := BuildDiffIndex(comparison, fetcher(patchOf("a.go", []byte(patch.String()))), RoleForPath)
	if err != nil {
		t.Fatal(err)
	}
	capped, _, err := BuildDiffIndexWithin(comparison, fetcher(patchOf("a.go", []byte(patch.String()))), RoleForPath, DiffLimits{MaxHunkLines: 3})
	if err != nil {
		t.Fatal(err)
	}
	if len(full) != 1 || len(capped) != 1 {
		t.Fatalf("hunks: %d %d", len(full), len(capped))
	}
	kept := capped[0].Patch
	head, _ := capPatchLines(kept, 3)
	if !strings.HasPrefix(full[0].Patch, kept) || len(kept) <= len(head)+redactionMargin-16 || len(kept) > len(head)+1+redactionMargin {
		t.Fatalf("kept %d bytes, %d of them past the cap; want a prefix of the patch with a %d-byte margin", len(kept), len(kept)-len(head), redactionMargin)
	}
	fullCut, _ := capPatchLines(full[0].Patch, 3)
	cappedCut, cut := capPatchLines(capped[0].Patch, 3)
	if !cut || fullCut != cappedCut {
		t.Fatalf("cap differs: %q vs %q", fullCut, cappedCut)
	}
	if capped[0].ID != full[0].ID || capped[0].Additions != 5000 || capped[0].Deletions != 5000 || capped[0].LineCount != full[0].LineCount {
		t.Fatalf("capped hunk lost its identity or counts: %+v", capped[0])
	}
	changed := strings.TrimPrefix(patch.String(), "@@ -1,5000 +1,5000 @@\n")
	digest := sha256.Sum256([]byte("a.go\x00" + changed))
	if want := "h_" + hex.EncodeToString(digest[:6]); full[0].ID != want {
		t.Fatalf("hunk ID %s, want %s", full[0].ID, want)
	}

	// One long line past the cap is cut to the margin; the cap still cuts.
	long := "@@ -1,2 +1,3 @@\n+a\n+" + strings.Repeat("x", 3*redactionMargin) + "\n+b\n"
	capped, _, err = BuildDiffIndexWithin(comparison, fetcher(patchOf("a.go", []byte(long))), RoleForPath, DiffLimits{MaxHunkLines: 2})
	if err != nil {
		t.Fatal(err)
	}
	if want := "@@ -1,2 +1,3 @@\n+a\n+" + strings.Repeat("x", redactionMargin-1); capped[0].Patch != want || capped[0].Additions != 3 {
		t.Fatalf("long line kept as %d bytes, want %d", len(capped[0].Patch), len(want))
	}
	if cut, ok := capPatchLines(capped[0].Patch, 2); !ok || cut != "@@ -1,2 +1,3 @@\n+a" {
		t.Fatalf("cap = %q, %t", cut, ok)
	}
}

// A secret the hunk line cap runs through is masked in the kept lines
// exactly as when the whole patch is redacted: parsing keeps a margin past
// the cap, Redact reads it and CapPatches cuts after.
func TestRedactionReadsPastTheHunkLineCap(t *testing.T) {
	redactor, err := NewRedactor()
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	const limit = 20
	forms := map[string][]string{
		"pem":           syntheticKeyLines(),
		"encrypted pem": syntheticEncryptedKeyLines(),
	}
	for name, key := range forms {
		for _, marker := range []string{"+", " ", "-"} {
			// start is the key's first line in the hunk, the "@@" line being
			// line 1: from the whole key kept to only its header.
			for start := limit - len(key); start <= limit; start++ {
				var patch strings.Builder
				patch.WriteString("@@ -1,60 +1,60 @@\n")
				for i := 2; i < start; i++ {
					fmt.Fprintf(&patch, "%sline %d\n", marker, i)
				}
				for _, line := range key {
					patch.WriteString(marker + line + "\n")
				}
				for i := range 20 {
					fmt.Fprintf(&patch, "%sline after %d\n", marker, i)
				}
				comparison := Comparison{Files: []FileChange{{NewPath: "key.pem", Status: "modified"}}}
				hunks, _, err := BuildDiffIndexWithin(comparison, fetcher(patchOf("key.pem", []byte(patch.String()))), RoleForPath, DiffLimits{MaxHunkLines: limit})
				if err != nil {
					t.Fatal(err)
				}
				b := &Bundle{Diff: hunks}
				ApplyCaps(b, Caps{MaxHunkPatchLines: limit})
				if err := Redact(ctx, b, redactor); err != nil {
					t.Fatal(err)
				}
				CapPatches(b, Caps{MaxHunkPatchLines: limit})

				whole := &Bundle{Diff: []Hunk{{Patch: strings.TrimRight(patch.String(), "\n")}}}
				if err := Redact(ctx, whole, redactor); err != nil {
					t.Fatal(err)
				}
				want, _ := capPatchLines(whole.Diff[0].Patch, limit)
				got := b.Diff[0].Patch
				where := fmt.Sprintf("%s %q from line %d", name, marker, start)
				if got != want {
					t.Fatalf("%s: capped redaction differs from the whole patch's:\n%s\nwant\n%s", where, got, want)
				}
				for _, line := range key {
					if strings.Contains(got, line) && strings.ContainsAny(line, "abcdefghijklmnopqrstuvwxyz0123456789") {
						t.Fatalf("%s keeps key line %q", where, line)
					}
				}
				if strings.Contains(got, "PRIVATE KEY") || strings.Contains(got, "ENCRYPTED") {
					t.Fatalf("%s: the kept part of the key block is not masked:\n%s", where, got)
				}
				if n := strings.Count(got, "\n") + 1; n != limit || len(b.Truncated) != 1 {
					t.Fatalf("%s: kept %d lines, notes %q", where, n, b.Truncated)
				}
			}
		}
	}
}

// syntheticEncryptedKeyLines is syntheticKeyLines with the encapsulated
// headers of an encrypted PEM key, which hold no long base64 run of their
// own.
func syntheticEncryptedKeyLines() []string {
	key := syntheticKeyLines()
	headers := []string{"Proc-Type: 4,ENCRYPTED", "DEK-Info: AES-128-CBC," + strings.Repeat("A1B2", 8), ""}
	return append(append([]string{key[0]}, headers...), key[1:]...)
}

func TestTextlessHunkIDHashesThePathAlone(t *testing.T) {
	comparison := Comparison{Files: []FileChange{{NewPath: "image.png", Status: "modified"}}}
	hunks, err := BuildDiffIndex(comparison, fetcher(nil), RoleForPath)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte("image.png\x00"))
	if want := "h_" + hex.EncodeToString(digest[:6]); len(hunks) != 1 || hunks[0].ID != want {
		t.Fatalf("textless hunk = %+v, want ID %s", hunks, want)
	}
}
