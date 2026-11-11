package codex

import (
	"strings"
	"testing"

	"github.com/baum-ai/baum/sessions/codextranscript"
)

func rootEntry(id string) Entry {
	return Entry{Path: "/s/" + id + ".jsonl", Class: codextranscript.ThreadRoot,
		Meta: &codextranscript.SessionMeta{ID: id, SessionID: id}}
}

func childEntry(id, session, parent, fork string, depth int) Entry {
	return Entry{Path: "/s/" + id + ".jsonl", Class: codextranscript.ThreadChild,
		Meta: &codextranscript.SessionMeta{
			ID: id, SessionID: session, ParentThreadID: parent, ForkedFromID: fork,
			Source: codextranscript.Source{ThreadSpawn: &codextranscript.ThreadSpawn{ParentThreadID: parent, Depth: depth}},
		}}
}

// TestTreeFindErrors pins the outcomes Find has always had for damaged stores.
func TestTreeFindErrors(t *testing.T) {
	disagree := childEntry("c", "r", "r", "", 1)
	disagree.Meta.Source.ThreadSpawn.ParentThreadID = "other"
	cases := []struct {
		name    string
		index   []Entry
		root    string
		wantErr string
	}{
		{"valid", []Entry{rootEntry("r"), childEntry("c", "r", "r", "", 1)}, "r", ""},
		{"missing parent", []Entry{rootEntry("r"), childEntry("c", "r", "gone", "", 1)}, "r", "missing parent"},
		{"parent cycle", []Entry{rootEntry("r"), childEntry("a", "r", "b", "", 1), childEntry("b", "r", "a", "", 1)}, "r", "parent cycle"},
		{"disagreeing links", []Entry{rootEntry("r"), disagree}, "r", "disagreeing parent links"},
		{"depth mismatch", []Entry{rootEntry("r"), childEntry("c", "r", "r", "", 3)}, "r", "reports depth 3, want 1"},
		{"session mismatch", []Entry{rootEntry("r"), childEntry("c", "x", "r", "", 1)}, "r", "reports session x, want r"},
		{"missing fork source", []Entry{rootEntry("r"), childEntry("c", "r", "r", "nofork", 1)}, "r", "missing fork source"},
		{"no metadata", []Entry{rootEntry("r"), {Path: "/s/bad.jsonl"}}, "r", "has no thread metadata"},
		{"first broken child wins", []Entry{rootEntry("r"), childEntry("a", "r", "gone-a", "", 1), childEntry("b", "r", "gone-b", "", 1)}, "r", "gone-a"},
		{"other session's damage is ignored", []Entry{rootEntry("r"), rootEntry("q"), childEntry("c", "q", "gone", "", 1)}, "r", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var root Entry
			for _, e := range tc.index {
				if e.Meta != nil && e.Meta.ID == tc.root {
					root = e
				}
			}
			for name, find := range map[string]func(Entry) (Files, error){
				"Find": func(e Entry) (Files, error) { return Find(tc.index, e) },
				"Tree": NewTree(tc.index).Find,
			} {
				_, err := find(root)
				switch {
				case tc.wantErr == "" && err != nil:
					t.Errorf("%s: unexpected error %v", name, err)
				case tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)):
					t.Errorf("%s: error = %v, want it to contain %q", name, err, tc.wantErr)
				}
			}
		})
	}
}

// TestPassesNest: the memo lives until the last pass ends.
func TestPassesNest(t *testing.T) {
	s := newStore(t)
	cache := &Cache{}
	cache.BeginPass()
	cache.BeginPass()
	_, _, _ = FindSessionsCached(s.dir, []string{s.repo}, cache)
	cache.EndPass()
	writeFile(t, s.dir+"/2026/08/01/rollout-new.jsonl", s.root("new"))
	got, _, _ := FindSessionsCached(s.dir, []string{s.repo}, cache)
	if len(got) != 1 {
		t.Fatalf("sessions = %d, want the memoised 1 while a pass is open", len(got))
	}
	cache.EndPass()
	got, _, _ = FindSessionsCached(s.dir, []string{s.repo}, cache)
	if len(got) != 2 {
		t.Fatalf("sessions = %d, want 2 after the last pass", len(got))
	}
}

// TestCacheDoesNotKeepFailures: an unreadable first record is not cached, so it
// is read again once it is complete.
func TestCacheDoesNotKeepFailures(t *testing.T) {
	s := newStore(t)
	cache := &Cache{}
	path := s.dir + "/2026/08/01/rollout-late.jsonl"
	writeFile(t, path, `{"timestamp":"2026-08-01T12:00:00Z","type":"session_meta","payl`)
	_, report, err := FindSessionsCached(s.dir, []string{s.repo}, cache)
	if err != nil || len(report.Skipped) != 1 {
		t.Fatalf("report = %+v, err = %v, want the partial rollout skipped", report, err)
	}
	writeFile(t, path, s.root("late"))
	got, _, err := FindSessionsCached(s.dir, []string{s.repo}, cache)
	if err != nil || len(got) != 2 {
		t.Fatalf("sessions = %d, err = %v, want 2", len(got), err)
	}
}
