package bundle

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// indexHunks is n hunks of path, ten lines apart, each with a short patch.
func indexHunks(path string, n int) []Hunk {
	hunks := make([]Hunk, n)
	for i := range hunks {
		start := 10 + 10*i
		hunks[i] = Hunk{
			ID: hunkID(path, fmt.Sprint(i)), Path: path, Change: "modified", Role: RoleForPath(path),
			StartLine: start, LineCount: 3, Additions: 2, Deletions: 1, DiffURL: HunkDiffURL(path, start),
			Patch: fmt.Sprintf("@@ -%d,2 +%d,3 @@\n context\n-old\n+new\n+more", start, start),
		}
	}
	return hunks
}

func diffPaths(diff []Hunk) []string {
	var paths []string
	for _, hunk := range diff {
		paths = append(paths, hunk.Path)
	}
	return paths
}

func countPath(diff []Hunk, path string) int {
	n := 0
	for _, hunk := range diff {
		if hunk.Path == path {
			n++
		}
	}
	return n
}

// The diff index is fitted by collapsing generated and vendored files first,
// each to one entry that keeps the file's place, ID shape and line counts.
func TestFitCollapsesGeneratedAndVendoredFilesFirst(t *testing.T) {
	diff := slices.Concat(indexHunks("a.go", 3), indexHunks("pnpm-lock.yaml", 40), indexHunks("vendor/x/lib.go", 20), indexHunks("z.go", 2))
	bundle := &Bundle{Diff: diff}
	limit := diffIndexBytes(&Bundle{Diff: slices.Concat(diff[:3], []Hunk{collapseHunks(diff[3:43]), collapseHunks(diff[43:63])}, diff[63:])})
	if err := Fit(bundle, Caps{MaxDiffIndexBytes: limit}); err != nil {
		t.Fatal(err)
	}
	if got := diffPaths(bundle.Diff); !slices.Equal(got, []string{"a.go", "a.go", "a.go", "pnpm-lock.yaml", "vendor/x/lib.go", "z.go", "z.go"}) {
		t.Fatalf("diff paths %q", got)
	}
	lock := bundle.Diff[3]
	want := Hunk{
		ID: fileHunkID("pnpm-lock.yaml"), Path: "pnpm-lock.yaml", Change: "modified", Role: RoleGenerated,
		StartLine: 10, LineCount: 10*39 + 3, Additions: 80, Deletions: 40, DiffURL: HunkDiffURL("pnpm-lock.yaml", 10),
		Textless: true, TextlessReason: CollapsedReason,
	}
	if lock != want {
		t.Fatalf("collapsed lockfile:\n got %+v\nwant %+v", lock, want)
	}
	if !regexp.MustCompile(`^h_[0-9a-f]{12}(-[0-9]+)?$`).MatchString(lock.ID) {
		t.Fatalf("collapsed ID %q does not have the hunk ID form", lock.ID)
	}
	if bundle.Diff[0].Patch == "" || bundle.Diff[0].Textless {
		t.Fatalf("a code hunk lost its patch: %+v", bundle.Diff[0])
	}
	if size := diffIndexBytes(bundle); size > limit {
		t.Fatalf("diff index %d bytes over its %d-byte limit", size, limit)
	}
	if !slices.Equal(bundle.Truncated, []string{"hunks of 2 generated or vendored file(s) collapsed to one diff index entry per file, without patch text, to fit the diff index limit"}) {
		t.Fatalf("notes %q", bundle.Truncated)
	}
}

// Other files are collapsed one at a time, the largest saving first, and no
// more than the limit needs.
func TestFitCollapsesOtherFilesLargestFirst(t *testing.T) {
	diff := slices.Concat(indexHunks("a.go", 4), indexHunks("b.go", 30), indexHunks("c.go", 10), indexHunks("go.sum", 5))
	bundle := &Bundle{Diff: diff}
	full := diffIndexBytes(bundle)
	if err := Fit(bundle, Caps{MaxDiffIndexBytes: full * 2 / 3}); err != nil {
		t.Fatal(err)
	}
	if countPath(bundle.Diff, "go.sum") != 1 || countPath(bundle.Diff, "b.go") != 1 || countPath(bundle.Diff, "c.go") != 10 || countPath(bundle.Diff, "a.go") != 4 {
		t.Fatalf("diff paths %q", diffPaths(bundle.Diff))
	}
	if size := diffIndexBytes(bundle); size > full*2/3 {
		t.Fatalf("diff index %d bytes over its limit %d", size, full*2/3)
	}
	if !slices.Equal(bundle.Truncated, []string{
		"hunks of 1 generated or vendored file(s) collapsed to one diff index entry per file, without patch text, to fit the diff index limit",
		"hunks of 1 other file(s) collapsed to one diff index entry per file, without patch text, to fit the diff index limit, the largest first",
	}) {
		t.Fatalf("notes %q", bundle.Truncated)
	}
}

