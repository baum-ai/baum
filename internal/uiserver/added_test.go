package uiserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/baum-ai/baum/bundle"
	"github.com/baum-ai/baum/internal/addedtiles"
	"github.com/baum-ai/baum/internal/agentcontext"
	"github.com/baum-ai/baum/internal/appdb/appdbtest"
	"github.com/baum-ai/baum/reviewv2"
	"github.com/baum-ai/baum/tiles"
	"github.com/baum-ai/baum/tiles/library"
)

// fakeFiller records the fills asked for.
type fakeFiller struct {
	mu       sync.Mutex
	requests []addedtiles.FillRequest
}

func (f *fakeFiller) Fill(request addedtiles.FillRequest) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, request)
}

func (f *fakeFiller) asked() []addedtiles.FillRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]addedtiles.FillRequest(nil), f.requests...)
}

// addedFixture is a tiles fixture whose API keeps the tiles people add, and
// the events it published.
type addedFixture struct {
	*tilesFixture
	api    *LocalAPI
	mu     sync.Mutex
	events []string
}

func newAddedFixture(t *testing.T) (*addedFixture, *fakeFiller) {
	t.Helper()
	f := &addedFixture{}
	filler := &fakeFiller{}
	service := &addedtiles.Service{Path: filepath.Join(appdbtest.TempDir(t), "app.sqlite"), Changed: func(pr string) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.events = append(f.events, pr)
	}}
	f.tilesFixture = newTilesFixture(t, func(a *LocalAPI) { a.Added = service; a.Fills = filler; f.api = a })
	return f, filler
}

func (f *addedFixture) eventCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.events)
}

