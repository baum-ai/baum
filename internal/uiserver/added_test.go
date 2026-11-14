package uiserver

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/baum-ai/baum/internal/agentcontext"
	"github.com/baum-ai/baum/internal/appdb/appdbtest"
	"github.com/baum-ai/baum/internal/tilelayout"
	"github.com/baum-ai/baum/internal/tileview"
	"github.com/baum-ai/baum/tiles"
	"github.com/baum-ai/baum/tiles/library"
)

// fakeFiller records the fills asked for.
type fakeFiller struct {
	mu       sync.Mutex
	requests []tilelayout.FillRequest
}

func (f *fakeFiller) Fill(request tilelayout.FillRequest) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, request)
}

func (f *fakeFiller) asked() []tilelayout.FillRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]tilelayout.FillRequest(nil), f.requests...)
}

func newAddedFixture(t *testing.T) (*layoutFixture, *fakeFiller) {
	t.Helper()
	f := &layoutFixture{}
	filler := &fakeFiller{}
	service := &tilelayout.Service{Path: filepath.Join(appdbtest.TempDir(t), "app.sqlite"), Changed: func(pr string, revision int64) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.events = append(f.events, fmt.Sprintf("%s@%d", pr, revision))
	}}
	f.tilesFixture = newTilesFixture(t, func(a *LocalAPI) { a.Layouts = service; a.Fills = filler; f.api = a })
	return f, filler
}

type addedBody struct {
	Tiles *tiles.Document       `json:"tiles"`
	Added []tileview.AddedNote `json:"added"`
}

func (f *layoutFixture) added(t *testing.T, query string) addedBody {
	t.Helper()
	w := f.get("/api/tiles", query, "")
	if w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	var body addedBody
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	return body
}

func tileByID(doc *tiles.Document, id string) *tiles.Tile {
	for i := range doc.Tiles {
		if doc.Tiles[i].ID == id {
			return &doc.Tiles[i]
		}
	}
	return nil
}

// A library type added from the page's route with no data is pending and
// being filled, and a fill starts for the latest version. Once the fill is
// stored the page is told (the layout event) and the tile shows its data. A
// new version shows that data, marked as from the earlier version, while it
// is filled again; the earlier version shows its own.
func TestAnAddedTileIsPendingThenFilledAndRefilled(t *testing.T) {
	f, filler := newAddedFixture(t)
	f.put(t, titled(t, "First title"))
	edit := layoutOf(t, f.action("edit", `,"ops":[{"op":"add","add":{"kind":"library","name":"api_change"},"to":{"edge":"first"}}]`))
	if len(edit.Applied) != 1 || edit.Applied[0] != "Added Interface changes" {
		t.Fatalf("add: %+v", edit)
	}
	body := f.added(t, "")
	tile := tileByID(body.Tiles, "api_change")
	if tile == nil || tile.State != tiles.StatePending || body.Tiles.Layout.Entries[0].Tile != "api_change" {
		t.Fatalf("the added tile: %+v", tile)
	}
	if len(body.Added) != 1 || !body.Added[0].Filling || body.Added[0].From != 0 {
		t.Errorf("notes: %+v", body.Added)
	}
	if !strings.Contains(string(tile.Definition), `"builtin":true`) {
		t.Errorf("an added built-in type is not marked built in: %s", tile.Definition)
	}
	asked := filler.asked()
	if len(asked) != 1 || asked[0].Version != 1 || asked[0].Type != "api_change" || asked[0].Number != 42 {
		t.Fatalf("fills asked: %+v", asked)
	}

	// The fill lands.
	diff := library.DiffOf(body.Tiles)
	if len(diff) == 0 {
		t.Fatal("the review has no hunks")
	}
	entry, _ := library.Get("api_change")
	data := json.RawMessage(fmt.Sprintf(`{"endpoints":[],"docs":null,"consumers":[{"label":"the client","updated":true,"cites":[{"type":"hunk","id":%q}]}]}`, diff[0].ID))
	events := len(f.events)
	if err := f.api.Layouts.PutFill(context.Background(), "acme/widgets#42", "api_change", 1, entry.Encoded, data); err != nil {
		t.Fatal(err)
	}
	if len(f.events) != events+1 {
		t.Error("a stored fill told the page nothing")
	}
	body = f.added(t, "")
	if tile := tileByID(body.Tiles, "api_change"); tile == nil || tile.State != tiles.StateReady || !strings.Contains(string(tile.Data), "the client") || len(body.Added) != 0 {
		t.Fatalf("after the fill: %+v %+v", tile, body.Added)
	}

	// A new version: the last data, marked, while it is filled again.
	f.put(t, titled(t, "Second title"))
	body = f.added(t, "")
	if tile := tileByID(body.Tiles, "api_change"); tile == nil || tile.State != tiles.StateReady || !strings.Contains(string(tile.Data), "the client") {
		t.Fatalf("the new version's tile: %+v", tile)
	}
	if len(body.Added) != 1 || body.Added[0].From != 1 || !body.Added[0].Filling {
		t.Errorf("the new version's notes: %+v", body.Added)
	}
	if asked := filler.asked(); len(asked) != 2 || asked[1].Version != 2 {
		t.Errorf("fills asked: %+v", asked)
	}
	body = f.added(t, "&version=1")
	if len(body.Added) != 0 || tileByID(body.Tiles, "api_change") == nil {
		t.Errorf("the first version: %+v", body.Added)
	}
	if asked := filler.asked(); len(asked) != 2 {
		t.Errorf("an earlier version asked for a fill: %+v", asked)
	}
}

