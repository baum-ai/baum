package tilelibrary_test

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/baum-ai/baum/internal/tilelibrary"
	"github.com/baum-ai/baum/tiles"
	"github.com/baum-ai/baum/tiles/library"
)

func names(entries []library.Entry) []string {
	var out []string
	for _, e := range entries {
		out = append(out, e.Definition.Name)
	}
	return out
}

// typeOf is a saved type for Offer, built from a stored change.
func typeOf(t *testing.T, name string, paths []string) tilelibrary.Type {
	t.Helper()
	c := change(t, name, "")
	c.Paths = paths
	p, err := tilelibrary.Prepare(c)
	if err != nil {
		t.Fatal(err)
	}
	return tilelibrary.Type{Name: name, Entry: p.Entry, Paths: paths, Origin: tilelibrary.OriginChat, Created: "2026-10-01T00:00:00Z"}
}

// Types whose paths are empty or match a diff path are offered; the others
// are not.
func TestOfferFiltersByPaths(t *testing.T) {
	types := []tilelibrary.Type{
		typeOf(t, "anything", nil),
		typeOf(t, "sql", []string{"*.sql"}),
		typeOf(t, "frontend", []string{"web/**/*.{ts,tsx}"}),
		typeOf(t, "docs", []string{"docs/**"}),
	}
	_, offered := tilelibrary.Offer(types, tilelibrary.Offering{Repository: "o/r", DiffPaths: []string{"db/migrations/001.sql", "web/src/app/page.tsx"}, Taken: library.Names})
	if got := names(offered); !slices.Equal(got, []string{"anything", "frontend", "sql"}) {
		t.Errorf("offered %v", got)
	}
}

// The offered types rank by their last use in this repository, then their
// last use anywhere, then the newest; at most MaxOffered are offered.
func TestOfferRanksAndCaps(t *testing.T) {
	var types []tilelibrary.Type
	for i := range 9 {
		ty := typeOf(t, fmt.Sprintf("type_%d", i), nil)
		ty.Created = time.Date(2026, 10, 1+i, 0, 0, 0, 0, time.UTC).Format(time.RFC3339Nano)
		types = append(types, ty)
	}
	types[2].UsedIn = map[string]string{"o/r": "2026-11-01T10:00:00Z", "o/other": "2026-11-09T10:00:00Z"}
	types[2].UsedAt = "2026-11-09T10:00:00Z"
	types[5].UsedIn = map[string]string{"o/r": "2026-11-03T10:00:00Z"}
	types[5].UsedAt = "2026-11-03T10:00:00Z"
	types[7].UsedIn = map[string]string{"o/other": "2026-11-08T10:00:00Z"}
	types[7].UsedAt = "2026-11-08T10:00:00Z"
	_, offered := tilelibrary.Offer(types, tilelibrary.Offering{Repository: "o/r", Taken: library.Names})
	want := []string{"type_5", "type_2", "type_7", "type_8", "type_6", "type_4"}
	if got := names(offered); !slices.Equal(got, want) {
		t.Errorf("offered %v, want %v", got, want)
	}
	if tilelibrary.MaxOffered != 6 {
		t.Errorf("MaxOffered is %d", tilelibrary.MaxOffered)
	}
}