// served is the tile route's document for a version ("" the latest).
func (f *addedFixture) served(t *testing.T, query string) reviewv2.Document {
	t.Helper()
	w := f.get("/api/tiles", query, "")
	if w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	var body struct {
		Document *reviewv2.Document `json:"document"`
		Problem  string             `json:"problem"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || body.Document == nil || body.Problem != "" {
		t.Fatalf("%v: %s", err, w.Body.String())
	}
	return *body.Document
}

func servedTile(d reviewv2.Document, id string) *reviewv2.Tile {
	for i := range d.Tiles {
		if d.Tiles[i].ID == id {
			return &d.Tiles[i]
		}
	}
	return nil
}

// ownerOf is the section (or "context", "overview") a served tile sits on.
func ownerOf(d reviewv2.Document, id string) string {
	if tile := servedTile(d, id); tile != nil {
		return tile.Owner
	}
	return ""
}

// diffOf is the hunks of the latest version the fixture serves.
func (f *addedFixture) diffOf(t *testing.T) []bundle.Hunk {
	t.Helper()
	shown, err := f.api.latestShown(context.Background(), f.clone, 42)
	if err != nil {
		t.Fatal(err)
	}
	diff := library.DiffOf(shown.Document)
	if len(diff) == 0 {
		t.Fatal("the review has no hunks")
	}
	return diff
}

func apiFill(hunk string) json.RawMessage {
	return json.RawMessage(fmt.Sprintf(`{"endpoints":[],"docs":null,"consumers":[{"label":"the client","updated":true,"cites":[{"type":"hunk","id":%q}]}]}`, hunk))
}

// A library type added with no data is pending and being filled, on the
// section asked for, and a fill starts for the latest version. Once the
// fill is stored the page is told and the tile shows its data. A new
// version shows that data, marked as from the earlier version, while it is
// filled again; the earlier version shows its own.
func TestAnAddedTileIsPendingThenFilledAndRefilled(t *testing.T) {
	f, filler := newAddedFixture(t)
	f.put(t, titled(t, "First title"))
	ctx := context.Background()
	added, err := AddedTilesAPI{API: *f.api}.Add(ctx, "acme/widgets", 42, agentcontext.TileAdd{Type: "api_change", Section: "context"})
	if err != nil || added.State != "filling" || added.Section != "context" {
		t.Fatalf("add: %+v %v", added, err)
	}
	v := f.served(t, "")
	tile := servedTile(v, "api_change")
	if tile == nil || tile.State != tiles.StatePending || !tile.Added || !tile.Filling || ownerOf(v, "api_change") != "context" {
		t.Fatalf("the added tile: %+v", tile)
	}
	if !strings.Contains(string(tile.Definition), `"builtin":true`) {
		t.Errorf("an added built-in type is not marked built in: %s", tile.Definition)
	}
	asked := filler.asked()
	if len(asked) < 1 || asked[0].Version != 1 || asked[0].Type != "api_change" || asked[0].Number != 42 {
		t.Fatalf("fills asked: %+v", asked)
	}

	// The fill lands.
	entry, _ := library.Get("api_change")
	events := f.eventCount()
	if err := f.api.Added.PutFill(ctx, "acme/widgets#42", "api_change", 1, addedtiles.OriginFill, entry.Encoded, apiFill(f.diffOf(t)[0].ID)); err != nil {
		t.Fatal(err)
	}
	if f.eventCount() != events+1 {
		t.Error("a stored fill told the page nothing")
	}
	v = f.served(t, "")
	if tile := servedTile(v, "api_change"); tile == nil || tile.State != tiles.StateReady || !strings.Contains(string(tile.Data), "the client") || tile.Filling || tile.From != 0 {
		t.Fatalf("after the fill: %+v", tile)
	}

	// A new version: the last data, marked, while it is filled again.
	f.put(t, titled(t, "Second title"))
	before := len(filler.asked())
	v = f.served(t, "")
	if tile := servedTile(v, "api_change"); tile == nil || tile.State != tiles.StateReady || !strings.Contains(string(tile.Data), "the client") || tile.From != 1 || !tile.Filling {
		t.Fatalf("the new version's tile: %+v", tile)
	}
	if asked := filler.asked(); len(asked) <= before || asked[len(asked)-1].Version != 2 {
		t.Errorf("fills asked: %+v", asked)
	}
	before = len(filler.asked())
	v = f.served(t, "&version=1")
	if tile := servedTile(v, "api_change"); tile == nil || tile.From != 0 || tile.Filling {
		t.Errorf("the first version: %+v", tile)
	}
	if len(filler.asked()) != before {
		t.Errorf("an earlier version asked for a fill: %+v", filler.asked())
	}
}

// add_tile with data the agent wrote stores it as the latest version's fill
// and shows the tile at once with no fill run, on the section its data
// cites most; data citing a hunk the version does not have is refused,
// naming the place only, and changes nothing.
func TestAddTileWithTheAgentsData(t *testing.T) {
	f, filler := newAddedFixture(t)
	f.put(t, titled(t, "First title"))
	adder := AddedTilesAPI{API: *f.api}
	ctx := context.Background()
	bad := json.RawMessage(`{"endpoints":[],"docs":null,"consumers":[{"label":"SENTINEL","updated":true,"cites":[{"type":"hunk","id":"h_000000000000"}]}]}`)
	_, err := adder.Add(ctx, "acme/widgets", 42, agentcontext.TileAdd{Type: "api_change", Data: bad})
	if err == nil || !strings.Contains(err.Error(), "/consumers/0/cites/0 names a hunk that is not in the diff") || strings.Contains(err.Error(), "SENTINEL") {
		t.Fatalf("bad data: %v", err)
	}
	if list, _ := f.api.Added.List(ctx, "acme/widgets#42"); len(list) != 0 {
		t.Fatal("a refused add was kept")
	}
	added, err := adder.Add(ctx, "acme/widgets", 42, agentcontext.TileAdd{Type: "Interface changes", Data: apiFill(f.diffOf(t)[0].ID)})
	if err != nil || added.State != "ready" || added.Tile != "api_change" || added.Section == "" {
		t.Fatalf("add: %+v %v", added, err)
	}
	v := f.served(t, "")
	if tile := servedTile(v, "api_change"); tile == nil || tile.State != tiles.StateReady || !tile.Added || tile.Filling || ownerOf(v, "api_change") != added.Section {
		t.Errorf("the tile: %+v on %s", tile, ownerOf(v, "api_change"))
	}
	if len(filler.asked()) != 0 {
		t.Errorf("data the agent wrote started a fill: %+v", filler.asked())
	}
	if _, err := adder.Add(ctx, "acme/widgets", 42, agentcontext.TileAdd{Type: "weather"}); err == nil || !strings.Contains(err.Error(), "No tile type weather") {
		t.Errorf("an unknown type: %v", err)
	}
}

// The page's Remove takes an added tile off every version, and its Undo
// puts it back on its section; each write tells the page. A tile that is
// not added is 404, and a malformed request 400.
func TestRemoveAndRestoreAnAddedTile(t *testing.T) {
	f, _ := newAddedFixture(t)
	f.put(t, titled(t, "First title"))
	ctx := context.Background()
	if _, err := (AddedTilesAPI{API: *f.api}).Add(ctx, "acme/widgets", 42, agentcontext.TileAdd{Type: "api_change", Section: "context"}); err != nil {
		t.Fatal(err)
	}
	post := func(body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "http://local/api/tiles/added", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		f.handler.ServeHTTP(w, r)
		return w
	}
	action := func(action, tile string) *httptest.ResponseRecorder {
		return post(fmt.Sprintf(`{"clone":%q,"pr":42,"tile":%q,"action":%q}`, f.clone.ID, tile, action))
	}
	events := f.eventCount()
	if w := action("remove", "api_change"); w.Code != 200 || !strings.Contains(w.Body.String(), `"action":"remove"`) {
		t.Fatalf("remove: %d %s", w.Code, w.Body.String())
	}
	if servedTile(f.served(t, ""), "api_change") != nil || f.eventCount() != events+1 {
		t.Fatal("the removed tile is still served, or the page was not told")
	}
	if w := action("remove", "api_change"); w.Code != 404 {
		t.Errorf("removing it again: %d", w.Code)
	}
	if w := action("restore", "api_change"); w.Code != 200 {
		t.Fatalf("restore: %d %s", w.Code, w.Body.String())
	}
	if v := f.served(t, ""); servedTile(v, "api_change") == nil || ownerOf(v, "api_change") != "context" {
		t.Fatal("the restored tile is not back on its section")
	}
	for _, body := range []string{`{"clone":"` + f.clone.ID + `","pr":42,"tile":"api_change","action":"hide"}`, `{"clone":"` + f.clone.ID + `","pr":0,"tile":"api_change","action":"remove"}`, `not json`} {
		if w := post(body); w.Code != 400 {
			t.Errorf("%s: %d", body, w.Code)
		}
	}
	if w := action("remove", "schema_change"); w.Code != 404 {
		t.Errorf("a tile not added: %d", w.Code)
	}
}
