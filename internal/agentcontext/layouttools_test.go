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
	c, _ := tiles.ApplyOverlay(f.doc, nil, state.Overlay, nil)
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
	c, err := f.service.Edit(ctx, tilelayout.Key(repository, number), "chat", shown.Document, ops)
	return change(c), err
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
	valid := map[string]any{"edit_tiles": map[string]any{"ops": []any{map[string]any{"op": "hide", "tile": "context"}}}, "add_tile": map[string]any{"type": "context"}}
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
	// add_tile brings a removed tile back; it cannot fill a new one.
	if text, failed := data(t, s, token, "add_tile", map[string]any{"type": "api_changes"}); !failed || !strings.Contains(text, "not available yet") {
		t.Errorf("adding a new tile type: %s", text)
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