// When collapsing every file is not enough, whole files are left out,
// generated and vendored first, each from the end of the diff.
func TestFitLeavesFilesOutLast(t *testing.T) {
	var diff []Hunk
	for i := range 6 {
		diff = append(diff, indexHunks(fmt.Sprintf("src/f%d.go", i), 1)...)
		diff = append(diff, indexHunks(fmt.Sprintf("gen/f%d.pb.go", i), 1)...)
	}
	bundle := &Bundle{Diff: diff}
	keep := diffIndexBytes(&Bundle{Diff: []Hunk{diff[0], diff[2], diff[4]}})
	if err := Fit(bundle, Caps{MaxDiffIndexBytes: keep}); err != nil {
		t.Fatal(err)
	}
	if got := diffPaths(bundle.Diff); !slices.Equal(got, []string{"src/f0.go", "src/f1.go", "src/f2.go"}) {
		t.Fatalf("diff paths %q", got)
	}
	if !slices.Equal(bundle.Truncated, []string{"9 file(s) left out of the diff index to fit its limit, generated and vendored files first"}) {
		t.Fatalf("notes %q", bundle.Truncated)
	}
}

// A file the checkout omitted keeps that reason when collapsed, so the risk
// score still counts it as unreadable.
func TestFitKeepsTheCheckoutOmissionOfACollapsedFile(t *testing.T) {
	diff := slices.Concat(indexHunks("a.go", 1), indexHunks("big.go", 20))
	for i := 1; i < len(diff); i++ {
		diff[i].Patch, diff[i].Textless, diff[i].TextlessReason = "", true, OmittedSizeReason
	}
	bundle := &Bundle{Diff: diff}
	if err := Fit(bundle, Caps{MaxDiffIndexBytes: diffIndexBytes(bundle) / 2}); err != nil {
		t.Fatal(err)
	}
	if len(bundle.Diff) != 2 || bundle.Diff[1].TextlessReason != OmittedSizeReason || bundle.Diff[1].ID != fileHunkID("big.go") {
		t.Fatalf("diff %+v", bundle.Diff)
	}
}

// The case the limit used to fail: a dependency bump with thousands of
// lockfile and vendored hunks beside ordinary code fits the default limits,
// keeping every code hunk.
func TestFitReviewsAPullRequestWithThousandsOfHunks(t *testing.T) {
	diff := slices.Concat(indexHunks("package-lock.json", 1500), indexHunks("pnpm-lock.yaml", 800))
	for i := range 40 {
		diff = append(diff, indexHunks(fmt.Sprintf("vendor/github.com/example/module%d/file.go", i), 10)...)
	}
	for i := range 30 {
		diff = append(diff, indexHunks(fmt.Sprintf("internal/service/handler%d.go", i), 8)...)
	}
	bundle := &Bundle{Diff: diff}
	caps := Caps{MaxBytes: 256 << 10, MaxDiffIndexBytes: 512 << 10, MaxHunkPatchLines: 200}
	if diffIndexBytes(bundle) <= caps.MaxDiffIndexBytes {
		t.Fatalf("fixture index of %d bytes must exceed the limit", diffIndexBytes(bundle))
	}
	if err := Fit(bundle, caps); err != nil {
		t.Fatal(err)
	}
	if size := diffIndexBytes(bundle); size > caps.MaxDiffIndexBytes || !fits(bundle, caps.MaxBytes) {
		t.Fatalf("index %d bytes, content fits: %t", size, fits(bundle, caps.MaxBytes))
	}
	code := 0
	for _, hunk := range bundle.Diff {
		if strings.HasPrefix(hunk.Path, "internal/") {
			code++
		}
	}
	if code != 240 || countPath(bundle.Diff, "package-lock.json") != 1 || len(bundle.Diff) != 240+2+40 {
		t.Fatalf("%d code hunks of %d entries", code, len(bundle.Diff))
	}
}

// Collapsing alone cannot fit thousands of single-hunk files: the files
// past what fits are left out, and the review still runs.
func TestFitReviewsAPullRequestWithThousandsOfFiles(t *testing.T) {
	var diff []Hunk
	for i := range 3000 {
		diff = append(diff, indexHunks(fmt.Sprintf("packages/app/src/components/generated-by-codemod/component%04d.tsx", i), 1)...)
	}
	bundle := &Bundle{Diff: diff}
	caps := Caps{MaxBytes: 256 << 10, MaxDiffIndexBytes: 512 << 10, MaxHunkPatchLines: 200}
	if err := Fit(bundle, caps); err != nil {
		t.Fatal(err)
	}
	if size := diffIndexBytes(bundle); size > caps.MaxDiffIndexBytes || len(bundle.Diff) == 0 || len(bundle.Diff) >= 3000 {
		t.Fatalf("index %d bytes with %d entries", size, len(bundle.Diff))
	}
	if bundle.Diff[0].Path != diff[0].Path {
		t.Fatalf("the first files must be kept: %q", bundle.Diff[0].Path)
	}
	want := fmt.Sprintf("%d file(s) left out of the diff index to fit its limit, generated and vendored files first", 3000-len(bundle.Diff))
	if !slices.Contains(bundle.Truncated, want) {
		t.Fatalf("notes %q, want %q", bundle.Truncated, want)
	}
}

// Within its limit, the index is left as it is.
func TestFitLeavesAnIndexWithinItsLimit(t *testing.T) {
	diff := slices.Concat(indexHunks("a.go", 3), indexHunks("go.sum", 3))
	bundle := &Bundle{Diff: slices.Clone(diff)}
	if err := Fit(bundle, Caps{MaxDiffIndexBytes: diffIndexBytes(bundle)}); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(bundle.Diff, diff) || len(bundle.Truncated) != 0 {
		t.Fatalf("diff %+v, notes %q", bundle.Diff, bundle.Truncated)
	}
}
