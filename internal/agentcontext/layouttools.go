package agentcontext

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/baum-ai/baum/tiles"
	"github.com/baum-ai/baum/tiles/library"
)

// The layout tools change the person's own tile layout of the pull request
// the page shows, at once: move, resize, remove, bring back and pin tiles,
// undo and reset. They are the one exception to "a chat changes baum only
// through proposals", and a narrow one: they change only how the person's
// own page lays out a review they have open, never its content, the saved
// version, the repository or GitHub. Every edit shows on the page with an
// Undo, keeps the rules a review's tiles always keep (the overview, and the
// risks tile while it ranks a critical or high risk, stay on the landing
// page; every item is presented once; removed tiles stay reachable), and is
// kept on this computer only. Like the propose_* tools they are global, so
// an action never sees them, and refuse any page but the pull request's.

// LayoutEditor changes the person's own tile layout of a pull request; the
// daemon's is uiserver's, over internal/tilelayout. Each write is one undo
// step.
type LayoutEditor interface {
	// Shown is the pull request's latest saved version as the person is
	// shown it on the tiles page, their layout applied.
	Shown(ctx context.Context, repository string, number int) (ShownLayout, error)
	// Edit checks each op against what Shown shows and saves those that
	// pass as one batch.
	Edit(ctx context.Context, repository string, number int, ops []tiles.Op) (LayoutChange, error)
	// Undo removes the newest batch, whoever made it.
	Undo(ctx context.Context, repository string, number int) (LayoutChange, error)
	// Reset returns to the generated layout, as a batch an undo takes back.
	Reset(ctx context.Context, repository string, number int) (LayoutChange, error)
	// Add adds a library tile type to the review as one batch, with the
	// data the agent wrote for it (checked against the version's hunks and
	// masked) or, without data, filled in the background.
	Add(ctx context.Context, repository string, number int, add TileAdd) (TileAdded, error)
}

// TileAdd is an add_tile of a library type: its name, size, place and the
// data the agent wrote, if any.
type TileAdd struct {
	Type string
	Size tiles.Size
	To   *tiles.Place
	Data json.RawMessage
}

// TileAdded is what an add did: the tile's ID, "ready" when it shows its
// data or "filling" while it is filled, and the layout's change.
type TileAdded struct {
	Tile   string
	State  string
	Change LayoutChange
}

// ShownLayout is a version's tiles as the person is shown them.
type ShownLayout struct {
	Version  int
	Document *tiles.Document
	Revision int64
	Edited   bool
	CanUndo  bool
}

// LayoutChange is what a layout write did, in the host's words.
type LayoutChange struct {
	Applied  []string
	Refused  []LayoutRefusal
	Undone   []string
	Revision int64
	Edited   bool
	CanUndo  bool
	// Saved is false when nothing was written (every op refused).
	Saved bool
}

// LayoutRefusal is an op that was refused, by its index in the call.
type LayoutRefusal struct {
	Index  int    `json:"index"`
	Reason string `json:"reason"`
}

// layoutPageTools are the layout tools, which the pull request page lists.
var layoutPageTools = []string{"tile_layout", "edit_tiles", "tile_type", "add_tile", "undo_tiles", "reset_tiles"}

// errNoLayouts is a daemon without a layout editor.
var errNoLayouts = errors.New("tile layouts are not available in this baum")

// errOtherTurnPage is an edit from a turn begun on another page.
var errOtherTurnPage = errors.New("This conversation was started about another page; ask from the pull request you want to change")

// maxLayoutResult bounds what tile_layout answers.
const maxLayoutResult = 32 << 10

// layoutPull is the pull request the page shows, for a layout tool: only a
// chat on the pull request's page may change its layout.
//
// An edit also needs the turn to have been sent from that same pull
// request: a conversation's page follows the person around the app, so a
// long turn begun about one pull request must not edit another they opened
// meanwhile. Reading a layout has no such rule.
func (s *Server) layoutPull(c CallContext, edits bool) (string, int, error) {
	if s.layouts == nil {
		return "", 0, errNoLayouts
	}
	if c.Consumer.Kind == Action || !c.HasPage || c.Page.Page != PagePullRequest || c.Page.Repository == "" || c.Page.PullRequest < 1 {
		return "", 0, errors.New("the layout tools change the tiles of the pull request page the person has open: open a pull request's page first")
	}
	if edits && c.HasTurn && (c.Turn.Page != PagePullRequest || !strings.EqualFold(c.Turn.Repository, c.Page.Repository) || c.Turn.PullRequest != c.Page.PullRequest) {
		return "", 0, errOtherTurnPage
	}
	return c.Page.Repository, c.Page.PullRequest, nil
}

