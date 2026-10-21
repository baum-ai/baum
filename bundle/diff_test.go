package bundle

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
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
		{"service/internal/db/sqlc/queries/foo.sql", RoleCode},
		{"service/internal/db/sqlc/queries/foo.sql.go", RoleGenerated},
		{"service/internal/db/sqlc/schema.sql", RoleCode},
		{"service/internal/db/sqlc/querier.go", RoleGenerated},
		{"service/internal/db/sqlc/db.go", RoleGenerated},
		{"service/internal/db/sqlc/models.go", RoleGenerated},
		{"service/internal/db/sqlc/batch.go", RoleGenerated},
		{"service/internal/db/sqlc/copyfrom.go", RoleGenerated},
		{"sqlc/querier.go", RoleGenerated},
		{"sqlc/custom_models.go", RoleGenerated},
		{"sqlc/queries_gen.go", RoleGenerated},
		{"sqlc/custom_query.go", RoleGenerated},
		{"Service/Internal/DB/SQLC/Models.go", RoleGenerated},
		{"service/internal/db/sqlc/queries/db.go", RoleCode},
		{"internal/app/models.go", RoleCode},
		{"internal/app/db.go", RoleCode},
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
// the cap, Redact reads it and Fit cuts after.
func TestRedactionReadsPastTheHunkLineCap(t *testing.T) {
	redactor, err := NewRedactor()
	if err != nil {
		t.Fatal(err)
	}
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
				got, want, notes := cutAfterRedaction(t, redactor, patch.String(), limit)
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
				if n := strings.Count(got, "\n") + 1; n != limit || len(notes) != 1 {
					t.Fatalf("%s: kept %d lines, notes %q", where, n, notes)
				}
			}
		}
	}
}

// cutAfterRedaction runs patch, one file's diff of one hunk, through the
// assembly order and returns the kept patch and notes, with want: the whole
// patch redacted, then capped.
func cutAfterRedaction(t *testing.T, redactor *Redactor, patch string, limit int) (got, want string, notes []string) {
	t.Helper()
	hunks, wants, b := assembleFilePatch(t, redactor, patch, limit)
	if len(hunks) != 1 || len(wants) != 1 {
		t.Fatalf("want one hunk, got %d and %d", len(hunks), len(wants))
	}
	return hunks[0], wants[0], b.Truncated
}

// assembleFilePatch runs patch, one file's diff, through the assembly order
// (diff index within the line cap, ApplyCaps, Redact, Fit) and returns each
// hunk's kept patch and the bundle, with want: the whole patch redacted as
// one text with its diff syntax kept, split into hunks, then capped. A limit
// of 0 caps nothing.
func assembleFilePatch(t *testing.T, redactor *Redactor, patch string, limit int) (got, want []string, b *Bundle) {
	t.Helper()
	ctx := context.Background()
	comparison := Comparison{Files: []FileChange{{NewPath: "key.pem", Status: "modified"}}}
	caps := Caps{MaxHunkPatchLines: limit}
	hunks, _, err := BuildDiffIndexWithin(comparison, fetcher(patchOf("key.pem", []byte(patch))), RoleForPath, caps.DiffLimits())
	if err != nil {
		t.Fatal(err)
	}
	b = &Bundle{Diff: hunks}
	ApplyCaps(b, caps)
	if err := Redact(ctx, b, redactor); err != nil {
		t.Fatal(err)
	}
	if err := Fit(b, caps); err != nil {
		t.Fatal(err)
	}
	for _, hunk := range b.Diff {
		got = append(got, hunk.Patch)
	}
	// Diff parsing reads lines as bufio.ScanLines does, dropping a CR before
	// each newline.
	text := strings.TrimRight(strings.ReplaceAll(patch, "\r\n", "\n"), "\n")
	redacted, _, err := redactor.RedactString(ctx, text)
	if err != nil {
		t.Fatal(err)
	}
	redacted = keepDiffSyntax(text, redacted)
	for _, hunk := range strings.Split(redacted, "\n@@ -") {
		if !strings.HasPrefix(hunk, "@@ -") {
			hunk = "@@ -" + hunk
		}
		if limit > 0 {
			hunk, _ = capPatchLines(hunk, limit)
		}
		want = append(want, strings.TrimRight(hunk, "\n"))
	}
	return got, want, b
}

