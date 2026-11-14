package uiserver

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/baum-ai/baum/internal/agentcontext"
	"github.com/baum-ai/baum/internal/appdb/appdbtest"
	"github.com/baum-ai/baum/internal/repos"
	"github.com/baum-ai/baum/internal/tilelayout"
	"github.com/baum-ai/baum/internal/tileview/tileviewtest"
	"github.com/baum-ai/baum/tiles"
)

// layoutFixture is a tiles fixture whose API keeps people's layouts, and the
// layout events it published.
type layoutFixture struct {
	*tilesFixture
	api    *LocalAPI
	mu     sync.Mutex
	events []string
}

func newLayoutFixture(t *testing.T) *layoutFixture {
	t.Helper()
	f := &layoutFixture{}
	service := &tilelayout.Service{Path: filepath.Join(appdbtest.TempDir(t), "app.sqlite"), Changed: func(pr string, revision int64) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.events = append(f.events, fmt.Sprintf("%s@%d", pr, revision))
	}}
	f.tilesFixture = newTilesFixture(t, func(a *LocalAPI) { a.Layouts = service; f.api = a })
	return f
}

func (f *layoutFixture) post(body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", "http://local/api/tiles/layout", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	f.handler.ServeHTTP(w, r)
	return w
}

func (f *layoutFixture) action(action string, extra string) *httptest.ResponseRecorder {
	return f.post(fmt.Sprintf(`{"clone":%q,"pr":42,"action":%q%s}`, f.clone.ID, action, extra))
}

func layoutOf(t *testing.T, w *httptest.ResponseRecorder) layoutAnswer {
	t.Helper()
	if w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	var answer layoutAnswer
	if err := json.Unmarshal(w.Body.Bytes(), &answer); err != nil {
		t.Fatal(err)
	}
	return answer
}

func entryOf(doc *tiles.Document, id string) (int, tiles.Entry) {
	for i, e := range doc.Layout.Entries {
		if e.Tile == id {
			return i, e
		}
	}
	return -1, tiles.Entry{}
}

