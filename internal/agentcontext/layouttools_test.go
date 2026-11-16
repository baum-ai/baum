package agentcontext

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/baum-ai/baum/internal/appdb/appdbtest"
	"github.com/baum-ai/baum/internal/tilelayout"
	"github.com/baum-ai/baum/review"
	"github.com/baum-ai/baum/schema"
	"github.com/baum-ai/baum/tiles"
	"github.com/baum-ai/baum/tiles/library"
)

// fakeLayouts is a LayoutEditor over the real layout service and the
// schema's example review, saved as acme/web#13.
type fakeLayouts struct {
	service *tilelayout.Service
	doc     *tiles.Document
	// edits counts the writes asked for.
	edits int
}

func newFakeLayouts(t *testing.T) *fakeLayouts {
	t.Helper()
	normalized, err := review.NormalizeStored(schema.Example)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := tiles.FromReview(normalized, tiles.Options{})
	if err != nil {
		t.Fatal(err)
	}
	return &fakeLayouts{service: &tilelayout.Service{Path: filepath.Join(appdbtest.TempDir(t), "app.sqlite")}, doc: doc}
}

func (f *fakeLayouts) Shown(ctx context.Context, repository string, number int) (ShownLayout, error) {
	if !strings.EqualFold(repository, "acme/web") || number != 13 {
		return ShownLayout{}, errors.New("Pull request has no saved review yet")
	}
	state, err := f.service.Read(ctx, tilelayout.Key(repository, number))
	if err != nil {
		return ShownLayout{}, err
	}
	fills, err := f.service.Fills(ctx, tilelayout.Key(repository, number))
	if err != nil {
		return ShownLayout{}, err
	}
	held, _ := tilelayout.AddedFor(state.AddedTypes(), fills, tilelayout.Version{Number: 2, Latest: true, Diff: library.DiffOf(f.doc)}, func(string) (json.RawMessage, bool) { return nil, false })
	c, _ := tiles.ApplyOverlay(f.doc, nil, state.Overlay, held.Added)
	composed, err := tiles.Compose(f.doc, &c)
	if err != nil {
		return ShownLayout{}, err
	}
	return ShownLayout{Version: 2, Document: composed, Revision: state.Revision, Edited: state.Edited(), CanUndo: state.CanUndo()}, nil
}

func change(c tilelayout.Change) LayoutChange {
	out := LayoutChange{Applied: c.Applied, Undone: c.Undone, Revision: c.State.Revision, Edited: c.State.Edited(), CanUndo: c.State.CanUndo(), Saved: c.Saved}
	for _, r := range c.Refused {
		out.Refused = append(out.Refused, LayoutRefusal{Index: r.Index, Reason: r.Reason})
	}
	return out
}

func (f *fakeLayouts) Edit(ctx context.Context, repository string, number int, ops []tiles.Op) (LayoutChange, error) {
	f.edits++
	shown, err := f.Shown(ctx, repository, number)
	if err != nil {
		return LayoutChange{}, err
	}
	c, err := f.service.Edit(ctx, tilelayout.Key(repository, number), "chat", shown.Document, shown.Revision, ops)
	return change(c), err
}

// Add checks and stores the data as the daemon does (uiserver), then adds.
func (f *fakeLayouts) Add(ctx context.Context, repository string, number int, add TileAdd) (TileAdded, error) {
	e, ok := library.Find(add.Type)
	if !ok {
		return TileAdded{}, errors.New("unknown type")
	}
	shown, err := f.Shown(ctx, repository, number)
	if err != nil {
		return TileAdded{}, err
	}
	state := "filling"
	if len(add.Data) > 0 {
		if problems := library.CheckAddedFill(tiles.NewValidator(), e, add.Data, library.DiffOf(f.doc)); len(problems) > 0 {
			return TileAdded{}, errors.New("The data does not fit: " + strings.Join(problems, "; "))
		}
		if err := f.service.PutFill(ctx, tilelayout.Key(repository, number), tiles.LibraryTileID(e.Definition.Name), shown.Version, tilelayout.OriginChat, e.Encoded, add.Data); err != nil {
			return TileAdded{}, err
		}
		state = "ready"
	}
	f.edits++
	c, err := f.service.Edit(ctx, tilelayout.Key(repository, number), "chat", shown.Document, shown.Revision,
		[]tiles.Op{{Op: tiles.OpAdd, Size: add.Size, To: add.To, Add: &tiles.Added{Kind: "library", Name: e.Definition.Name, Digest: e.Definition.Digest, Title: e.Definition.Title}}})
	if err != nil {
		return TileAdded{}, err
	}
	return TileAdded{Tile: tiles.LibraryTileID(e.Definition.Name), State: state, Change: change(c)}, nil
}

