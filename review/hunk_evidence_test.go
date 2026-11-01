package review

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/baum-ai/baum/bundle"
)

const evidenceDocument = `{"schema":"baum.review/1","index":{"file_comparisons":[],"hunks":[` +
	`{"id":"h_000000000001","path":"a.go","change":"modified","role":"code","new_range":[3,3],"additions":1,"deletions":0,"github_url":"diff-aR3","diff":{"header":"@@ -3,2 +3,3 @@","lines":[]}},` +
	`{"id":"h_000000000002","path":"b.go","change":"removed","role":"code","new_range":[0,0],"additions":0,"deletions":1}` +
	`],"checks":[]},"sections":[{"hunks":["h_000000000001"]}]}`

func TestStripHunkEvidenceKeepsEverythingElse(t *testing.T) {
	stripped, changed, err := StripHunkEvidence([]byte(evidenceDocument))
	if err != nil || !changed {
		t.Fatalf("changed %v, %v", changed, err)
	}
	want := `{"schema":"baum.review/1","index":{"file_comparisons":[],"hunks":[` +
		`{"id":"h_000000000001","path":"a.go","change":"modified","role":"code","new_range":[3,3],"additions":1,"deletions":0},` +
		`{"id":"h_000000000002","path":"b.go","change":"removed","role":"code","new_range":[0,0],"additions":0,"deletions":1}` +
		`],"checks":[]},"sections":[{"hunks":["h_000000000001"]}]}`
	if string(stripped) != want {
		t.Fatalf("stripped\n%s\nwant\n%s", stripped, want)
	}
	again, changed, err := StripHunkEvidence(stripped)
	if err != nil || changed || &again[0] != &stripped[0] {
		t.Fatalf("a stripped document changed again: %v %v", changed, err)
	}
	for _, document := range []string{`{"schema":"baum.review/1"}`, `{"index":null}`, `{"index":{"checks":[]}}`} {
		if out, changed, err := StripHunkEvidence([]byte(document)); err != nil || changed || string(out) != document {
			t.Errorf("%s: %s %v %v", document, out, changed, err)
		}
	}
	if _, _, err := StripHunkEvidence([]byte(`[]`)); err == nil {
		t.Error("a document that is not an object was stripped")
	}
}

// The derived github_url is the one the bundle records for the hunk; a hunk
// that has one keeps it.
func TestDeriveHunkURLsIsTheBundlesFragment(t *testing.T) {
	stripped, _, err := StripHunkEvidence([]byte(evidenceDocument))
	if err != nil {
		t.Fatal(err)
	}
	derived, err := DeriveHunkURLs(stripped)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`"deletions":0,"github_url":"` + bundle.HunkDiffURL("a.go", 3) + `"}`,
		`"deletions":1,"github_url":"` + bundle.HunkDiffURL("b.go", 0) + `"}`,
	} {
		if !strings.Contains(string(derived), want) {
			t.Errorf("derived %s\nlacks %s", derived, want)
		}
	}
	original := []byte(strings.Replace(evidenceDocument, `"deletions":1}`, `"deletions":1,"github_url":"kept"}`, 1))
	if out, err := DeriveHunkURLs(original); err != nil || &out[0] != &original[0] {
		t.Fatalf("a document with every fragment changed: %s %v", out, err)
	}
}

func TestEmbedHunkPatchesFindsEachHunkInItsFilesPatch(t *testing.T) {
	stripped, _, err := StripHunkEvidence([]byte(evidenceDocument))
	if err != nil {
		t.Fatal(err)
	}
	patches := map[string]string{
		"a.go": "@@ -1,2 +1,2 @@ func a\n-x\n+y\n z\n@@ -3,2 +3,3 @@ func b\n c\n+d\n e\n",
		"b.go": "@@ -1 +0,0 @@\n-gone\n\\ No newline at end of file\n",
	}
	var asked []string
	source := func(_ context.Context, path string) (string, bool, error) {
		asked = append(asked, path)
		patch, ok := patches[path]
		return patch, ok, nil
	}
	out, err := EmbedHunkPatches(context.Background(), stripped, source, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`"github_url":"` + bundle.HunkDiffURL("a.go", 3) + `","diff":{"header":"@@ -3,2 +3,3 @@ func b","lines":[{"kind":"context","text":"c","old_line":3,"new_line":3},{"kind":"add","text":"d","new_line":4},{"kind":"context","text":"e","old_line":4,"new_line":5}]}}`,
		`"github_url":"` + bundle.HunkDiffURL("b.go", 0) + `","diff":{"header":"@@ -1 +0,0 @@","lines":[{"kind":"remove","text":"gone","old_line":1}]}}`,
	} {
		if !strings.Contains(string(out), want) {
			t.Errorf("embedded %s\nlacks %s", out, want)
		}
	}
	if strings.Join(asked, ",") != "a.go,b.go" {
		t.Errorf("asked for %v", asked)
	}

	// A stored diff is kept as it is; a hunk whose counts differ from its
	// patch's, or whose file has no patch, gets none; past the byte budget
	// nothing more is read.
	kept, err := EmbedHunkPatches(context.Background(), []byte(evidenceDocument), source, 0)
	if err != nil || !strings.Contains(string(kept), `"github_url":"diff-aR3","diff":{"header":"@@ -3,2 +3,3 @@","lines":[]}`) {
		t.Fatalf("stored diff replaced: %s %v", kept, err)
	}
	patches["a.go"] = "@@ -3,2 +3,3 @@\n c\n+d\n+e\n"
	delete(patches, "b.go")
	out, err = EmbedHunkPatches(context.Background(), stripped, source, 0)
	if err != nil || strings.Contains(string(out), `"diff"`) {
		t.Fatalf("a different hunk was embedded: %s %v", out, err)
	}
	asked = nil
	patches["a.go"] = "@@ -3,2 +3,3 @@ func b\n c\n+d\n e\n"
	patches["b.go"] = "@@ -1 +0,0 @@\n-gone\n"
	out, err = EmbedHunkPatches(context.Background(), stripped, source, int64(len(patches["a.go"])))
	if err != nil || strings.Count(string(out), `"diff"`) != 1 || strings.Join(asked, ",") != "a.go" {
		t.Fatalf("budget: asked %v for %s %v", asked, out, err)
	}
	failed := errors.New("git failed")
	if _, err := EmbedHunkPatches(context.Background(), stripped, func(context.Context, string) (string, bool, error) { return "", false, failed }, 0); !errors.Is(err, failed) {
		t.Fatalf("error %v", err)
	}
}