// Pinned types are always filled, first and in pin order, and are not also
// offered; with the built-in types they stay within tiles.MaxLibraryTiles,
// which leaves the offered types the room the pins do not take.
func TestOfferPinsAlwaysAndTheCapHolds(t *testing.T) {
	var types []tilelibrary.Type
	for i := range 9 {
		types = append(types, typeOf(t, fmt.Sprintf("type_%d", i), nil))
	}
	always, offered := tilelibrary.Offer(types, tilelibrary.Offering{Repository: "o/r", Pinned: []string{"type_4", "type_1", "missing", "type_4"}, Taken: library.Names})
	if got := names(always); !slices.Equal(got, []string{"type_4", "type_1"}) {
		t.Errorf("always %v", got)
	}
	if slices.Contains(names(offered), "type_4") || slices.Contains(names(offered), "type_1") {
		t.Errorf("a pinned type is offered too: %v", names(offered))
	}
	if n := len(library.Names) + len(always) + len(offered); n != tiles.MaxLibraryTiles {
		t.Errorf("%d library types in all, want %d", n, tiles.MaxLibraryTiles)
	}
	// Pins past the room are left out; nothing is offered beside them.
	var all []string
	for _, ty := range types {
		all = append(all, ty.Name)
	}
	always, offered = tilelibrary.Offer(types, tilelibrary.Offering{Pinned: all, Taken: append(slices.Clone(library.Names), "schema_change")})
	if len(always) != tiles.MaxLibraryTiles-len(library.Names)-1 || len(offered) != 0 {
		t.Errorf("always %v, offered %v", names(always), names(offered))
	}
	// A pinned type is filled whatever its paths.
	sql := typeOf(t, "sql", []string{"*.sql"})
	always, _ = tilelibrary.Offer([]tilelibrary.Type{sql}, tilelibrary.Offering{Pinned: []string{"sql"}, DiffPaths: []string{"main.go"}})
	if len(always) != 1 {
		t.Errorf("the pinned type was not filled outside its paths")
	}
}

// A type not checked yet, or deleted, is neither offered nor filled when
// pinned.
func TestOfferLeavesOutUncheckedAndDeleted(t *testing.T) {
	unchecked, deleted, fine := typeOf(t, "unchecked", nil), typeOf(t, "deleted", nil), typeOf(t, "fine", nil)
	unchecked.Unchecked = true
	deleted.Deleted = "2026-11-01T00:00:00Z"
	types := []tilelibrary.Type{unchecked, deleted, fine}
	always, offered := tilelibrary.Offer(types, tilelibrary.Offering{Pinned: []string{"unchecked", "deleted"}, Taken: library.Names})
	if len(always) != 0 || !slices.Equal(names(offered), []string{"fine"}) {
		t.Errorf("always %v, offered %v", names(always), names(offered))
	}
}

// The store offers what the library holds: an imported type not checked yet
// only once a check marks it.
func TestStoreOfferReadsTheLibrary(t *testing.T) {
	s, _ := newStore(t)
	ctx := context.Background()
	mustCreate(t, s, change(t, "made_in_chat", ""))
	imported := mustCreate(t, s, change(t, "imported", ""))
	if err := s.MarkChecked(ctx, imported.Entry.Definition.Digest, tilelibrary.VerdictUnchecked); err != nil {
		t.Fatal(err)
	}
	offering := tilelibrary.Offering{Repository: "o/r", DiffPaths: []string{"db/001.sql"}, Taken: library.Names}
	_, offered, err := s.Offer(ctx, offering)
	if err != nil || !slices.Equal(names(offered), []string{"made_in_chat"}) {
		t.Fatalf("offered %v, %v", names(offered), err)
	}
	if err := s.MarkChecked(ctx, imported.Entry.Definition.Digest, tilelibrary.VerdictPass); err != nil {
		t.Fatal(err)
	}
	if _, offered, _ = s.Offer(ctx, offering); len(offered) != 2 {
		t.Errorf("a checked type is not offered: %v", names(offered))
	}
	// An empty library, or none, offers nothing.
	empty, _ := newStore(t)
	if always, offered, err := empty.Offer(ctx, offering); err != nil || len(always)+len(offered) != 0 {
		t.Errorf("an empty library offered %v %v %v", always, offered, err)
	}
}

func TestMatchPath(t *testing.T) {
	cases := []struct {
		glob, path string
		want       bool
	}{
		{"*.sql", "db/migrations/001.sql", true},
		{"*.sql", "001.sql", true},
		{"db/*.sql", "db/001.sql", true},
		{"db/*.sql", "db/migrations/001.sql", false},
		{"db/**/*.sql", "db/001.sql", true},
		{"db/**/*.sql", "db/a/b/001.sql", true},
		{"**/config/*.go", "internal/config/config.go", true},
		{"**/*.{ts,tsx}", "web/a.tsx", true},
		{"**/*.{ts,tsx}", "web/a.go", false},
		{"{cmd,internal}/**", "internal/x/y.go", true},
		{"{cmd,internal}/**", "pkg/x.go", false},
		{"/internal/**", "internal/a.go", true},
		{"src/[ab].go", "src/a.go", true},
		{"src/{a", "src/{a", true},
	}
	for _, c := range cases {
		if got := tilelibrary.MatchPath(c.glob, c.path); got != c.want {
			t.Errorf("MatchPath(%q, %q) = %v", c.glob, c.path, got)
		}
	}
	if !tilelibrary.Applies(nil, nil) || tilelibrary.Applies([]string{"*.sql"}, []string{"a.go"}) || !tilelibrary.Applies([]string{"*.go", "*.sql"}, []string{"a.sql"}) {
		t.Error("Applies")
	}
}