// add_tile with data the agent wrote stores it as the latest version's fill
// and shows the tile at once with no fill run; data citing a hunk the
// version does not have is refused, naming the place only, and changes
// nothing.
func TestAddTileThroughTheLayoutEditor(t *testing.T) {
	f, filler := newAddedFixture(t)
	f.put(t, titled(t, "First title"))
	editor := LayoutEditor{API: *f.api}
	diff := library.DiffOf(f.added(t, "").Tiles)
	bad := json.RawMessage(`{"endpoints":[],"docs":null,"consumers":[{"label":"SENTINEL","updated":true,"cites":[{"type":"hunk","id":"h_000000000000"}]}]}`)
	_, err := editor.Add(context.Background(), "acme/widgets", 42, agentcontext.TileAdd{Type: "api_change", Data: bad})
	if err == nil || !strings.Contains(err.Error(), "/consumers/0/cites/0 names a hunk that is not in the diff") || strings.Contains(err.Error(), "SENTINEL") {
		t.Fatalf("bad data: %v", err)
	}
	if summary := layoutOf(t, f.get("/api/tiles/layout", "", "")); summary.Edited {
		t.Fatal("a refused add changed the layout")
	}
	good := json.RawMessage(fmt.Sprintf(`{"endpoints":[],"docs":null,"consumers":[{"label":"the client","updated":true,"cites":[{"type":"hunk","id":%q}]}]}`, diff[0].ID))
	added, err := editor.Add(context.Background(), "acme/widgets", 42, agentcontext.TileAdd{Type: "Interface changes", Data: good})
	if err != nil || added.State != "ready" || added.Tile != "api_change" {
		t.Fatalf("add: %+v %v", added, err)
	}
	body := f.added(t, "")
	if tile := tileByID(body.Tiles, "api_change"); tile == nil || tile.State != tiles.StateReady || len(body.Added) != 0 {
		t.Errorf("the tile: %+v %+v", tile, body.Added)
	}
	if len(filler.asked()) != 0 {
		t.Errorf("data the agent wrote started a fill: %+v", filler.asked())
	}
	if _, err := editor.Add(context.Background(), "acme/widgets", 42, agentcontext.TileAdd{Type: "weather"}); err == nil || !strings.Contains(err.Error(), "No tile type weather") {
		t.Errorf("an unknown type: %v", err)
	}
}