// A duplicate key is read as encoding/json reads it, the last one, and an
// explicit null diff is no diff: it is filled in where it stands.
func TestEmbedHunkPatchesReadsDuplicateKeysAndNullDiffs(t *testing.T) {
	document := []byte(`{"index":{"hunks":[{"id":"h_000000000001","path":"x.go","path":"a.go","new_range":[9,9],"new_range":[3,3],` +
		`"additions":1,"deletions":0,"github_url":"kept","diff":null}]}}`)
	paths, err := HunkPatchPaths(document)
	if err != nil || strings.Join(paths, ",") != "a.go" {
		t.Fatalf("paths %v %v", paths, err)
	}
	out, err := EmbedHunkPatches(context.Background(), document, func(_ context.Context, path string) (string, bool, error) {
		return "@@ -3,2 +3,3 @@\n c\n+d\n e\n", path == "a.go", nil
	}, 0)
	want := `"github_url":"kept","diff":{"header":"@@ -3,2 +3,3 @@","lines":[`
	if err != nil || !strings.Contains(string(out), want) || strings.Contains(string(out), "null") {
		t.Fatalf("embedded %s %v", out, err)
	}
	var decoded struct {
		Index struct {
			Hunks []struct {
				Diff *struct{ Header string } `json:"diff"`
			} `json:"hunks"`
		} `json:"index"`
	}
	if err := json.Unmarshal(out, &decoded); err != nil || decoded.Index.Hunks[0].Diff == nil {
		t.Fatalf("encoding/json reads no diff: %s %v", out, err)
	}
}

func TestHunkFromFilePatchLargeHunks(t *testing.T) {
	const count = 30000
	for _, kind := range []string{"add", "remove"} {
		t.Run(kind, func(t *testing.T) {
			start, newCount, added, removed := 1, count, count, 0
			header, marker := fmt.Sprintf("@@ -0,0 +1,%d @@", count), "+"
			if kind == "remove" {
				start, newCount, added, removed = 0, 0, 0, count
				header, marker = fmt.Sprintf("@@ -1,%d +0,0 @@", count), "-"
			}
			// Include a preceding hunk and no final newline to exercise both
			// boundaries without copying the large hunk's accumulated text.
			patch := "@@ -9 +9 @@\n-before\n+after\n" + header + "\n" +
				strings.Repeat(marker+"line\n", count-1) + marker + "last"
			diff, err := hunkFromFilePatch(context.Background(), patch, start, newCount, added, removed)
			if err != nil || diff == nil {
				t.Fatalf("missing large hunk: %v", err)
			}
			if diff.Header != header || len(diff.Lines) != count || diff.Lines[0].Kind != kind || diff.Lines[count-1].Text != "last" {
				t.Fatalf("wrong hunk: header %q, %d lines", diff.Header, len(diff.Lines))
			}
		})
	}
}

func TestEmbedHunkPatchesCancelledAfterRead(t *testing.T) {
	document := []byte(`{"index":{"hunks":[{"path":"a.go","new_range":[1,1],"additions":1,"deletions":0}]}}`)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_, err := EmbedHunkPatches(ctx, document, func(context.Context, string) (string, bool, error) {
		cancel()
		return "@@ -0,0 +1 @@\n+line\n", true, nil
	}, 0)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error %v, want cancellation after reading the patch", err)
	}
}

func BenchmarkHunkFromFilePatch(b *testing.B) {
	for _, count := range []int{16384, 65536} {
		b.Run(fmt.Sprint(count), func(b *testing.B) {
			patch := fmt.Sprintf("@@ -0,0 +1,%d @@\n", count) + strings.Repeat("+line\n", count)
			b.SetBytes(int64(len(patch)))
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				diff, err := hunkFromFilePatch(context.Background(), patch, 1, count, count, 0)
				if err != nil || diff == nil {
					b.Fatalf("missing hunk: %v", err)
				}
			}
		})
	}
}
