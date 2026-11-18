package addedtiles_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/baum-ai/baum/bundle"
	"github.com/baum-ai/baum/internal/addedtiles"
	"github.com/baum-ai/baum/tiles/kit"
	"github.com/baum-ai/baum/tiles/library"
)

// A baum.review/2 version holds the types of its ready dedicated tiles
// filled, so an added tile of a type its generation filled (a pinned or
// added type always is) shows once, as the generated tile.
func TestGeneratedReadsAReviewV2Document(t *testing.T) {
	entry, _ := library.Get("api_change")
	schema, _ := library.Get("schema_change")
	doc, err := json.Marshal(map[string]any{"schema": "baum.review/2", "tiles": []map[string]any{
		{"id": "tl_api_change", "type": "api_change", "owner": "sec_a", "state": "ready", "definition": json.RawMessage(entry.Encoded)},
		{"id": "tl_schema_change", "type": "schema_change", "owner": "sec_a", "state": "failed", "definition": json.RawMessage(schema.Encoded)},
		{"id": "tl_flow", "type": "flow", "owner": "sec_a", "state": "ready"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	got := addedtiles.Generated(nil, doc)
	if !got["api_change"] || got["schema_change"] || len(got) != 1 {
		t.Errorf("generated: %v", got)
	}
	if got := addedtiles.Generated(nil, []byte(`{"schema":"baum.review/1"}`)); len(got) != 0 {
		t.Errorf("a baum.review/1 document without a composition: %v", got)
	}
}

func apiData(hunk string) json.RawMessage {
	return json.RawMessage(fmt.Sprintf(`{"endpoints":[],"docs":null,"consumers":[{"label":"x","updated":true,"cites":[{"type":"hunk","id":%q}]}]}`, hunk))
}

// The fills of the two newest versions of an added tile are kept; a fill
// tells the page. The sweep drops the fills of a pull request without added
// tiles and the definitions nothing names.
func TestFillsAreKeptForTwoVersionsAndSwept(t *testing.T) {
	ctx := context.Background()
	s, e := newService(t)
	addAPI(t, s, "")
	entry, _ := library.Get("api_change")
	for version := 1; version <= 3; version++ {
		if err := s.PutFill(ctx, pr, "api_change", version, addedtiles.OriginFill, entry.Encoded, apiData("h_0123456789ab")); err != nil {
			t.Fatal(err)
		}
	}
	fills, err := s.Fills(ctx, pr)
	if err != nil || len(fills) != 2 || fills[0].Version != 3 || fills[1].Version != 2 {
		t.Fatalf("fills %+v: %v", fills, err)
	}
	if len(e.seen) != 4 {
		t.Errorf("events %v", e.seen)
	}
	stale := entry.Encoded
	var def map[string]any
	_ = json.Unmarshal(stale, &def)
	def["digest"] = "sha256:0000"
	orphan, _ := json.Marshal(def)
	if err := s.PutFill(ctx, "acme/other#1", "api_change", 1, addedtiles.OriginChat, orphan, apiData("h_0123456789ab")); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Definition(ctx, "sha256:0000"); !ok {
		t.Fatal("the definition was not kept")
	}
	if err := s.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	if fills, _ := s.Fills(ctx, "acme/other#1"); len(fills) != 0 {
		t.Errorf("the fills of a pull request without added tiles: %+v", fills)
	}
	if _, ok := s.Definition(ctx, "sha256:0000"); ok {
		t.Error("a definition nothing names was kept")
	}
	if _, ok := s.Definition(ctx, entry.Definition.Digest); !ok {
		t.Error("the added type's definition was swept")
	}
	if err := (&addedtiles.Service{Path: s.Path + ".missing"}).Sweep(ctx); err != nil {
		t.Errorf("sweeping no database: %v", err)
	}
}

// AddedFor shows a version's own fill; on the latest version an earlier
// fill whose hunks are still there, marked and filling; a pending tile when
// the earlier fill no longer resolves (it is dropped); and nothing for a
// type generation filled or the library does not have.
func TestAddedFor(t *testing.T) {
	entry, _ := library.Get("api_change")
	added := []addedtiles.Added{{Tile: "api_change", Kind: "library", Name: "api_change", Section: "sec_api"}, {Tile: "weather", Kind: "library", Name: "weather"}}
	diff := []bundle.Hunk{{ID: "h_0123456789ab", Path: "api/routes.go", StartLine: 1, LineCount: 10}}
	none := func(string) (json.RawMessage, bool) { return nil, false }
	own := []addedtiles.Fill{{Tile: "api_change", Version: 2, Digest: entry.Definition.Digest, Data: apiData("h_0123456789ab")}}
	held, missing := addedtiles.AddedFor(added, own, addedtiles.Version{Number: 2, Latest: true, Diff: diff}, none)
	if len(held) != 1 || held[0].Tile.Pending || held[0].From != 0 || held[0].Filling || held[0].Section != "sec_api" || len(missing) != 0 {
		t.Errorf("the version's own fill: %+v %v", held, missing)
	}
	held, missing = addedtiles.AddedFor(added, own, addedtiles.Version{Number: 3, Latest: true, Diff: diff}, none)
	if len(held) != 1 || held[0].Tile.Pending || held[0].From != 2 || !held[0].Filling || len(missing) != 1 {
		t.Errorf("an earlier fill: %+v %v", held, missing)
	}
	moved := []bundle.Hunk{{ID: "h_ba9876543210", Path: "api/routes.go", StartLine: 1, LineCount: 10}}
	held, missing = addedtiles.AddedFor(added, own, addedtiles.Version{Number: 3, Latest: true, Diff: moved}, none)
	if len(held) != 1 || !held[0].Tile.Pending || held[0].From != 0 || len(missing) != 1 {
		t.Errorf("a dropped fill: %+v %v", held, missing)
	}
	held, missing = addedtiles.AddedFor(added, own, addedtiles.Version{Number: 3, Diff: moved}, none)
	if len(held) != 0 || len(missing) != 0 {
		t.Errorf("an earlier version without data: %+v %v", held, missing)
	}
	held, missing = addedtiles.AddedFor(added, nil, addedtiles.Version{Number: 3, Latest: true, Generated: map[string]bool{"api_change": true}, Diff: diff}, none)
	if len(held) != 0 || len(missing) != 0 {
		t.Errorf("a generated type: %+v %v", held, missing)
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
	addAPI(t, s, "")
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

// A fill records who wrote it; a draft shows an earlier fill with the
// version it is from.
func TestFillOriginAndDraftNotes(t *testing.T) {
	ctx := context.Background()
	s, _ := newService(t)
	addAPI(t, s, "")
	entry, _ := library.Get("api_change")
	if err := s.PutFill(ctx, pr, "api_change", 2, addedtiles.OriginChat, entry.Encoded, apiData("h_0123456789ab")); err != nil {
		t.Fatal(err)
	}
	if err := s.PutFill(ctx, pr, "api_change", 3, "someone", entry.Encoded, apiData("h_0123456789ab")); err == nil {
		t.Error("a fill of no known origin was stored")
	}
	fills, err := s.Fills(ctx, pr)
	if err != nil || len(fills) != 1 || fills[0].Origin != addedtiles.OriginChat {
		t.Fatalf("fills %+v: %v", fills, err)
	}
	held := s.Added("acme/widgets", 7)
	if len(held) != 1 || held[0].From != 2 {
		t.Errorf("draft's added tiles: %+v", held)
	}
}

// madeInChat is a type made in chat, the kit's starter under its own name,
// with or without fill instructions, as a kept definition and its add.
func madeInChat(t *testing.T, fill string) (addedtiles.Added, json.RawMessage) {
	t.Helper()
	e, err := library.Load(kit.Starter())
	if err != nil {
		t.Fatal(err)
	}
	def := e.Definition
	def.Name, def.Fill = "env_vars", fill
	encoded, err := library.Seal(&def)
	if err != nil {
		t.Fatal(err)
	}
	return addedtiles.Added{Tile: "env_vars", Kind: addedtiles.KindCustom, Name: "env_vars", Digest: def.Digest}, encoded
}

func starterData(hunk string) json.RawMessage {
	return json.RawMessage(fmt.Sprintf(`{"summary":"One variable.","items":[{"title":"PORT","severity":"low","text":null,"cites":[{"type":"hunk","id":%q}]}]}`, hunk))
}

// A tile made in chat shows its own version's data. On a later version it
// is never filled again, with or without fill instructions (those came from
// the session agent, not the person), so every later version shows its newest
// data, marked as from its version, and never as filling.
// Its definition gone, it shows nothing.
func TestAddedForATileMadeInChat(t *testing.T) {
	diff := []bundle.Hunk{{ID: "h_0123456789ab", Path: "config.go", StartLine: 1, LineCount: 10}}
	for _, fill := range []string{"List each new environment variable with its default.", ""} {
		add, encoded := madeInChat(t, fill)
		kept := func(digest string) (json.RawMessage, bool) { return encoded, digest == add.Digest }
		own := []addedtiles.Fill{{Tile: "env_vars", Version: 1, Digest: add.Digest, Data: starterData("h_0123456789ab"), Origin: addedtiles.OriginChat}}
		held, missing := addedtiles.AddedFor([]addedtiles.Added{add}, own, addedtiles.Version{Number: 1, Latest: true, Diff: diff}, kept)
		if len(held) != 1 || held[0].Tile.Pending || held[0].From != 0 || len(missing) != 0 {
			t.Errorf("fill %q, its own version: %+v %v", fill, held, missing)
		}
		held, missing = addedtiles.AddedFor([]addedtiles.Added{add}, own, addedtiles.Version{Number: 2, Latest: true, Diff: diff}, kept)
		if len(held) != 1 || held[0].From != 1 {
			t.Fatalf("fill %q, a later version: %+v %v", fill, held, missing)
		}
		if held[0].Filling || len(missing) != 0 {
			t.Errorf("a chat tile is filled: %+v %v", held, missing)
		}
		moved := []bundle.Hunk{{ID: "h_ba9876543210", Path: "config.go", StartLine: 1, LineCount: 10}}
		held, _ = addedtiles.AddedFor([]addedtiles.Added{add}, own, addedtiles.Version{Number: 2, Latest: true, Diff: moved}, kept)
		if len(held) != 1 || held[0].Tile.Pending || held[0].From != 1 {
			t.Errorf("a tile without fill instructions whose citations moved: %+v", held)
		}
		gone := func(string) (json.RawMessage, bool) { return nil, false }
		if held, missing = addedtiles.AddedFor([]addedtiles.Added{add}, own, addedtiles.Version{Number: 2, Latest: true, Diff: diff}, gone); len(held) != 0 || len(missing) != 0 {
			t.Errorf("fill %q, its definition gone: %+v %v", fill, held, missing)
		}
	}
}

// A fill a refused add wrote is taken back: deleted when the tile had none
// on that version, and the earlier one put back when it had.
func TestRestoreFill(t *testing.T) {
	ctx := context.Background()
	s, _ := newService(t)
	add, encoded := madeInChat(t, "")
	if err := s.PutFill(ctx, pr, "env_vars", 1, addedtiles.OriginChat, encoded, starterData("h_0123456789ab")); err != nil {
		t.Fatal(err)
	}
	prior := s.HeldFill(ctx, pr, "env_vars", 1)
	if prior == nil {
		t.Fatal("no held fill")
	}
	if err := s.PutFill(ctx, pr, "env_vars", 1, addedtiles.OriginChat, encoded, starterData("h_ba9876543210")); err != nil {
		t.Fatal(err)
	}
	s.RestoreFill(ctx, pr, "env_vars", 1, prior)
	fills, err := s.Fills(ctx, pr)
	if err != nil || len(fills) != 1 || fills[0].Digest != add.Digest || !strings.Contains(string(fills[0].Data), "h_0123456789ab") {
		t.Fatalf("the earlier fill is not back: %+v %v", fills, err)
	}
	s.RestoreFill(ctx, pr, "env_vars", 1, nil)
	if fills, err = s.Fills(ctx, pr); err != nil || len(fills) != 0 {
		t.Fatalf("the fill is not deleted: %+v %v", fills, err)
	}
}
