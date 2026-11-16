package tilelayout_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/baum-ai/baum/bundle"
	"github.com/baum-ai/baum/internal/tilelayout"
	"github.com/baum-ai/baum/tiles"
	"github.com/baum-ai/baum/tiles/library"
)

func apiData(hunk string) json.RawMessage {
	return json.RawMessage(fmt.Sprintf(`{"endpoints":[],"docs":null,"consumers":[{"label":"x","updated":true,"cites":[{"type":"hunk","id":%q}]}]}`, hunk))
}

func addAPI(t *testing.T, s *tilelayout.Service) {
	t.Helper()
	entry, _ := library.Get("api_change")
	doc := served(t, tilelayout.State{})
	if _, err := s.Edit(context.Background(), pr, "chat", doc, 0, []tiles.Op{{Op: tiles.OpAdd, Add: &tiles.Added{Kind: "library", Name: "api_change", Digest: entry.Definition.Digest}}}); err != nil {
		t.Fatal(err)
	}
}

// The fills of the two newest versions of an added tile are kept; a fill
// moves the layout's revision and tells the page. The sweep drops the fills
// of a pull request without an overlay and the definitions nothing names.
func TestFillsAreKeptForTwoVersionsAndSwept(t *testing.T) {
	ctx := context.Background()
	s, e := newService(t)
	addAPI(t, s)
	entry, _ := library.Get("api_change")
	for version := 1; version <= 3; version++ {
		if err := s.PutFill(ctx, pr, "api_change", version, tilelayout.OriginFill, entry.Encoded, apiData("h_0123456789ab")); err != nil {
			t.Fatal(err)
		}
	}
	fills, err := s.Fills(ctx, pr)
	if err != nil || len(fills) != 2 || fills[0].Version != 3 || fills[1].Version != 2 {
		t.Fatalf("fills %+v: %v", fills, err)
	}
	if state, _ := s.Read(ctx, pr); state.Revision != 4 || len(e.seen) != 4 {
		t.Errorf("revision %d, events %v", state.Revision, e.seen)
	}
	stale := entry.Encoded
	var def map[string]any
	_ = json.Unmarshal(stale, &def)
	def["digest"] = "sha256:0000"
	orphan, _ := json.Marshal(def)
	if err := s.PutFill(ctx, "acme/other#1", "api_change", 1, tilelayout.OriginChat, orphan, apiData("h_0123456789ab")); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Definition(ctx, "sha256:0000"); !ok {
		t.Fatal("the definition was not kept")
	}
	if err := s.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	if fills, _ := s.Fills(ctx, "acme/other#1"); len(fills) != 0 {
		t.Errorf("the fills of a pull request without an overlay: %+v", fills)
	}
	if _, ok := s.Definition(ctx, "sha256:0000"); ok {
		t.Error("a definition nothing names was kept")
	}
	if _, ok := s.Definition(ctx, entry.Definition.Digest); !ok {
		t.Error("the added type's definition was swept")
	}
	if err := (&tilelayout.Service{Path: s.Path + ".missing"}).Sweep(ctx); err != nil {
		t.Errorf("sweeping no database: %v", err)
	}
}

// AddedFor shows a version's own fill; on the latest version an earlier
// fill whose hunks are still there, marked and filling; a pending tile when
// the earlier fill no longer resolves (it is dropped); and nothing for a
// type generation filled or the library does not have.
func TestAddedFor(t *testing.T) {
	entry, _ := library.Get("api_change")
	added := []tiles.Added{{Kind: "library", Name: "api_change"}, {Kind: "library", Name: "weather"}}
	diff := []bundle.Hunk{{ID: "h_0123456789ab", Path: "api/routes.go", StartLine: 1, LineCount: 10}}
	none := func(string) (json.RawMessage, bool) { return nil, false }
	own := []tilelayout.Fill{{Tile: "api_change", Version: 2, Digest: entry.Definition.Digest, Data: apiData("h_0123456789ab")}}
	layout, missing := tilelayout.AddedFor(added, own, tilelayout.Version{Number: 2, Latest: true, Diff: diff}, none)
	if len(layout.Added) != 1 || layout.Added[0].Pending || len(layout.Notes) != 0 || len(missing) != 0 {
		t.Errorf("the version's own fill: %+v %v", layout, missing)
	}
	layout, missing = tilelayout.AddedFor(added, own, tilelayout.Version{Number: 3, Latest: true, Diff: diff}, none)
	if len(layout.Added) != 1 || layout.Added[0].Pending || len(layout.Notes) != 1 || layout.Notes[0].From != 2 || !layout.Notes[0].Filling || len(missing) != 1 {
		t.Errorf("an earlier fill: %+v %v", layout, missing)
	}
	moved := []bundle.Hunk{{ID: "h_ba9876543210", Path: "api/routes.go", StartLine: 1, LineCount: 10}}
	layout, missing = tilelayout.AddedFor(added, own, tilelayout.Version{Number: 3, Latest: true, Diff: moved}, none)
	if len(layout.Added) != 1 || !layout.Added[0].Pending || layout.Notes[0].From != 0 || len(missing) != 1 {
		t.Errorf("a dropped fill: %+v %v", layout, missing)
	}
	layout, missing = tilelayout.AddedFor(added, own, tilelayout.Version{Number: 3, Diff: moved}, none)
	if len(layout.Added) != 0 || len(missing) != 0 {
		t.Errorf("an earlier version without data: %+v %v", layout, missing)
	}
	layout, missing = tilelayout.AddedFor(added, nil, tilelayout.Version{Number: 3, Latest: true, Generated: map[string]bool{"api_change": true}, Diff: diff}, none)
	if len(layout.Added) != 0 || len(missing) != 0 {
		t.Errorf("a generated type: %+v %v", layout, missing)
	}
}

