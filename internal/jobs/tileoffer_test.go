package jobs

import (
	"context"
	"encoding/json"
	"path/filepath"
	"slices"
	"testing"

	"github.com/baum-ai/baum/agent"
	"github.com/baum-ai/baum/internal/appdb/appdbtest"
	"github.com/baum-ai/baum/internal/generate"
	"github.com/baum-ai/baum/internal/repos"
	"github.com/baum-ai/baum/internal/store"
	"github.com/baum-ai/baum/internal/tilelibrary"
	"github.com/baum-ai/baum/reviewv2"
	"github.com/baum-ai/baum/tiles"
	"github.com/baum-ai/baum/tiles/library"
	"github.com/baum-ai/baum/tiles/view"
)

// savedType saves a type to the library: the built-in schema change type
// under another name, with its synthetic samples.
func savedType(t *testing.T, lib *tilelibrary.Store, name string, paths []string) tilelibrary.Type {
	t.Helper()
	entry, _ := library.Get("schema_change")
	def := entry.Definition
	def.Name, def.Title = name, "Saved "+name
	encoded, err := library.Seal(&def)
	if err != nil {
		t.Fatal(err)
	}
	c := tilelibrary.Change{Definition: encoded, Glance: entry.Glance, AppliesWith: entry.AppliesWith, Paths: paths, Origin: tilelibrary.OriginChat}
	for _, s := range entry.Samples {
		if tilelibrary.IsSynthetic(s) && len(c.Samples) == 0 {
			c.Samples = append(c.Samples, s)
		}
	}
	saved, err := lib.Create(context.Background(), c, nil)
	if err != nil {
		t.Fatal(err)
	}
	return saved
}

// A review of a repository fills the saved types pinned to it, with the last
// data held for each, and offers the other saved types its diff's paths
// match; a stored version's ready fill of a saved type is recorded as a use
// in the repository.
func TestReviewsOfferSavedTypesAndRecordTheirUse(t *testing.T) {
	ctx := context.Background()
	registry, clone := registeredClone(t)
	lib := &tilelibrary.Store{Path: filepath.Join(appdbtest.TempDir(t), "app.sqlite")}
	pinned := savedType(t, lib, "pinned_type", []string{"*.sql"})
	savedType(t, lib, "go_type", []string{"**/*.go"})
	savedType(t, lib, "sql_type", []string{"*.sql"})
	if err := registry.Update(ctx, clone.ID, repos.Settings{Tiles: &repos.TileSettings{Types: []string{"pinned_type"}}}); err != nil {
		t.Fatal(err)
	}
	versions, err := store.Open("", t.TempDir(), true)
	if err != nil {
		t.Fatal(err)
	}
	key := store.Key{Repository: clone.Repository, Number: 7}
	l := &Local{Registry: registry, TileLibrary: lib}
	o := &generate.Tiles{Always: []agent.AlwaysTile{{Name: "api_change"}}}
	l.offerSaved(ctx, o, versions, key, clone.Repository)
	if len(o.Always) != 2 || o.Always[1].Name != "pinned_type" || o.Saved == nil {
		t.Fatalf("always %+v", o.Always)
	}
	always, offered := o.Saved(ctx, []string{"cmd/main.go"}, library.Names)
	if len(always) != 1 || always[0].Definition.Name != "pinned_type" || len(offered) != 1 || offered[0].Definition.Name != "go_type" {
		t.Errorf("always %d, offered %d", len(always), len(offered))
	}
	// Without a library nothing is added.
	plain := &generate.Tiles{}
	(&Local{Registry: registry}).offerSaved(ctx, plain, versions, key, clone.Repository)
	if plain.Saved != nil || len(plain.Always) != 0 {
		t.Errorf("a review without a library offers saved types")
	}

	composition, _ := json.Marshal(view.Composition{Schema: view.CompositionName, Library: []tiles.LibraryTile{{Definition: pinned.Entry.Encoded, Data: json.RawMessage(`{}`)}}})
	l.recordTileUse(ctx, clone.Repository, generate.Result{Composition: composition})
	got, err := lib.Get(ctx, "pinned_type")
	if err != nil || got.UsedAt == "" || got.UsedIn[clone.Repository] == "" {
		t.Errorf("use not recorded: %v %+v", err, got.UsedIn)
	}
	if other, _ := lib.Get(ctx, "go_type"); other.UsedAt != "" || !slices.Equal(tilelibrary.UsedTypes(composition, nil), []string{"pinned_type"}) {
		t.Error("a type the version did not fill was recorded")
	}

	// A baum.review/2 version, which carries no composition, records the
	// ready tiles that embed a definition.
	doc, _ := json.Marshal(map[string]any{"schema": reviewv2.Name, "tiles": []reviewv2.Tile{
		{ID: "tl_a", Type: "go_type", State: reviewv2.TileReady, Definition: json.RawMessage(`{"name":"go_type"}`)},
		{ID: "tl_b", Type: "sql_type", State: reviewv2.TilePending, Definition: json.RawMessage(`{"name":"sql_type"}`)},
		{ID: "tl_c", Type: "diagram", State: reviewv2.TileReady},
	}})
	l.recordTileUse(ctx, clone.Repository, generate.Result{Document: doc})
	if got, _ := lib.Get(ctx, "go_type"); got.UsedIn[clone.Repository] == "" {
		t.Errorf("v2 use not recorded: %+v", got.UsedIn)
	}
	if got, _ := lib.Get(ctx, "sql_type"); got.UsedAt != "" {
		t.Errorf("a pending tile was recorded")
	}
}