// focusOf is the tile the page context's focus names: the tile an Ask
// picked (kind tile), else the tile open in its detail.
func focusOf(c CallContext) string {
	if !c.HasPage || len(c.Page.Focus) == 0 {
		return ""
	}
	var focus struct {
		Kind string `json:"kind"`
		ID   string `json:"id"`
		Tile string `json:"tile"`
	}
	if json.Unmarshal(c.Page.Focus, &focus) != nil {
		return ""
	}
	if focus.Kind == "tile" && focus.ID != "" {
		return focus.ID
	}
	return focus.Tile
}

// tilesView reports whether the page shows the tiles page.
func tilesView(c CallContext) bool {
	var focus struct {
		View string `json:"view"`
	}
	return c.HasPage && json.Unmarshal(c.Page.Focus, &focus) == nil && focus.View == "tiles"
}

// layoutTile is a tile as tile_layout lists it.
type layoutTile struct {
	ID        string       `json:"id"`
	Type      string       `json:"type"`
	Title     string       `json:"title"`
	Size      tiles.Size   `json:"size"`
	Sizes     []tiles.Size `json:"sizes"`
	Group     string       `json:"group,omitempty"`
	Pinned    bool         `json:"pinned,omitempty"`
	Hidden    bool         `json:"hidden,omitempty"`
	OnLanding bool         `json:"on_landing"`
	// Row is the tile's row on the page, from 1; 0 when it is folded away.
	Row     int    `json:"row,omitempty"`
	State   string `json:"state"`
	CanHide bool   `json:"can_hide"`
	WhyNot  string `json:"why_not,omitempty"`
}

// layoutView is what tile_layout answers.
type layoutView struct {
	PullRequest     string       `json:"pull_request"`
	Version         int          `json:"version"`
	View            string       `json:"view"`
	Edited          bool         `json:"edited"`
	CanUndo         bool         `json:"can_undo"`
	Focus           *focusTile   `json:"focus"`
	FirstScreenRows int          `json:"first_screen_rows"`
	LandingRows     int          `json:"landing_rows"`
	Rows            [][]string   `json:"rows"`
	Tiles           []layoutTile `json:"tiles"`
	ChangesShown    *int         `json:"changes_shown,omitempty"`
	// Addable are the library tile types the review does not show yet,
	// which add_tile adds.
	Addable []addableType `json:"addable,omitempty"`
	Note    string        `json:"note,omitempty"`
}

// addableType is a library tile type tile_layout lists as addable.
type addableType struct {
	Type    string `json:"type"`
	Title   string `json:"title"`
	UseWhen string `json:"use_when"`
	Source  string `json:"source"`
}

type focusTile struct {
	Tile  string `json:"tile"`
	Title string `json:"title"`
}

// layoutRows lays a document's entries out as the page does: each row its tiles
// as "id:size", the hidden group's members and folded members left out.
func layoutRows(doc *tiles.Document) ([][]string, map[string]int) {
	var rows [][]string
	at := map[string]int{}
	for k, row := range tiles.Rows(doc.Layout) {
		var line []string
		for _, i := range row.Entries {
			entry := doc.Layout.Entries[i]
			if entry.Group == tiles.GroupHidden {
				continue
			}
			line = append(line, entry.Tile+":"+string(entry.Size))
			at[entry.Tile] = k + 1
		}
		if len(line) > 0 {
			rows = append(rows, line)
		}
	}
	return rows, at
}