func (f *fakeLayouts) Undo(ctx context.Context, repository string, number int) (LayoutChange, error) {
	f.edits++
	c, err := f.service.Undo(ctx, tilelayout.Key(repository, number), "")
	return change(c), err
}

func (f *fakeLayouts) Reset(ctx context.Context, repository string, number int) (LayoutChange, error) {
	f.edits++
	c, err := f.service.Reset(ctx, tilelayout.Key(repository, number), "chat")
	return change(c), err
}

func layoutServer(t *testing.T) (*Server, *fakeLayouts, string, string) {
	t.Helper()
	f := newFakeLayouts(t)
	s := New(Options{API: http.NotFoundHandler(), Layouts: f})
	token, id := chat(t, s)
	return s, f, token, id
}

var pullPage = PageContext{Page: PagePullRequest, Repository: "acme/web", PullRequest: 13, Focus: json.RawMessage(`{"view":"tiles"}`)}

func withFocus(focus string) PageContext {
	p := pullPage
	p.Focus = json.RawMessage(focus)
	return p
}

// The layout tools are the pull request page's, and only a chat's on that
// page: elsewhere they refuse, and an action never sees them.
func TestLayoutToolsArePageBoundAndChatOnly(t *testing.T) {
	s, f, token, id := layoutServer(t)
	var page struct {
		Tools []string `json:"tools"`
	}
	setPage(t, s, id, pullPage)
	text, _ := data(t, s, token, "current_page", nil)
	if err := json.Unmarshal([]byte(text), &page); err != nil {
		t.Fatal(err)
	}
	for _, name := range layoutPageTools {
		if !slices.Contains(page.Tools, name) {
			t.Errorf("the pull request page does not list %s", name)
		}
	}
	setPage(t, s, id, PageContext{Page: PageInbox})
	text, _ = data(t, s, token, "current_page", nil)
	if strings.Contains(text, "edit_tiles") {
		t.Error("the Inbox lists the layout tools")
	}
	valid := map[string]any{"edit_tiles": map[string]any{"ops": []any{map[string]any{"op": "hide", "tile": "context"}}}, "add_tile": map[string]any{"type": "context"}, "tile_type": map[string]any{"type": "api_change"}}
	for _, tool := range layoutPageTools {
		if text, failed := data(t, s, token, tool, valid[tool]); !failed || !strings.Contains(text, "open a pull request's page first") {
			t.Errorf("%s off the pull request page: %v %s", tool, failed, text)
		}
	}
	action, _, revoke := mint(t, s, Consumer{Kind: Action, Repository: "acme/web", PullRequest: 13})
	defer revoke()
	r := rpc(t, s, action, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"edit_tiles","arguments":{"ops":[{"op":"hide","tile":"context"}]}}}`)
	if r.Error == nil || !strings.Contains(r.Error.Message, "unknown tool") {
		t.Errorf("an action called edit_tiles: %s", r.Result)
	}
	if f.edits != 0 {
		t.Errorf("%d writes", f.edits)
	}
	// Without an editor the tools say so.
	bare := New(Options{API: http.NotFoundHandler()})
	token, id = chat(t, bare)
	setPage(t, bare, id, pullPage)
	if text, failed := data(t, bare, token, "tile_layout", nil); !failed || !strings.Contains(text, "not available") {
		t.Errorf("no editor: %s", text)
	}
}

// An edit needs the turn to have been sent from the pull request the page
// shows now: a conversation's page follows the person, so a turn begun about
// one pull request cannot change another's layout. Reading is not bound, and
// a turn from the open pull request edits as before.
func TestLayoutEditsAreBoundToTheTurnsPullRequest(t *testing.T) {
	s, f, token, id := layoutServer(t)
	setPage(t, s, id, pullPage)
	if err := s.Registry.SetTurnPage(id, pullPage); err != nil {
		t.Fatal(err)
	}
	hide := map[string]any{"ops": []any{map[string]any{"op": "hide", "tile": "context"}}}
	if text, failed := data(t, s, token, "edit_tiles", hide); failed {
		t.Fatalf("an edit from the turn's own pull request: %s", text)
	}
	other := pullPage
	other.PullRequest = 14
	setPage(t, s, id, other)
	writes := f.edits
	for _, tool := range []string{"edit_tiles", "add_tile", "undo_tiles", "reset_tiles"} {
		args := map[string]any{"edit_tiles": hide, "add_tile": map[string]any{"type": "context"}}[tool]
		if text, failed := data(t, s, token, tool, args); !failed || !strings.Contains(text, "started about another page; ask from the pull request you want to change") {
			t.Errorf("%s from another pull request: %v %s", tool, failed, text)
		}
	}
	if f.edits != writes {
		t.Errorf("%d writes from another pull request", f.edits-writes)
	}
	if text, _ := data(t, s, token, "tile_layout", nil); strings.Contains(text, "another page") {
		t.Errorf("reading the layout is bound to the turn: %s", text)
	}
	// The same number in another repository, and a turn from another kind of
	// page, are not the pull request either.
	elsewhere := pullPage
	elsewhere.Repository = "acme/api"
	setPage(t, s, id, elsewhere)
	if _, failed := data(t, s, token, "edit_tiles", hide); !failed {
		t.Error("an edit on the same number of another repository")
	}
	setPage(t, s, id, pullPage)
	if err := s.Registry.SetTurnPage(id, PageContext{Page: PageInbox}); err != nil {
		t.Fatal(err)
	}
	if _, failed := data(t, s, token, "edit_tiles", hide); !failed {
		t.Error("an edit from a turn begun on the Inbox")
	}
	// The next turn, sent from the pull request, edits.
	if err := s.Registry.SetTurnPage(id, pullPage); err != nil {
		t.Fatal(err)
	}
	if text, failed := data(t, s, token, "undo_tiles", nil); failed {
		t.Errorf("an undo from the turn's own pull request: %s", text)
	}
}

// tile_layout lists the rows and tiles the page shows and the tile the
// person pointed at, by an Ask or by the tile they have open.
func TestTileLayoutListsTheRowsAndTheFocus(t *testing.T) {
	s, _, token, id := layoutServer(t)
	var view layoutView
	for focus, want := range map[string]string{`{"view":"tiles","kind":"tile","id":"sec_api","title":"API"}`: "sec_api", `{"view":"tiles","tile":"context"}`: "context", `{"view":"tiles"}`: ""} {
		setPage(t, s, id, withFocus(focus))
		out(t, s, token, "tile_layout", nil, &view)
		got := ""
		if view.Focus != nil {
			got = view.Focus.Tile
		}
		if got != want {
			t.Errorf("focus %s: got %q, want %q", focus, got, want)
		}
	}
	if view.View != "tiles" || view.Version != 2 || view.FirstScreenRows != 2 || len(view.Rows) < 3 || view.Rows[0][0] != "overview:half" {
		t.Errorf("view %+v", view)
	}
	overview := view.Tiles[slices.IndexFunc(view.Tiles, func(t layoutTile) bool { return t.ID == "overview" })]
	if overview.CanHide || overview.WhyNot == "" || !slices.Equal(overview.Sizes, []tiles.Size{tiles.SizeFull, tiles.SizeHalf}) || !overview.OnLanding || overview.Row != 1 {
		t.Errorf("overview %+v", overview)
	}
	setPage(t, s, id, withFocus(`{}`))
	out(t, s, token, "tile_layout", nil, &view)
	if view.View != "full" || !strings.Contains(view.Note, "tiles page") {
		t.Errorf("on the full review: %+v", view)
	}
}

// edit_tiles applies at once as one batch, says what applied and what was
// refused in the host's words, and fails with the reasons when nothing
// could; undo and reset take edits back and say so.
func TestEditUndoResetTiles(t *testing.T) {
	s, _, token, id := layoutServer(t)
	setPage(t, s, id, pullPage)
	var result layoutResult
	out(t, s, token, "edit_tiles", map[string]any{"ops": []any{
		map[string]any{"op": "move", "tile": "risks", "to": map[string]any{"edge": "first"}},
		map[string]any{"op": "hide", "tile": "overview"},
		map[string]any{"op": "resize", "tile": "sec_api", "size": "full"},
	}}, &result)
	if !slices.Equal(result.Applied, []string{"Moved Risks to the top", "Made " + tileTitle(t, s, token, "sec_api") + " full width"}) ||
		len(result.Refused) != 1 || result.Refused[0].Index != 1 || !result.Edited || !result.CanUndo || result.Revision != 1 || result.Rows[0][0] != "risks:half" {
		t.Fatalf("edit: %+v", result)
	}
	text, failed := data(t, s, token, "edit_tiles", map[string]any{"ops": []any{map[string]any{"op": "hide", "tile": "overview"}}})
	if !failed || !strings.Contains(text, "Nothing changed: The overview always stays on the landing page") {
		t.Errorf("an edit refused whole: %s", text)
	}
	out(t, s, token, "edit_tiles", map[string]any{"ops": []any{map[string]any{"op": "hide", "tile": "context"}}}, &result)
	if !slices.Contains(result.Hidden, "context") {
		t.Errorf("hidden %v", result.Hidden)
	}
	// add_tile brings a removed tile back; a type that is neither a tile of
	// the review nor a library type is refused, naming the library types.
	if text, failed := data(t, s, token, "add_tile", map[string]any{"type": "api_changes"}); !failed || !strings.Contains(text, "No tile type api_changes") || !strings.Contains(text, "api_change (Interface changes)") {
		t.Errorf("adding an unknown tile type: %s", text)
	}
	if text, failed := data(t, s, token, "add_tile", map[string]any{"type": "media"}); !failed || !strings.Contains(text, "already on the review") {
		t.Errorf("adding a tile already shown: %s", text)
	}
	out(t, s, token, "add_tile", map[string]any{"type": "context", "to": map[string]any{"after": "risks"}}, &result)
	if len(result.Hidden) != 0 || !slices.Contains(result.Rows[0], "context:half") && !slices.Contains(result.Rows[1], "context:third") && !strings.Contains(strings.Join(result.Rows[0], " ")+" "+strings.Join(result.Rows[1], " "), "context") {
		t.Errorf("add_tile: %+v", result)
	}
	out(t, s, token, "undo_tiles", nil, &result)
	if !slices.Equal(result.Undone, []string{"Brought back Context", "Moved Context after Risks"}) {
		t.Errorf("undo: %+v", result)
	}
	out(t, s, token, "reset_tiles", nil, &result)
	if result.Edited || !result.CanUndo || result.Rows[0][0] != "overview:half" {
		t.Errorf("reset: %+v", result)
	}
	if text, failed := data(t, s, token, "reset_tiles", nil); !failed || !strings.Contains(text, "already the generated one") {
		t.Errorf("a second reset: %s", text)
	}
	for range 4 {
		data(t, s, token, "undo_tiles", nil)
	}
	if text, failed := data(t, s, token, "undo_tiles", nil); !failed || !strings.Contains(text, "Nothing to undo") {
		t.Errorf("nothing to undo: %s", text)
	}
}

func tileTitle(t *testing.T, s *Server, token, id string) string {
	t.Helper()
	var view layoutView
	out(t, s, token, "tile_layout", nil, &view)
	for _, tile := range view.Tiles {
		if tile.ID == id {
			return tile.Title
		}
	}
	t.Fatalf("no tile %s", id)
	return ""
}

// The arguments are checked strictly: an unknown member, an op past the
// bound or of an unknown kind, and a place with two anchors are refused
// before anything is written.
func TestEditTilesArgumentsAreStrict(t *testing.T) {
	s, f, token, id := layoutServer(t)
	setPage(t, s, id, pullPage)
	many := []any{}
	for range 21 {
		many = append(many, map[string]any{"op": "pin", "tile": "risks"})
	}
	for name, args := range map[string]any{
		"unknown member": map[string]any{"ops": []any{map[string]any{"op": "hide", "tile": "context", "why": "x"}}},
		"no ops":         map[string]any{"ops": []any{}},
		"too many":       map[string]any{"ops": many},
		"unknown op":     map[string]any{"ops": []any{map[string]any{"op": "delete", "tile": "context"}}},
		"two places":     map[string]any{"ops": []any{map[string]any{"op": "move", "tile": "context", "to": map[string]any{"edge": "first", "before": "risks"}}}},
		"size":           map[string]any{"ops": []any{map[string]any{"op": "resize", "tile": "context", "size": "huge"}}},
		"count":          map[string]any{"ops": []any{map[string]any{"op": "changes_shown", "count": 0}}},
		"extra argument": map[string]any{"ops": []any{map[string]any{"op": "hide", "tile": "context"}}, "pr": 4},
	} {
		if text, failed := data(t, s, token, "edit_tiles", args); !failed {
			t.Errorf("%s: accepted: %s", name, text)
		}
	}
	if f.edits != 0 {
		t.Errorf("%d writes", f.edits)
	}
	// A move with no place passes the schema and is refused by the host.
	if text, failed := data(t, s, token, "edit_tiles", map[string]any{"ops": []any{map[string]any{"op": "move", "tile": "context"}}}); !failed || !strings.Contains(text, "needs a place") {
		t.Errorf("a move with no place: %s", text)
	}
}

// tile_layout's result stays within its bound, however many tiles.
func TestTileLayoutIsBounded(t *testing.T) {
	v := layoutView{PullRequest: "acme/web#13"}
	for i := range 2000 {
		v.Tiles = append(v.Tiles, layoutTile{ID: "tile_" + strings.Repeat("x", 60) + string(rune('a'+i%26)), Title: strings.Repeat("T", 80), Sizes: []tiles.Size{tiles.SizeFull}, OnLanding: i < 6})
	}
	text, err := boundedView(v)
	if err != nil || len(text) > maxLayoutResult {
		t.Fatalf("%d bytes, %v", len(text), err)
	}
}

// apiData is api_change data for the example review: a consumer cited on one
// of its hunks.
func apiData(hunk, label string) map[string]any {
	return map[string]any{"endpoints": []any{}, "docs": nil,
		"consumers": []any{map[string]any{"label": label, "updated": true, "cites": []any{map[string]any{"type": "hunk", "id": hunk}}}}}
}

// tile_type gives what the agent needs to write a library type's data:
// its instructions and the fill schema, without the host-written part.
// tile_layout lists the library types the review does not show as addable.
func TestTileTypeAndAddable(t *testing.T) {
	s, _, token, id := layoutServer(t)
	setPage(t, s, id, pullPage)
	var kind struct {
		Type         string          `json:"type"`
		Title        string          `json:"title"`
		Instructions string          `json:"fill_instructions"`
		Schema       json.RawMessage `json:"fill_schema"`
	}
	out(t, s, token, "tile_type", map[string]any{"type": "Interface changes"}, &kind)
	if kind.Type != "api_change" || kind.Instructions == "" || !strings.Contains(string(kind.Schema), `"endpoints"`) || strings.Contains(string(kind.Schema), `"related"`) {
		t.Errorf("tile_type: %+v", kind)
	}
	if text, failed := data(t, s, token, "tile_type", map[string]any{"type": "weather"}); !failed || !strings.Contains(text, "api_change") {
		t.Errorf("an unknown type: %s", text)
	}
	var view layoutView
	out(t, s, token, "tile_layout", nil, &view)
	if !slices.ContainsFunc(view.Addable, func(a addableType) bool { return a.Type == "api_change" && a.Source == "built-in" }) {
		t.Errorf("addable: %+v", view.Addable)
	}
}

// add_tile with data the agent wrote shows the tile at once with that data;
// data that does not fit is refused, naming places and never values, and
// changes nothing; without data the tile is pending while it is filled.
func TestAddTileOfALibraryType(t *testing.T) {
	s, f, token, id := layoutServer(t)
	setPage(t, s, id, pullPage)
	writes := f.edits
	text, failed := data(t, s, token, "add_tile", map[string]any{"type": "api_change", "data": apiData("h_000000000000", "SENTINEL_LABEL")})
	if !failed || !strings.Contains(text, "/consumers/0/cites/0") || strings.Contains(text, "SENTINEL_LABEL") || strings.Contains(text, "h_000000000000") {
		t.Errorf("data citing a hunk not in the diff: %s", text)
	}
	if f.edits != writes {
		t.Error("a refused add wrote")
	}
	var added struct {
		Tile    string   `json:"tile"`
		State   string   `json:"state"`
		Applied []string `json:"applied"`
	}
	out(t, s, token, "add_tile", map[string]any{"type": "api_change", "to": map[string]any{"edge": "first"}, "data": apiData("h_a408875b8eaa", "api.Handler")}, &added)
	if added.Tile != "api_change" || added.State != "ready" || len(added.Applied) != 1 {
		t.Fatalf("add with data: %+v", added)
	}
	shown, err := f.Shown(context.Background(), "acme/web", 13)
	if err != nil {
		t.Fatal(err)
	}
	tile := shown.Document.Tiles[slices.IndexFunc(shown.Document.Tiles, func(t tiles.Tile) bool { return t.ID == "api_change" })]
	if tile.State != tiles.StateReady || !strings.Contains(string(tile.Data), "api.Handler") || shown.Document.Layout.Entries[0].Tile != "api_change" {
		t.Errorf("the added tile: %s %s, first %s", tile.State, tile.Data, shown.Document.Layout.Entries[0].Tile)
	}
	if text, failed := data(t, s, token, "add_tile", map[string]any{"type": "api_change"}); !failed || !strings.Contains(text, "already on the review") {
		t.Errorf("adding it again: %s", text)
	}
	out(t, s, token, "add_tile", map[string]any{"type": "schema_change"}, &added)
	if added.State != "filling" {
		t.Errorf("add without data: %+v", added)
	}
	shown, _ = f.Shown(context.Background(), "acme/web", 13)
	if i := slices.IndexFunc(shown.Document.Tiles, func(t tiles.Tile) bool { return t.ID == "schema_change" }); i < 0 || shown.Document.Tiles[i].State != tiles.StatePending {
		t.Errorf("the tile being filled is not pending")
	}
}

// add_tile finds a removed tile by its title too, as a person names it.
func TestAddTileFindsARemovedTileByItsTitle(t *testing.T) {
	s, _, token, id := layoutServer(t)
	setPage(t, s, id, pullPage)
	var result layoutResult
	// A removed tile is found by its title too.
	var view layoutView
	out(t, s, token, "tile_layout", nil, &view)
	title := view.Tiles[slices.IndexFunc(view.Tiles, func(t layoutTile) bool { return t.ID == "sec_api" })].Title
	out(t, s, token, "edit_tiles", map[string]any{"ops": []any{map[string]any{"op": "hide", "tile": "sec_api"}}}, &result)
	out(t, s, token, "add_tile", map[string]any{"type": strings.ToLower(title)}, &result)
	if slices.Contains(result.Hidden, "sec_api") || title == "sec_api" {
		t.Errorf("add_tile did not bring %q back by its title", title)
	}
}