// goredactVersion returns the goredact version go.mod pins.
func goredactVersion(t *testing.T) string {
	t.Helper()
	gomod, err := os.ReadFile(filepath.Join("..", "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(gomod), "\n") {
		if fields := strings.Fields(line); len(fields) == 2 && fields[0] == "github.com/baum-ai/goredact" {
			return fields[1]
		}
	}
	t.Fatal("go.mod pins no goredact version")
	return ""
}

// syntheticPuTTYKey returns a PuTTY private key file, one line per
// element, and its private lines: synthetic base64, not key material.
func syntheticPuTTYKey() (lines, private []string) {
	body := keyBody(5)
	return []string{
		"PuTTY-User-Key-File-3: ssh-rsa", "Encryption: none", "Comment: rsa-key-20261020",
		"Public-Lines: 2", body[0], body[1],
		"Private-Lines: 3", body[2], body[3], body[4],
		"Private-MAC: " + strings.Repeat("0a1b2c3d", 8),
	}, body[2:]
}

// A key that runs on from one hunk into the next is redacted with the
// file's whole patch, so the bundle masks it as one text would, in both
// hunks and in the second hunk's header, whose line ranges are put back.
// That holds with the cap cutting a long first hunk short, so the key's
// first lines sit only in the text the cap drops. goredact v0.3.0 ends a key at a
// hunk header even in one text; later versions follow it across, and the
// bundle must then keep no line of it.
func TestRedactionFollowsAKeyAcrossHunks(t *testing.T) {
	redactor, err := NewRedactor()
	if err != nil {
		t.Fatal(err)
	}
	acrossHunks := goredactVersion(t) != "v0.3.0"
	pem := syntheticKeyLines()
	ppk, private := syntheticPuTTYKey()
	keys := []struct {
		name          string
		lines, secret []string
		// split is how many of the key's lines the first hunk holds.
		split int
	}{
		{"pem", pem, pem[1 : len(pem)-1], 4},
		{"putty", ppk, private, 8},
	}
	for _, key := range keys {
		for _, marker := range []string{" ", "+"} {
			for _, heading := range []string{"", "func load() {", key.lines[key.split-1]} {
				// A long first hunk puts the key past the cap and its margin.
				for _, long := range []bool{false, true} {
					for _, limit := range []int{0, 3} {
						where := fmt.Sprintf("%s %q heading %q long %t cap %d", key.name, marker, heading, long, limit)
						second := "@@ -36,7 +36,7 @@"
						if heading != "" {
							second += " " + heading
						}
						context := " context\n"
						if long {
							context = strings.Repeat(" "+strings.Repeat("context ", 8)+"\n", 2*redactionMargin/65)
						}
						patch := "@@ -1,6 +1,6 @@\n" + context + prefixLines(marker, strings.Join(key.lines[:key.split], "\n")) + "\n" +
							second + "\n" + prefixLines(marker, strings.Join(key.lines[key.split:], "\n")) + "\n-after\n+after 2\n"
						got, want, b := assembleFilePatch(t, redactor, patch, limit)
						if strings.Join(got, "\n") != strings.Join(want, "\n") {
							t.Fatalf("%s: the bundle's hunks differ from the whole patch's:\n%s\nwant\n%s", where, strings.Join(got, "\n"), strings.Join(want, "\n"))
						}
						if len(got) != 2 || !strings.HasPrefix(got[0], "@@ -1,6 +1,6 @@\n") || !strings.HasPrefix(got[1], "@@ -36,7 +36,7 @@") {
							t.Fatalf("%s: hunk headers lost their ranges:\n%s", where, strings.Join(got, "\n"))
						}
						if b.Diff[1].StartLine != 36 || b.Diff[1].LineCount != 7 {
							t.Fatalf("%s: second hunk = %+v", where, b.Diff[1])
						}
						if b.MaskedSpanCount != 1 {
							t.Fatalf("%s: masked %d spans, want the one key", where, b.MaskedSpanCount)
						}
						if !acrossHunks {
							continue
						}
						text := strings.Join(got, "\n")
						for _, line := range key.secret {
							if strings.Contains(text, line) {
								t.Fatalf("%s: key line %q survives:\n%s", where, line, text)
							}
						}
						if strings.Contains(text, "PRIVATE KEY") {
							t.Fatalf("%s: the armor survives:\n%s", where, text)
						}
						if header, _, _ := strings.Cut(got[1], "\n"); heading != "" && !strings.HasPrefix(header, "@@ -36,7 +36,7 @@ ") {
							t.Fatalf("%s: the header lost the space before its heading: %q", where, header)
						}
					}
				}
			}
		}
	}
}

// keepDiffSyntax puts back a hunk header's line ranges a mask spanning it
// overwrote, and the diff marker of every line, and leaves the rest masked:
// a section heading that is a line of the key, and a line that only looks
// like a header.
func TestKeepDiffSyntaxRestoresHunkHeaders(t *testing.T) {
	line := keyBody(1)[0]
	stars := strings.Repeat("*", len(line))
	patch := "@@ -1,3 +1,3 @@ func a() {\n context\n+" + line + "\n" +
		"@@ -36,7 +36,7 @@ " + line + "\n " + line + "\n" +
		"@@ -50 +50 @@\n-" + line + "\n" +
		"@@ -x +1 @@ " + line + "\n\\ No newline at end of file"
	// The format-preserving mask, as the engine writes it over a key that
	// spans every line after the first context line.
	maskedHeader := "@@*-99*9*+99*9*@@*"
	redacted := "@@ -1,3 +1,3 @@ func a() {\n context\n*" + stars + "\n" +
		maskedHeader + stars + "\n*" + stars + "\n" +
		"@@*-99*+99*@@\n*" + stars + "\n" +
		"@@*-X*+9*@@*" + stars + "\n*" + strings.Repeat("*", len(" No newline at end of file"))
	if len(redacted) != len(patch) {
		t.Fatalf("the simulated mask is %d bytes, the patch %d", len(redacted), len(patch))
	}
	want := "@@ -1,3 +1,3 @@ func a() {\n context\n+" + stars + "\n" +
		"@@ -36,7 +36,7 @@ " + stars + "\n " + stars + "\n" +
		"@@ -50 +50 @@\n-" + stars + "\n" +
		"@@*-X*+9*@@*" + stars + "\n\\" + strings.Repeat("*", len(" No newline at end of file"))
	if got := keepDiffSyntax(patch, redacted); got != want {
		t.Fatalf("keepDiffSyntax:\n%s\nwant\n%s", got, want)
	}
	for header, n := range map[string]int{
		"@@ -1,3 +1,3 @@ func a() {": len("@@ -1,3 +1,3 @@ "),
		"@@ -0,0 +1 @@":              len("@@ -0,0 +1 @@"),
		"@@ -1, +1 @@":               0,
		"@@ -1 +1 @":                 0,
		" @@ -1 +1 @@":               0,
	} {
		if got := hunkHeaderLen(header); got != n {
			t.Errorf("hunkHeaderLen(%q) = %d, want %d", header, got, n)
		}
	}
}

// For any file patch and line cap, the bundle's hunks are the whole patch
// redacted as one text, split into hunks, then capped: whichever hunks the
// cap cuts, and wherever keys lie across hunk headers, the margin and the
// file text diff parsing keeps give Redact what it needs. Hunk headers keep
// their line ranges.
func TestCappedRedactionMatchesTheWholePatch(t *testing.T) {
	redactor, err := NewRedactor()
	if err != nil {
		t.Fatal(err)
	}
	ppk, _ := syntheticPuTTYKey()
	keys := [][]string{syntheticKeyLines(), syntheticEncryptedKeyLines(), ppk, pgpKeyLines(20 << 10)}
	random := rand.New(rand.NewPCG(1414, 0))
	for run := range 60 {
		// The file's lines in order, before hunk headers split them.
		var stream []string
		for range 1 + random.IntN(3) {
			for range random.IntN(30) {
				switch random.IntN(10) {
				case 0:
					stream = append(stream, strings.Repeat("filler ", 300+random.IntN(3000)))
				case 1:
					stream = append(stream, "contact: dev@example.com")
				default:
					stream = append(stream, fmt.Sprintf("line %d", len(stream)))
				}
			}
			stream = append(stream, keys[random.IntN(len(keys))]...)
		}
		var patch strings.Builder
		var headers []string
		for i, line := range stream {
			if i == 0 || random.IntN(12) == 0 {
				header := fmt.Sprintf("@@ -%d,%d +%d,%d @@", i+1, 9, i+3, 9)
				switch random.IntN(3) {
				case 1:
					header += " func f() {"
				case 2:
					header += " " + stream[max(i-1, 0)]
				}
				headers = append(headers, header)
				patch.WriteString(header + "\n")
			}
			patch.WriteString(string(" +-"[random.IntN(3)]) + line)
			if random.IntN(8) == 0 {
				patch.WriteByte('\r')
			}
			patch.WriteByte('\n')
		}
		limit := []int{0, 1, 3, 20}[random.IntN(4)]
		got, want, _ := assembleFilePatch(t, redactor, patch.String(), limit)
		if len(got) != len(headers) || len(want) != len(headers) {
			t.Fatalf("run %d: %d hunks, want %d (%d split from the whole patch)", run, len(got), len(headers), len(want))
		}
		for i := range got {
			if got[i] != want[i] {
				t.Fatalf("run %d, cap %d, hunk %d: redaction differs from the whole patch's:\n%.2000s\nwant\n%.2000s", run, limit, i, got[i], want[i])
			}
			if n := hunkHeaderLen(headers[i]); n == 0 || !strings.HasPrefix(got[i], headers[i][:n]) {
				t.Fatalf("run %d, hunk %d: header %q lost its line ranges: %.200q", run, i, headers[i], got[i])
			}
		}
	}
}

// keyBody returns n distinct 64-byte base64 lines of key material.
func keyBody(n int) []string {
	lines := make([]string, n)
	for i := range lines {
		lines[i] = strings.Repeat(fmt.Sprintf("Qz7%04dKp", i)+"Xb9", 6)[:64]
	}
	return lines
}

// A key whose BEGIN line is the last line the cap keeps, with a body that
// ends just inside the margin or runs far past it, is masked as in the
// whole patch.
func TestRedactionMarginAtItsBoundary(t *testing.T) {
	redactor, err := NewRedactor()
	if err != nil {
		t.Fatal(err)
	}
	const limit = 5
	label := "RSA " + "PRIVATE KEY"
	// A body line is 66 bytes in the patch, with its marker and newline.
	for _, n := range []int{(redactionMargin - 200) / 66, 2 * redactionMargin / 66} {
		patch := "@@ -1,9 +1,9 @@\n+a\n+b\n+c\n+-----BEGIN " + label + "-----\n" +
			prefixLines("+", strings.Join(keyBody(n), "\n")) + "\n+-----END " + label + "-----\n+after\n"
		got, want, _ := cutAfterRedaction(t, redactor, patch, limit)
		if got != want {
			t.Fatalf("%d body lines: capped redaction differs from the whole patch's:\n%s\nwant\n%s", n, got, want)
		}
		if strings.Contains(got, label) || strings.Count(got, "\n")+1 != limit {
			t.Fatalf("%d body lines: the BEGIN line is not masked or not kept:\n%s", n, got)
		}
	}
}

// The margin must cover the furthest the engine reads past a secret's
// trigger to confirm it: in the goredact version pinned here, the
// private-key rules' 16384-byte window. A PEM key whose first body run ends
// that far past "-----BEGIN " is found, and one a byte further is not.
// Once found, a key is masked to its END marker, past the margin. Any
// goredact upgrade must re-measure redactionMargin against its rules'
// lookaheads (see its comment), then update the version pinned here.
func TestRedactionMarginCoversTheEngine(t *testing.T) {
	if goredactVersion(t) != "v0.3.0" {
		t.Fatal("goredact changed: re-measure redactionMargin against its rules' lookaheads, then update the version this test pins")
	}
	redactor, err := NewRedactor()
	if err != nil {
		t.Fatal(err)
	}
	begin := "-----BEGIN RSA " + "PRIVATE KEY-----"
	trigger := len("-----BEGIN ")
	body := strings.Join(keyBody(4*redactionMargin/65), "\n")
	end := "\n-----END RSA " + "PRIVATE KEY-----"
	// reach is how far past the trigger the 16th byte of the key's first
	// body line lies, the byte that makes the body plausible key material.
	for reach, found := range map[int]bool{redactionMargin: true, redactionMargin + 1: false} {
		blank := reach - (len(begin) - trigger) - 16
		text := begin + strings.Repeat("\n", blank) + body + end + "\nafter"
		spans, err := redactor.Find(context.Background(), text)
		if err != nil {
			t.Fatal(err)
		}
		if got := len(spans) > 0 && spans[0].Start == 0; got != found {
			t.Fatalf("a key decided %d bytes past its trigger: found %v, want %v; the margin is %d", reach, got, found, redactionMargin)
		}
		if found && (len(spans) != 1 || spans[0].End != len(text)-len("\nafter")) {
			t.Fatalf("the key is not masked to its END marker: %+v of %d bytes", spans, len(text))
		}
	}
}

// pgpKeyLines returns an ASCII-armored PGP private key block of at least
// size bytes, one line per element: synthetic base64, not key material.
func pgpKeyLines(size int) []string {
	label := "PGP " + "PRIVATE KEY BLOCK"
	lines := append([]string{"-----BEGIN " + label + "-----", ""}, keyBody(size/65+1)...)
	return append(lines, "=Qz7a", "-----END "+label+"-----")
}

// A key far longer than the margin, added in a hunk, is masked whole in the
// bundle: every line of it, past the line cap and the margin too, keeps its
// diff marker and nothing else.
func TestRedactionMasksALongKeyInAHunk(t *testing.T) {
	redactor, err := NewRedactor()
	if err != nil {
		t.Fatal(err)
	}
	key := pgpKeyLines(40 << 10)
	patch := "@@ -1,2 +1,700 @@\n before\n" + prefixLines("+", strings.Join(key, "\n")) + "\n after\n"
	for _, limit := range []int{0, 40} {
		var got string
		if limit == 0 {
			b := &Bundle{Diff: []Hunk{{Patch: strings.TrimRight(patch, "\n")}}}
			if err := Redact(context.Background(), b, redactor); err != nil {
				t.Fatal(err)
			}
			got = b.Diff[0].Patch
			if !strings.HasSuffix(got, "\n after") || strings.Count(got, "\n") != len(key)+2 {
				t.Fatalf("the hunk lost its lines or its trailing context:\n%s", got[max(0, len(got)-200):])
			}
		} else {
			var want string
			got, want, _ = cutAfterRedaction(t, redactor, patch, limit)
			if got != want {
				t.Fatalf("capped at %d lines: redaction differs from the whole patch's", limit)
			}
		}
		for _, line := range key[2:] {
			if strings.Contains(got, line) {
				t.Fatalf("limit %d: key line %q survives", limit, line)
			}
		}
		if strings.Contains(got, "PRIVATE KEY") {
			t.Fatalf("limit %d: the armor survives", limit)
		}
		for _, line := range strings.Split(got, "\n")[1:] {
			if line != "" && !strings.ContainsRune(" +", rune(line[0])) {
				t.Fatalf("limit %d: line %q lost its diff marker", limit, line)
			}
		}
	}
}

// A PuTTY key added in a hunk is found on its "+" lines: its private lines
// are masked, its public lines kept.
func TestRedactionMasksAPuTTYKeyInAHunk(t *testing.T) {
	redactor, err := NewRedactor()
	if err != nil {
		t.Fatal(err)
	}
	ppk, private := syntheticPuTTYKey()
	public := ppk[4:6]
	patch := "@@ -0,0 +1,11 @@\n" + prefixLines("+", strings.Join(ppk, "\n"))
	b := &Bundle{Diff: []Hunk{{Patch: patch}}}
	if err := Redact(context.Background(), b, redactor); err != nil {
		t.Fatal(err)
	}
	got := b.Diff[0].Patch
	for _, line := range private {
		if strings.Contains(got, line) {
			t.Fatalf("private key line %q survives:\n%s", line, got)
		}
	}
	for _, line := range append(public, "+Private-MAC: ", "+Public-Lines: 2") {
		if !strings.Contains(got, line) {
			t.Fatalf("%q was masked:\n%s", line, got)
		}
	}
	if b.MaskedSpanCount != 1 || strings.Count(got, "\n") != strings.Count(patch, "\n") {
		t.Fatalf("masked %d spans, %d lines; want the one key and every line:\n%s", b.MaskedSpanCount, strings.Count(got, "\n")+1, got)
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