func viewOf(c CallContext, repository string, number int, shown ShownLayout) layoutView {
	doc := shown.Document
	rows, at := layoutRows(doc)
	visible := tiles.Visible(doc.Layout)
	out := layoutView{PullRequest: fmt.Sprintf("%s#%d", repository, number), Version: shown.Version, View: "full", Edited: shown.Edited, CanUndo: shown.CanUndo,
		FirstScreenRows: tiles.FirstScreenRows, LandingRows: tiles.LandingRows, Rows: rows}
	if tilesView(c) {
		out.View = "tiles"
	} else {
		out.Note = "The page shows the full review: layout edits apply now and show on its tiles page (Tiles in the page's view switch, or ?view=tiles)."
	}
	if id := focusOf(c); id != "" {
		out.Focus = &focusTile{Tile: id, Title: tiles.TileTitle(doc, id)}
	}
	byID := map[string]tiles.Tile{}
	for _, tile := range doc.Tiles {
		byID[tile.ID] = tile
	}
	for i, entry := range doc.Layout.Entries {
		tile, ok := byID[entry.Tile]
		if !ok {
			continue
		}
		hidden := entry.Group == tiles.GroupHidden
		item := layoutTile{ID: tile.ID, Type: tile.Type, Title: tiles.TileTitle(doc, tile.ID), Size: entry.Size, Sizes: tiles.SizesIn(doc, tile),
			Pinned: entry.Pin, Hidden: hidden, OnLanding: visible[i], Row: at[tile.ID], State: tile.State}
		if !hidden {
			item.Group = entry.Group
		}
		if hidden {
			item.WhyNot = "already hidden: show brings it back"
		} else if reason := tiles.CheckOp(doc, tiles.Op{Op: tiles.OpHide, Tile: tile.ID}); reason != "" {
			item.WhyNot = reason
		} else {
			item.CanHide = true
		}
		out.Tiles = append(out.Tiles, item)
	}
	for _, g := range doc.Layout.Groups {
		if g.ID == tiles.GroupChanges && g.CollapseAfter != nil {
			out.ChangesShown = new(*g.CollapseAfter)
		}
	}
	for _, e := range library.All() {
		if _, ok := byID[tiles.LibraryTileID(e.Definition.Name)]; !ok {
			out.Addable = append(out.Addable, addableType{Type: e.Definition.Name, Title: e.Definition.Title, UseWhen: e.Definition.UseWhen, Source: "built-in"})
		}
	}
	return out
}

// boundedView encodes a view within maxLayoutResult, dropping the tiles'
// size lists, then the tiles past the landing page, when it does not fit.
func boundedView(v layoutView) (string, error) {
	text, err := compactJSON(v)
	if err != nil || len(text) <= maxLayoutResult {
		return text, err
	}
	for i := range v.Tiles {
		v.Tiles[i].Sizes = nil
	}
	if text, err = compactJSON(v); err != nil || len(text) <= maxLayoutResult {
		return text, err
	}
	v.Tiles = slices.DeleteFunc(v.Tiles, func(t layoutTile) bool { return !t.OnLanding })
	v.Note = strings.TrimSpace(v.Note + " Only the landing page's tiles are listed: the review has too many to list.")
	return compactJSON(v)
}

// opArgs is an op as edit_tiles takes it.
type opArgs struct {
	Op    string       `json:"op"`
	Tile  string       `json:"tile,omitempty"`
	To    *tiles.Place `json:"to,omitempty"`
	Size  tiles.Size   `json:"size,omitempty"`
	Count *int         `json:"count,omitempty"`
}

// layoutResult is what an edit, an undo or a reset answers: what it did, in
// the host's words, and the rows the page shows now.
type layoutResult struct {
	Applied  []string        `json:"applied,omitempty"`
	Refused  []LayoutRefusal `json:"refused,omitempty"`
	Undone   []string        `json:"undone,omitempty"`
	Reset    bool            `json:"reset,omitempty"`
	Revision int64           `json:"revision"`
	Edited   bool            `json:"edited"`
	CanUndo  bool            `json:"can_undo"`
	Rows     [][]string      `json:"rows,omitempty"`
	Hidden   []string        `json:"hidden,omitempty"`
	Note     string          `json:"note,omitempty"`
}

// result answers a write, with the rows of the page after it.
func (s *Server) result(ctx context.Context, c CallContext, repository string, number int, change LayoutChange) (string, error) {
	out := layoutResult{Applied: change.Applied, Refused: change.Refused, Undone: change.Undone, Revision: change.Revision, Edited: change.Edited, CanUndo: change.CanUndo}
	if shown, err := s.layouts.Shown(ctx, repository, number); err == nil {
		out.Rows, _ = layoutRows(shown.Document)
		for _, entry := range shown.Document.Layout.Entries {
			if entry.Group == tiles.GroupHidden {
				out.Hidden = append(out.Hidden, entry.Tile)
			}
		}
	}
	if !tilesView(c) {
		out.Note = "The page shows the full review: the edit shows on its tiles page."
	}
	return compactJSON(out)
}

