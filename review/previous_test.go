package review

import "testing"

func TestPreviousVersionCarriesABlankTitleAsItsPositionalLabel(t *testing.T) {
	previous, err := PreviousVersionOf([]byte(`{"sections":[{"id":"sec_a","title":"Named","derived":{}},{"id":"sec_b","title":" ","hunks":["h_0123456789ab"]}]}`), 3)
	if err != nil {
		t.Fatal(err)
	}
	if previous.Version != 3 || len(previous.Sections) != 2 {
		t.Fatalf("previous = %+v", previous)
	}
	named, blank := previous.Sections[0], previous.Sections[1]
	if named.Title != "Named" || string(named.Content) != `{"id":"sec_a","title":"Named"}` {
		t.Fatalf("named section changed: %+v %s", named, named.Content)
	}
	if blank.Title != "Section 2" || string(blank.Content) != `{"hunks":["h_0123456789ab"],"id":"sec_b","title":"Section 2"}` {
		t.Fatalf("blank section: %+v %s", blank, blank.Content)
	}
}

// The previous version records each of its hunks' right-side range, so its
// anchors can follow a hunk a rebase moved (review.FitPreviousHunkAnchors).
func TestPreviousVersionRecordsItsHunkRanges(t *testing.T) {
	previous, err := PreviousVersionOf([]byte(`{"index":{"hunks":[{"id":"h_0123456789ab","new_range":[268,7]},{"id":"h_aaaaaaaaaaaa","new_range":[]}]},"sections":[]}`), 4)
	if err != nil {
		t.Fatal(err)
	}
	if len(previous.HunkRanges) != 1 || previous.HunkRanges["h_0123456789ab"] != [2]int{268, 7} {
		t.Fatalf("hunk ranges = %v, want h_0123456789ab at 268 for 7 lines only", previous.HunkRanges)
	}
}