// A stored version's ready fills of saved types are recorded: used_at and
// used_in, which the library page shows and offering ranks by.
func TestRecordUse(t *testing.T) {
	s, clock := newStore(t)
	ctx := context.Background()
	saved := mustCreate(t, s, change(t, "migrations", ""))
	used := []string{saved.Entry.Definition.Name}
	if err := s.RecordUse(ctx, "o/r", append(used, "unknown")); err != nil {
		t.Fatal(err)
	}
	first := clock.now.Format(time.RFC3339Nano)
	clock.now = clock.now.Add(time.Hour)
	if err := s.RecordUse(ctx, "o/other", used); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(ctx, "migrations")
	if err != nil {
		t.Fatal(err)
	}
	second := clock.now.Format(time.RFC3339Nano)
	if got.UsedAt != second || got.UsedIn["o/r"] != first || got.UsedIn["o/other"] != second || len(got.UsedIn) != 2 {
		t.Errorf("used at %s in %v", got.UsedAt, got.UsedIn)
	}
	// The most recent MaxUsedIn repositories are kept.
	for i := range tilelibrary.MaxUsedIn + 3 {
		clock.now = clock.now.Add(time.Minute)
		if err := s.RecordUse(ctx, fmt.Sprintf("o/r%d", i), used); err != nil {
			t.Fatal(err)
		}
	}
	got, _ = s.Get(ctx, "migrations")
	if len(got.UsedIn) != tilelibrary.MaxUsedIn || got.UsedIn["o/r"] != "" || got.UsedIn[fmt.Sprintf("o/r%d", tilelibrary.MaxUsedIn+2)] == "" {
		t.Errorf("used in %d repositories", len(got.UsedIn))
	}
	// A deleted type is left alone.
	if err := s.Delete(ctx, "migrations"); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordUse(ctx, "o/deleted", used); err != nil {
		t.Fatal(err)
	}
}

// A glob of many brace groups is matched in bounded time: its expansion
// stops at its bound, and a save refuses a glob past the bounds with a
// reason, so no stored glob is silently cut short.
func TestPathologicalGlobsAreBounded(t *testing.T) {
	glob := strings.Repeat("{a,}", 50) + "x"
	started := time.Now()
	for range 100 {
		tilelibrary.MatchPath(glob, "src/main.go")
		tilelibrary.Applies([]string{glob}, []string{"a.go", "b.go", "aaax"})
	}
	if took := time.Since(started); took > 2*time.Second {
		t.Errorf("matching took %s", took)
	}
	if !tilelibrary.MatchPath(strings.Repeat("{a,}", 50)+"x", strings.Repeat("a", 50)+"x") {
		t.Error("a pattern within the bound does not match")
	}
	for glob, ok := range map[string]bool{
		strings.Repeat("{a,}", 50) + "x": false,
		strings.Repeat("{a,b}", 7) + "x": false, // 128 patterns
		strings.Repeat("{a,b}", 6) + "x": true,  // 64 patterns
		"**/*.{ts,tsx,js,jsx}":           true,
		strings.Repeat("{a}", 9):         false,
	} {
		if err := tilelibrary.CheckGlob(glob); (err == nil) != ok {
			t.Errorf("CheckGlob(%q): %v", glob, err)
		}
	}
	c := change(t, "braces", "")
	c.Paths = []string{strings.Repeat("{a,}", 49) + "x"}
	if _, err := tilelibrary.Prepare(c); err == nil || !strings.Contains(err.Error(), "braces") {
		t.Errorf("Prepare: %v", err)
	}
}