const layoutRule = " It changes only the person's own layout of the pull request page they have open, at once, with an Undo on the page; never the review's content."

// layoutTools are the tools that read and change the person's own layout.
func (s *Server) layoutTools() []Tool {
	place := `"to":{"type":"object","description":"Where a move puts the tile: exactly one of edge (first: the top, after any pinned tiles; last: the end), before (a tile id) or after (a tile id).","properties":{` +
		`"edge":{"type":"string","enum":["first","last"]},"before":{"type":"string","maxLength":128},"after":{"type":"string","maxLength":128}},"minProperties":1,"maxProperties":1,"additionalProperties":false}`
	size := `"size":{"type":"string","enum":["full","half","third"],"description":"full is the whole row, half half of it, third a third; tile_layout lists each tile's sizes."}`
	return []Tool{{
		Name: "tile_layout",
		Description: "The tiles of the review on the pull request page the person has open, as the tiles page lays them out: rows from the top (each tile as id:size; the first 2 rows are the first screen, the first 3 the landing page), and each tile's id, type, title, size, allowed sizes, group, whether it is pinned, hidden (removed, kept in the Hidden tiles group at the end) or on the landing page, and whether it can be removed. " +
			"focus is the tile the person pointed at (Ask about this tile, or the tile they have open): \"this\", \"it\" or \"that tile\" mean it; with no focus, ask which tile they mean. Read this before edit_tiles.",
		Call: func(ctx context.Context, c CallContext, _ json.RawMessage) (string, error) {
			repository, number, err := s.layoutPull(c, false)
			if err != nil {
				return "", err
			}
			shown, err := s.layouts.Shown(ctx, repository, number)
			if err != nil {
				return "", err
			}
			return boundedView(viewOf(c, repository, number, shown))
		},
	}, {
		Name: "edit_tiles",
		Description: "Changes the layout of the review's tiles on the pull request page the person has open, at once, as one undo step: move a tile (to the top, the end, or before or after another tile), resize it (full, half or third width), hide it (remove it from the grid to the Hidden tiles group at the end, where it stays reachable), show a hidden tile again, pin or unpin it (pinned tiles lead the page), or set how many changes the changes group shows before folding (changes_shown with count). " +
			"Use tile ids from tile_layout; \"this tile\" is tile_layout's focus. Several ops in one call are one undo step. Tiles stand side by side only when they share a row (6 units: full 6, half 3, third 2): to put one beside another, move it next to it and size the tiles of that row to fit, in one call. The overview, and the risks tile while it lists a critical or high risk, always stay on the landing page and cannot be hidden. The result lists what applied in the host's words, what was refused and why, and the rows the page shows now; tell the person what changed, and that Undo on the page or undo_tiles takes it back." + layoutRule,
		InputSchema: json.RawMessage(`{"type":"object","properties":{"ops":{"type":"array","minItems":1,"maxItems":20,"items":{"type":"object","properties":{` +
			`"op":{"type":"string","enum":["move","resize","hide","show","pin","unpin","changes_shown"]},` +
			`"tile":{"type":"string","maxLength":128,"description":"The tile's id, from tile_layout; every op but changes_shown names one."},` +
			place + `,` + size + `,` +
			`"count":{"type":"integer","minimum":1,"maximum":50,"description":"For changes_shown: how many changes show before Show more."}},` +
			`"required":["op"],"additionalProperties":false}}},"required":["ops"],"additionalProperties":false}`),
		Edits: true,
		Call: func(ctx context.Context, c CallContext, args json.RawMessage) (string, error) {
			repository, number, err := s.layoutPull(c, true)
			if err != nil {
				return "", err
			}
			var a struct {
				Ops []opArgs `json:"ops"`
			}
			if err := strictArgs(args, &a); err != nil {
				return "", err
			}
			ops := make([]tiles.Op, len(a.Ops))
			for i, op := range a.Ops {
				ops[i] = tiles.Op{Op: op.Op, Tile: strings.TrimSpace(op.Tile), To: op.To, Size: op.Size, Count: op.Count}
			}
			return s.edit(ctx, c, repository, number, ops)
		},
	}, {
		Name: "tile_type",
		Description: "A library tile type the person can add to the review on the pull request page they have open (tile_layout lists them under addable): its title, when it applies, what its glance shows, the instructions for filling its data and the JSON Schema the data must match. " +
			"Read it before add_tile, then write the data yourself from the diff and the checkout you already have, and pass it to add_tile: that shows the tile at once and costs no extra run.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"type":{"type":"string","minLength":1,"maxLength":128,"description":"The type's name from tile_layout's addable, such as api_change."}},"required":["type"],"additionalProperties":false}`),
		Call: func(ctx context.Context, c CallContext, args json.RawMessage) (string, error) {
			if _, _, err := s.layoutPull(c, false); err != nil {
				return "", err
			}
			var a struct {
				Type string `json:"type"`
			}
			if err := strictArgs(args, &a); err != nil {
				return "", err
			}
			e, ok := library.Find(a.Type)
			if !ok {
				return "", unknownType(a.Type)
			}
			schema, err := e.FillSchema()
			if err != nil {
				return "", err
			}
			return compactJSON(struct {
				Type         string          `json:"type"`
				Title        string          `json:"title"`
				UseWhen      string          `json:"use_when"`
				Glance       string          `json:"glance"`
				Sizes        []tiles.Size    `json:"sizes"`
				Instructions string          `json:"fill_instructions"`
				Schema       json.RawMessage `json:"fill_schema"`
				Note         string          `json:"note"`
			}{e.Definition.Name, e.Definition.Title, e.Definition.UseWhen, e.Glance, e.Definition.Sizes, e.Definition.Fill, schema,
				"Write data matching fill_schema from this pull request's diff and checkout; cite hunks by the IDs the diff tool gives. The host checks it, and every hunk it cites must be in the version shown. If nothing this type shows changes, write empty lists."})
		},
	}, {
		Name: "add_tile",
		Description: "Adds a tile to the review on the pull request page the person has open, at once, as one undo step, optionally at a size and a place: a library tile type the review does not show yet (tile_layout's addable, such as api_change), or a tile the person removed, which it brings back. " +
			"For a library type, first read tile_type and write its data yourself from the diff and checkout you have, then pass it as data: the tile shows at once. The host checks the data against the type's schema and this version's hunks and says where it does not fit, never changing anything then; fix it and call again. " +
			"With no place, a new tile stands right after the overview. Without data the tile shows as being filled and baum fills it in the background, which costs a separate model run; it fills it again for later versions of the pull request." + layoutRule,
		InputSchema: json.RawMessage(`{"type":"object","properties":{"type":{"type":"string","minLength":1,"maxLength":128,"description":"A library type's name from tile_layout's addable, or a removed tile's id or type."},` +
			size + `,` + place + `,"data":{"type":"object","description":"The tile's data, matching tile_type's fill_schema."}},"required":["type"],"additionalProperties":false}`),
		Edits: true,
		Call: func(ctx context.Context, c CallContext, args json.RawMessage) (string, error) {
			repository, number, err := s.layoutPull(c, true)
			if err != nil {
				return "", err
			}
			var a struct {
				Type string          `json:"type"`
				Size tiles.Size      `json:"size"`
				To   *tiles.Place    `json:"to"`
				Data json.RawMessage `json:"data"`
			}
			if err := strictArgs(args, &a); err != nil {
				return "", err
			}
			name := strings.TrimSpace(a.Type)
			shown, err := s.layouts.Shown(ctx, repository, number)
			if err != nil {
				return "", err
			}
			id, hidden, found := tileOfType(shown.Document, name)
			if e, ok := library.Find(name); ok && !found {
				added, err := s.layouts.Add(ctx, repository, number, TileAdd{Type: e.Definition.Name, Size: a.Size, To: a.To, Data: a.Data})
				if err != nil {
					return "", err
				}
				return s.addResult(ctx, c, repository, number, added)
			}
			switch {
			case !found:
				return "", unknownType(name)
			case !hidden:
				return "", fmt.Errorf("%s is already on the review: edit_tiles moves or resizes it", tiles.TileTitle(shown.Document, id))
			case len(a.Data) > 0:
				return "", fmt.Errorf("%s is on the review already, removed: add it without data to bring it back", tiles.TileTitle(shown.Document, id))
			}
			ops := []tiles.Op{{Op: tiles.OpShow, Tile: id}}
			if a.Size != "" {
				ops = append(ops, tiles.Op{Op: tiles.OpResize, Tile: id, Size: a.Size})
			}
			if a.To != nil {
				ops = append(ops, tiles.Op{Op: tiles.OpMove, Tile: id, To: a.To})
			}
			return s.edit(ctx, c, repository, number, ops)
		},
	}, {
		Name:        "undo_tiles",
		Description: "Undoes the last change to the layout of the review on the pull request page the person has open, whoever made it (you or the person on the page), at once. The result says what was undone. There is no redo: make the edit again instead." + layoutRule,
		Edits:       true,
		Call: func(ctx context.Context, c CallContext, _ json.RawMessage) (string, error) {
			repository, number, err := s.layoutPull(c, true)
			if err != nil {
				return "", err
			}
			change, err := s.layouts.Undo(ctx, repository, number)
			if err != nil {
				return "", err
			}
			return s.result(ctx, c, repository, number, change)
		},
	}, {
		Name:        "reset_tiles",
		Description: "Returns the review on the pull request page the person has open to its generated layout, at once, dropping the person's layout edits; it is one undo step, so undo_tiles brings the edits back." + layoutRule,
		Edits:       true,
		Call: func(ctx context.Context, c CallContext, _ json.RawMessage) (string, error) {
			repository, number, err := s.layoutPull(c, true)
			if err != nil {
				return "", err
			}
			change, err := s.layouts.Reset(ctx, repository, number)
			if err != nil {
				return "", err
			}
			return s.result(ctx, c, repository, number, change)
		},
	}}
}