// The tile library's DDL (saved tile types), which keeps its definitions in
// tile_definitions too.
const tileLibrarySchema = `CREATE TABLE IF NOT EXISTS tile_types(
  name TEXT PRIMARY KEY,
  digest TEXT NOT NULL,
  glance TEXT NOT NULL,
  review_checks TEXT NOT NULL,
  applies_with TEXT NOT NULL,
  paths TEXT NOT NULL,
  samples BLOB NOT NULL,
  origin TEXT NOT NULL,
  created TEXT NOT NULL,
  updated TEXT NOT NULL,
  used_at TEXT,
  used_in TEXT,
  deleted TEXT);
CREATE TABLE IF NOT EXISTS tile_type_versions(
  name TEXT NOT NULL,
  digest TEXT NOT NULL,
  at TEXT NOT NULL,
  PRIMARY KEY(name, digest))`

// The sweep keeps the definitions the tile library names, in its types and
// their versions, and removes none when it cannot read them.
func TestSweepKeepsTheTileLibrarysDefinitions(t *testing.T) {
	ctx := context.Background()
	s, _ := newService(t)
	addAPI(t, s)
	db, err := sql.Open("sqlite", s.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.ExecContext(ctx, tileLibrarySchema); err != nil {
		t.Fatal(err)
	}
	for _, digest := range []string{"sha256:saved", "sha256:earlier", "sha256:nothing"} {
		if _, err := db.ExecContext(ctx, `INSERT INTO tile_definitions(digest, definition, at) VALUES (?, '{}', 'now')`, digest); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO tile_types(name, digest, glance, review_checks, applies_with, paths, samples, origin, created, updated) VALUES ('mine', 'sha256:saved', '', '', '[]', '[]', '[]', 'chat', 'now', 'now')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO tile_type_versions(name, digest, at) VALUES ('mine', 'sha256:earlier', 'now')`); err != nil {
		t.Fatal(err)
	}
	if err := s.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	for digest, kept := range map[string]bool{"sha256:saved": true, "sha256:earlier": true, "sha256:nothing": false} {
		if _, ok := s.Definition(ctx, digest); ok != kept {
			t.Errorf("%s kept %v, want %v", digest, ok, kept)
		}
	}
	// A tile library this build cannot read keeps every definition.
	if _, err := db.ExecContext(ctx, `DROP TABLE tile_type_versions; CREATE TABLE tile_type_versions(name TEXT); INSERT INTO tile_definitions(digest, definition, at) VALUES ('sha256:unread', '{}', 'now')`); err != nil {
		t.Fatal(err)
	}
	if err := s.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Definition(ctx, "sha256:unread"); !ok {
		t.Error("a definition was removed while the tile library could not be read")
	}
}

// A fill records who wrote it; a draft's layout shows an earlier fill with
// the version it is from.
func TestFillOriginAndDraftNotes(t *testing.T) {
	ctx := context.Background()
	s, _ := newService(t)
	addAPI(t, s)
	entry, _ := library.Get("api_change")
	if err := s.PutFill(ctx, pr, "api_change", 2, tilelayout.OriginChat, entry.Encoded, apiData("h_0123456789ab")); err != nil {
		t.Fatal(err)
	}
	if err := s.PutFill(ctx, pr, "api_change", 3, "someone", entry.Encoded, apiData("h_0123456789ab")); err == nil {
		t.Error("a fill of no known origin was stored")
	}
	fills, err := s.Fills(ctx, pr)
	if err != nil || len(fills) != 1 || fills[0].Origin != tilelayout.OriginChat {
		t.Fatalf("fills %+v: %v", fills, err)
	}
	layout := s.Layout("acme/widgets", 7)
	if len(layout.Added) != 1 || len(layout.Notes) != 1 || layout.Notes[0].From != 2 {
		t.Errorf("draft layout: %+v", layout)
	}
}