// An edit from the page shows on the next read of the tile route, which
// keeps answers per layout; undo and reset bring the generated layout back,
// byte for byte; each write publishes the layout event, and a new version
// keeps the edits.
func TestTilesServesThePersonsLayout(t *testing.T) {
	f := newLayoutFixture(t)
	f.put(t, titled(t, "First title"))
	generated := f.get("/api/tiles", "", "")
	before := decodeTiles(t, generated)
	if summary := layoutOf(t, f.get("/api/tiles/layout", "", "")); summary.Edited || summary.CanUndo || summary.Revision != 0 || summary.Last != nil {
		t.Fatalf("a layout never edited: %+v", summary)
	}

	edit := layoutOf(t, f.action("edit", `,"ops":[{"op":"move","tile":"evidence","to":{"edge":"first"}},{"op":"hide","tile":"context"},{"op":"hide","tile":"overview"}]`))
	if !edit.Edited || !edit.CanUndo || edit.Revision != 1 || edit.Last == nil || edit.Last.By != "page" ||
		!slices.Equal(edit.Applied, []string{"Moved Evidence to the top", "Removed Context"}) || len(edit.Refused) != 1 {
		t.Fatalf("edit: %+v", edit)
	}
	after := f.get("/api/tiles", "", "")
	body := decodeTiles(t, after)
	if after.Header().Get("ETag") == generated.Header().Get("ETag") {
		t.Error("the edited layout was served under the generated layout's ETag")
	}
	if _, e := entryOf(body.Tiles, "context"); e.Group != tiles.GroupHidden {
		t.Errorf("context is not hidden: %+v", body.Tiles.Layout.Entries)
	}
	if at, _ := entryOf(body.Tiles, "evidence"); at > 1 {
		t.Errorf("evidence did not move up: %+v", body.Tiles.Layout.Entries)
	}
	if before.Tiles.Origin.Kind != tiles.OriginConverted || body.Tiles.Origin.Kind != tiles.OriginGenerated {
		t.Errorf("origins %s and %s", before.Tiles.Origin.Kind, body.Tiles.Origin.Kind)
	}

	// Nothing to apply, or the wrong batch to undo: nothing is written.
	if w := f.action("edit", `,"ops":[{"op":"hide","tile":"context"}]`); w.Code != 400 || !strings.Contains(w.Body.String(), "Context is already hidden") {
		t.Errorf("a refused edit: %d %s", w.Code, w.Body.String())
	}
	if w := f.action("undo", `,"batch":"edt_000000000000"`); w.Code != 409 {
		t.Errorf("an undo of another batch: %d %s", w.Code, w.Body.String())
	}

	// A new version keeps the edits.
	f.put(t, titled(t, "Second title"))
	if body := decodeTiles(t, f.get("/api/tiles", "", "")); body.Tiles.Layout.Entries[len(body.Tiles.Layout.Entries)-1].Tile != "context" {
		t.Errorf("the next version lost the edits: %+v", body.Tiles.Layout.Entries)
	}

	reset := layoutOf(t, f.action("reset", ""))
	if reset.Edited || !reset.CanUndo || reset.Last == nil || !reset.Last.Reset {
		t.Fatalf("reset: %+v", reset)
	}
	if w := f.action("reset", ""); w.Code != 400 || !strings.Contains(w.Body.String(), "already the generated one") {
		t.Errorf("a second reset: %d %s", w.Code, w.Body.String())
	}
	undone := layoutOf(t, f.action("undo", fmt.Sprintf(`,"batch":%q`, reset.Last.ID)))
	if !undone.Edited || !slices.Equal(undone.Undone, []string{"Reset the layout"}) {
		t.Fatalf("undoing the reset: %+v", undone)
	}
	undone = layoutOf(t, f.action("undo", ""))
	if undone.Edited || undone.CanUndo || !slices.Equal(undone.Undone, edit.Applied) {
		t.Fatalf("undoing the edit: %+v", undone)
	}
	// The second version, with no edits, is served as it was generated.
	plain := f.get("/api/tiles", "&version=1", "")
	if !bytes.Equal(plain.Body.Bytes(), generated.Body.Bytes()) {
		t.Error("with no edits the first version is not served as before")
	}
	if w := f.action("undo", ""); w.Code != 400 || !strings.Contains(w.Body.String(), "Nothing to undo") {
		t.Errorf("an undo with nothing to undo: %d %s", w.Code, w.Body.String())
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if !slices.Equal(f.events, []string{"acme/widgets#42@1", "acme/widgets#42@2", "acme/widgets#42@3", "acme/widgets#42@4"}) {
		t.Errorf("events %v", f.events)
	}
}

// What the routes refuse: a body they cannot read, an unknown action or
// clone, an op no layout takes, and a pull request with no saved review.
func TestLayoutRoutesRefuse(t *testing.T) {
	f := newLayoutFixture(t)
	f.put(t, titled(t, "First title"))
	for name, tc := range map[string]struct {
		body   string
		status int
	}{
		"not JSON":       {`{`, 400},
		"unknown member": {fmt.Sprintf(`{"clone":%q,"pr":42,"action":"edit","extra":1}`, f.clone.ID), 400},
		"action":         {fmt.Sprintf(`{"clone":%q,"pr":42,"action":"delete"}`, f.clone.ID), 400},
		"clone":          {`{"clone":"c-missing","pr":42,"action":"reset"}`, 404},
		"no ops":         {fmt.Sprintf(`{"clone":%q,"pr":42,"action":"edit","ops":[]}`, f.clone.ID), 400},
		"unknown op":     {fmt.Sprintf(`{"clone":%q,"pr":42,"action":"edit","ops":[{"op":"delete","tile":"risks"}]}`, f.clone.ID), 400},
		"no review":      {fmt.Sprintf(`{"clone":%q,"pr":43,"action":"edit","ops":[{"op":"hide","tile":"context"}]}`, f.clone.ID), 404},
	} {
		t.Run(name, func(t *testing.T) {
			if w := f.post(tc.body); w.Code != tc.status {
				t.Errorf("status %d, want %d: %s", w.Code, tc.status, w.Body.String())
			}
		})
	}
}

// The layout routes are behind the server's boundary like every other
// route, and report the capability unavailable without a service.
func TestLayoutRoutesAreAuthenticatedAndOptional(t *testing.T) {
	registry, err := repos.Open(filepath.Join(appdbtest.TempDir(t), "repos.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = registry.Close() }()
	for name, tt := range map[string]struct {
		layouts *tilelayout.Service
		get     int
	}{
		"served":      {&tilelayout.Service{Path: filepath.Join(appdbtest.TempDir(t), "app.sqlite")}, http.StatusBadRequest},
		"unavailable": {nil, http.StatusServiceUnavailable},
	} {
		t.Run(name, func(t *testing.T) {
			handler := LocalAPI{Registry: registry, Layouts: tt.layouts}.Handler()
			defer handler.Close()
			s, err := New(Options{API: handler})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = s.Listen(0); err != nil {
				t.Fatal(err)
			}
			defer func() { _ = s.listener.Close() }()
			body := `{"clone":"c-missing","pr":1,"action":"reset"}`
			for _, method := range []string{"GET", "POST"} {
				if w := request(t, s, method, "/api/tiles/layout", "", s.origin, "application/json", "", body); w.Code != http.StatusUnauthorized {
					t.Errorf("%s without credentials: %d", method, w.Code)
				}
			}
			if w := request(t, s, "POST", "/api/tiles/layout", "", "http://elsewhere.example", "application/json", "Bearer "+s.launch, body); w.Code != http.StatusForbidden {
				t.Errorf("POST from another origin: %d", w.Code)
			}
			if w := request(t, s, "POST", "/api/tiles/layout", "", s.origin, "text/plain", "Bearer "+s.launch, body); w.Code != http.StatusForbidden {
				t.Errorf("POST that is not JSON: %d", w.Code)
			}
			if w := request(t, s, "GET", "/api/tiles/layout", "", "", "", "Bearer "+s.launch, ""); w.Code != tt.get {
				t.Errorf("GET: %d %s", w.Code, w.Body.String())
			}
		})
	}
}

// Without a layout service the tile route serves what it always did.
func TestTilesWithoutLayouts(t *testing.T) {
	f := newTilesFixture(t)
	f.put(t, tileviewtest.Encode(t, tileviewtest.Base(t)))
	if body := decodeTiles(t, f.get("/api/tiles", "", "")); body.Tiles.Origin.Kind != tiles.OriginConverted {
		t.Errorf("origin %s", body.Tiles.Origin.Kind)
	}
}

// The session panel's tools change the layout through the same service: an
// edit_tiles call shows on the next read of the tile route, and the tools
// read the tiles the person is shown.
func TestLayoutEditorServesThePanelsTools(t *testing.T) {
	f := newLayoutFixture(t)
	f.put(t, titled(t, "First title"))
	mcp := agentcontext.New(agentcontext.Options{API: f.handler, Layouts: LayoutEditor{API: *f.api}})
	token, id, revoke, err := mcp.Registry.Mint(agentcontext.Consumer{Kind: agentcontext.Chat})
	if err != nil {
		t.Fatal(err)
	}
	defer revoke()
	if err := mcp.Registry.SetPage(id, agentcontext.PageContext{Page: agentcontext.PagePullRequest, Repository: "acme/widgets", PullRequest: 42,
		Focus: json.RawMessage(`{"view":"tiles","kind":"tile","id":"evidence","title":"Evidence"}`)}); err != nil {
		t.Fatal(err)
	}
	callTool := func(name string, args any) (string, bool) {
		params, _ := json.Marshal(map[string]any{"name": name, "arguments": args})
		r := httptest.NewRequest("POST", "/api/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":`+string(params)+`}`))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		mcp.ServeHTTP(w, r)
		var reply struct {
			Result struct {
				Content []struct {
					Text string `json:"text"`
				} `json:"content"`
				IsError bool `json:"isError"`
			} `json:"result"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &reply); err != nil || len(reply.Result.Content) != 1 {
			t.Fatalf("%s: %s", name, w.Body.String())
		}
		return reply.Result.Content[0].Text, reply.Result.IsError
	}
	text, failed := callTool("tile_layout", nil)
	if failed || !strings.Contains(text, `"focus":{"tile":"evidence","title":"Evidence"}`) || !strings.Contains(text, `"rows":[["overview:half","risks:half"]`) {
		t.Fatalf("tile_layout: %s", text)
	}
	text, failed = callTool("edit_tiles", map[string]any{"ops": []any{map[string]any{"op": "resize", "tile": "evidence", "size": "full"}, map[string]any{"op": "move", "tile": "evidence", "to": map[string]any{"edge": "first"}}}})
	if failed || !strings.Contains(text, "Made Evidence full width") || !strings.Contains(text, `"rows":[["evidence:full"]`) {
		t.Fatalf("edit_tiles: %s", text)
	}
	if body := decodeTiles(t, f.get("/api/tiles", "", "")); body.Tiles.Layout.Entries[0].Tile != "evidence" || body.Tiles.Layout.Entries[0].Size != tiles.SizeFull {
		t.Errorf("the tile route does not show the edit: %+v", body.Tiles.Layout.Entries[0])
	}
	summary := layoutOf(t, f.get("/api/tiles/layout", "", ""))
	if summary.Last == nil || summary.Last.By != "chat" {
		t.Errorf("the batch is not the chat's: %+v", summary)
	}
	if text, failed = callTool("undo_tiles", nil); failed || !strings.Contains(text, `"undone":["Made Evidence full width","Moved Evidence to the top"]`) {
		t.Errorf("undo_tiles: %s", text)
	}
	if text, failed = callTool("tile_layout", nil); failed || strings.Contains(text, `"edited":true`) {
		t.Errorf("after the undo: %s", text)
	}
	if err := mcp.Registry.SetPage(id, agentcontext.PageContext{Page: agentcontext.PagePullRequest, Repository: "acme/widgets", PullRequest: 99}); err != nil {
		t.Fatal(err)
	}
	if text, failed = callTool("tile_layout", nil); !failed || !strings.Contains(text, "no saved review yet") {
		t.Errorf("a pull request with no review: %s", text)
	}
}
