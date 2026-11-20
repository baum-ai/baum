package reviewv2

import (
	"encoding/json"
	"maps"
	"slices"
	"testing"
)

// current places the fixture bundle's hunks.
func current(t *testing.T) *Refs {
	t.Helper()
	return RefsOf(fixtureBundle(t))
}

// renamePrevious is a stored baum.review/2 version with logical sections
// over hunks placed by index (one JSON hunk entry each).
func renamePrevious(t *testing.T, index string, sections ...Section) []byte {
	t.Helper()
	for i := range sections {
		sections[i].Kind = KindLogical
		sections[i].Body = []Block{{Type: BlockProse, Text: "Previous text of " + sections[i].ID + "."}}
	}
	return encode(t, Document{Schema: Name, Sections: sections, Index: json.RawMessage(`{"hunks":[` + index + `]}`)})
}

func logical(id string, hunks ...string) Section {
	return Section{ID: id, Kind: KindLogical, Hunks: hunks}
}

// placed is a current diff: each hunk's path and new-side lines.
func placed(hunks map[string]HunkRef) *Refs {
	r := newRefs()
	for _, id := range slices.Sorted(maps.Keys(hunks)) {
		r.addHunk(id, hunks[id])
	}
	return r
}

// A new ID over an edited hunk (another ID, the same path, overlapping
// lines) continues the previous section; one over a new file does not; a
// section the new map keeps under its ID is no candidate.
func TestRenamedOverEditedHunk(t *testing.T) {
	previous := renamePrevious(t,
		`{"id":"h_old","path":"queue/retry.go","new_range":[10,20]},{"id":"h_kept","path":"queue/kept.go","new_range":[1,5]}`,
		logical("sec_retry", "h_old"), logical("sec_kept", "h_kept"))
	now := placed(map[string]HunkRef{"h_new": {Path: "queue/retry.go", Start: 25, Count: 8}, "h_file": {Path: "queue/added.go", Start: 1, Count: 30}, "h_kept": {Path: "queue/kept.go", Start: 1, Count: 5}})
	sections := []Section{logical("sec_backoff", "h_new"), logical("sec_added", "h_file"), logical("sec_kept", "h_kept")}
	if got := RenamedSections(previous, sections, now); len(got) != 1 || got["sec_backoff"] != "sec_retry" {
		t.Fatalf("renamed = %v", got)
	}
	r, ok := RevisableSections(previous, sections, now)["sec_backoff"]
	if !ok || r.Previous.ID != "sec_retry" || !slices.Equal(r.New, []string{"h_new"}) || len(r.Gone) != 1 || r.Gone[0].ID != "h_old" || r.Additive() {
		t.Errorf("revised = %+v", r)
	}

	// Lines that do not overlap on a file both sections share with others
	// say nothing.
	apart := placed(map[string]HunkRef{"h_new": {Path: "queue/retry.go", Start: 40, Count: 3}, "h_other": {Path: "queue/other.go", Start: 1, Count: 3}})
	if got := RenamedSections(previous, []Section{logical("sec_backoff", "h_new", "h_other")}, apart); len(got) != 0 {
		t.Errorf("a shared path alone matched: %v", got)
	}
	// When that file is the only one of both, it does.
	if got := RenamedSections(previous, []Section{logical("sec_backoff", "h_new")}, apart); got["sec_backoff"] != "sec_retry" {
		t.Errorf("the only file of both did not match: %v", got)
	}
	// A baum.review/1 previous version matches nothing.
	if got := RenamedSections(readFile(t, "v1.review.json"), sections, now); len(got) != 0 {
		t.Errorf("a baum.review/1 version matched: %v", got)
	}
}

// The most shared hunks win, and a previous section continues in at most
// one new section: the next best goes unmatched.
func TestRenamedBestMatchOnce(t *testing.T) {
	index := `{"id":"h_1","path":"a.go","new_range":[1,5]},{"id":"h_2","path":"b.go","new_range":[1,5]},{"id":"h_3","path":"c.go","new_range":[1,5]}`
	previous := renamePrevious(t, index, logical("sec_x", "h_1", "h_2"), logical("sec_y", "h_3"))
	now := placed(map[string]HunkRef{"h_1": {Path: "a.go", Start: 1, Count: 5}, "h_2": {Path: "b.go", Start: 1, Count: 5}, "h_3": {Path: "c.go", Start: 1, Count: 5}})
	if got := RenamedSections(previous, []Section{logical("sec_all", "h_3", "h_1", "h_2")}, now); len(got) != 1 || got["sec_all"] != "sec_x" {
		t.Errorf("most shared hunks: %v", got)
	}
	got := RenamedSections(previous, []Section{logical("sec_one", "h_1"), logical("sec_two", "h_2"), logical("sec_three", "h_3")}, now)
	if len(got) != 2 || got["sec_one"] != "sec_x" || got["sec_three"] != "sec_y" {
		t.Errorf("one match each: %v", got)
	}
	// sec_two takes over sec_x's other hunk, not sec_x itself.
	j := JoinedSections(previous, []Section{logical("sec_one", "h_1"), logical("sec_two", "h_2"), logical("sec_three", "h_3")}, now)
	if len(j["sec_one"].Hunks) != 0 || len(j["sec_two"].Hunks) != 1 || j["sec_two"].Hunks[0].From != "sec_x" {
		t.Errorf("joined: %+v", j)
	}
}

// A section kept under a new ID keeps the previous section's risks with
// their IDs and introduced version; the risks it no longer names are
// mitigated in it, and the old ID is gone.
func TestRenamedSectionCarriesRisks(t *testing.T) {
	d := authored(t)
	d.Sections[0].Risks = append(d.Sections[0].Risks, Risk{ID: "rsk_fixed_now", Tier: "low", Category: "correctness", Text: "A test is flaky.", Cites: []Anchor{HunkAnchor("h_a00000000003", nil)}})
	first := encode(t, completeFixture(t, d, 1, nil))
	next := authored(t)
	next.Sections[0].ID = "sec_backoff"
	if got := RenamedSections(first, next.Sections, current(t)); got["sec_backoff"] != "sec_retry_policy" {
		t.Fatalf("renamed = %v", got)
	}
	out := completeFixture(t, next, 2, first)
	carried, fixed := riskIn(out, "rsk_unbounded_retries"), riskIn(out, "rsk_fixed_now")
	if r := carried["sec_backoff"]; len(carried) != 1 || r.State != "active" || r.IntroducedVersion == nil || *r.IntroducedVersion != 1 {
		t.Errorf("carried: %+v", carried)
	}
	if r := fixed["sec_backoff"]; len(fixed) != 1 || r.State != "mitigated" || r.MitigatedVersion == nil || *r.MitigatedVersion != 2 {
		t.Errorf("no longer named: %+v", fixed)
	}
	for _, s := range out.Sections {
		if s.ID == "sec_retry_policy" {
			t.Error("the old ID is still a section")
		}
	}
}