// edit saves ops and answers what applied; when every op was refused it
// fails with the reasons, so the agent says why nothing changed.
func (s *Server) edit(ctx context.Context, c CallContext, repository string, number int, ops []tiles.Op) (string, error) {
	change, err := s.layouts.Edit(ctx, repository, number, ops)
	if err != nil {
		return "", err
	}
	if !change.Saved {
		reasons := make([]string, len(change.Refused))
		for i, refusal := range change.Refused {
			reasons[i] = refusal.Reason
		}
		return "", fmt.Errorf("Nothing changed: %s", strings.Join(reasons, "; "))
	}
	return s.result(ctx, c, repository, number, change)
}

// unknownType refuses a type that is neither a library type nor a tile of
// the review, naming the library types.
func unknownType(name string) error {
	names := make([]string, 0, len(library.All()))
	for _, e := range library.All() {
		names = append(names, e.Definition.Name+" ("+e.Definition.Title+")")
	}
	return fmt.Errorf("No tile type %s: the library types are %s, and tile_layout lists the review's tiles", name, strings.Join(names, ", "))
}

// addResult answers an add: what applied, the tile and whether it shows its
// data or is being filled, and the rows of the page after it.
func (s *Server) addResult(ctx context.Context, c CallContext, repository string, number int, added TileAdded) (string, error) {
	text, err := s.result(ctx, c, repository, number, added.Change)
	if err != nil {
		return "", err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(text), &fields); err != nil {
		return "", err
	}
	fields["tile"], _ = json.Marshal(added.Tile)
	fields["state"], _ = json.Marshal(added.State)
	if added.State == "filling" {
		fields["note"], _ = json.Marshal("The tile shows as being filled until baum's fill lands, usually within a minute; tell the person.")
	}
	return compactJSON(fields)
}

// tileOfType finds a tile by its ID, or by its type when the review has one
// tile of it, and says whether it is hidden.
func tileOfType(doc *tiles.Document, name string) (id string, hidden, found bool) {
	groups := map[string]string{}
	for _, entry := range doc.Layout.Entries {
		groups[entry.Tile] = entry.Group
	}
	var ofType []string
	for _, tile := range doc.Tiles {
		if tile.ID == name || tiles.LibraryTileID(name) == tile.ID {
			return tile.ID, groups[tile.ID] == tiles.GroupHidden, true
		}
		if tile.Type == name {
			ofType = append(ofType, tile.ID)
		}
	}
	if len(ofType) == 1 {
		return ofType[0], groups[ofType[0]] == tiles.GroupHidden, true
	}
	return "", false, false
}
