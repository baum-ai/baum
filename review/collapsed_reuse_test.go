package review

import (
	"encoding/json"
	"fmt"
	"testing"

	bundlepkg "github.com/baum-ai/baum/bundle"
)

// A section that places a file the diff index collapsed reuses its previous
// details only when the file's change is the same: a new version with
// different code in the same collapsed file goes back to the details stage.
func TestCollapsedFileReusesDetailsOnlyWhenUnchanged(t *testing.T) {
	collapsed := func(changed string) string {
		t.Helper()
		var diff []bundlepkg.Hunk
		for i := range 40 {
			content := fmt.Sprint(i)
			if i == 7 {
				content = changed
			}
			hunks, err := bundlepkg.BuildDiffIndex(bundlepkg.Comparison{Files: []bundlepkg.FileChange{{NewPath: "big.go", Status: "modified"}}}, func(...string) ([]byte, error) {
				return []byte(fmt.Sprintf("@@ -%d,1 +%d,1 @@\n-old\n+%s\n", 10*i+1, 10*i+1, content)), nil
			}, bundlepkg.RoleForPath)
			if err != nil {
				t.Fatal(err)
			}
			diff = append(diff, hunks...)
		}
		b := &bundlepkg.Bundle{Diff: diff}
		if err := bundlepkg.Fit(b, bundlepkg.Caps{MaxDiffIndexBytes: 1024}); err != nil {
			t.Fatal(err)
		}
		if len(b.Diff) != 1 || b.Diff[0].TextlessReason != bundlepkg.CollapsedReason {
			t.Fatalf("big.go not collapsed: %+v", b.Diff)
		}
		return b.Diff[0].ID
	}
	before := collapsed("first")
	previous := &bundlepkg.PreviousVersion{Version: 1, Sections: []bundlepkg.PreviousSection{{
		ID: "sec_big", HunkContentHashes: []string{before},
		Content: json.RawMessage(`{"id":"sec_big","title":"Big","summary":"Reviewed the first change.","risks":[]}`),
	}}}
	outline := func(id string) []byte {
		return []byte(`{"sections":[{"id":"sec_big","hunks":["` + id + `"]}]}`)
	}
	if same := collapsed("first"); same != before || len(ReusableSectionDetails(previous, outline(same), nil)) != 1 {
		t.Fatalf("an unchanged collapsed file must keep its ID (%s, %s) and its details", before, same)
	}
	after := collapsed("second")
	if after == before {
		t.Fatalf("a collapsed file kept its ID %s although its code changed", before)
	}
	if reusable := ReusableSectionDetails(previous, outline(after), nil); len(reusable) != 0 {
		t.Fatalf("details reused for changed code in a collapsed file: %s", reusable)
	}
}
