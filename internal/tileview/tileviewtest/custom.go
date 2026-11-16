package tileviewtest

import (
	"embed"
	"encoding/json"
	"strings"

	"github.com/baum-ai/baum/tiles"
	"github.com/baum-ai/baum/tiles/tilestest"
)

// customFiles are the synthetic custom tiles: for each, <id>.json holds the
// tile's ID, title and headline, its definition without the HTML and
// digest, and its data, and <id>.html the HTML. Data cites "@hunk" where the
// document's first hunk goes.
//
//go:embed testdata/custom
var customFiles embed.FS

// The synthetic custom tiles, in the order AddCustomTiles adds them.
const (
	// WidgetHealth is a well-behaved tile written against the kit's
	// documented API; it is ready.
	WidgetHealth = "widget_health"
	// EscapeProbe tries every way out of its frame and posts what each did
	// to the page as {probe: "escape_probe", mode, face, results}; it is
	// ready. Its data's mode chooses what it tries (see its HTML);
	// EscapeProbeModes are the other tiles of its type the browser suite
	// adds.
	EscapeProbe = "escape_probe"
	// NewerKit names a kit major this version does not ship; it is
	// unavailable, so its frame is never registered.
	NewerKit = "newer_kit"
)

// CustomTileIDs are the synthetic custom tiles' IDs, in order.
var CustomTileIDs = []string{WidgetHealth, EscapeProbe, NewerKit}

// EscapeProbeModes are the escape probe's other modes, each a tile of its
// own (ID "escape_" and the mode) that AddBrowserTiles adds: their hostile
// acts in a detail would take the page with them, so each has its own.
var EscapeProbeModes = []string{"spoof", "flood", "leave_daemon", "leave_outside", "leave_early", "hello_again", "focus", "busy"}

type customFile struct {
	ID         string               `json:"id"`
	Title      string               `json:"title"`
	Headline   string               `json:"headline"`
	Definition tiles.TypeDefinition `json:"definition"`
	Data       json.RawMessage      `json:"data"`
}

func readCustom(id string) customFile {
	raw, err := customFiles.ReadFile("testdata/custom/" + id + ".json")
	if err != nil {
		panic(err)
	}
	var f customFile
	if err := json.Unmarshal(raw, &f); err != nil {
		panic(err)
	}
	html, err := customFiles.ReadFile("testdata/custom/" + id + ".html")
	if err != nil {
		panic(err)
	}
	f.Definition.Source.HTML = string(html)
	return f
}

// CustomDefinition returns the named synthetic tile's definition with its
// digest written.
func CustomDefinition(id string) tiles.TypeDefinition {
	var def tiles.TypeDefinition
	if err := json.Unmarshal(tilestest.SealDefinition(readCustom(id).Definition), &def); err != nil {
		panic(err)
	}
	return def
}

// CustomTiles returns the synthetic custom tiles, their data citing hunk
// where it cites a hunk.
func CustomTiles(hunk string) []tiles.Tile {
	var out []tiles.Tile
	for _, id := range CustomTileIDs {
		f := readCustom(id)
		data := json.RawMessage(strings.ReplaceAll(string(f.Data), `"@hunk"`, mustQuote(hunk)))
		tile := tilestest.CustomTile(f.ID, f.Title, f.Definition, data)
		tile.Headline = f.Headline
		out = append(out, tile)
	}
	return out
}

// EscapeProbeTiles returns a tile of the escape probe's type for each of
// EscapeProbeModes, citing hunk.
func EscapeProbeTiles(hunk string) []tiles.Tile {
	f := readCustom(EscapeProbe)
	var out []tiles.Tile
	for _, mode := range EscapeProbeModes {
		var data map[string]any
		if err := json.Unmarshal(f.Data, &data); err != nil {
			panic(err)
		}
		data["mode"] = mode
		data["cite"] = map[string]any{"type": "hunk", "id": hunk}
		raw, err := json.Marshal(data)
		if err != nil {
			panic(err)
		}
		out = append(out, tilestest.CustomTile("escape_"+mode, "Escape probe: "+strings.ReplaceAll(mode, "_", " "), f.Definition, raw))
	}
	return out
}

// AddCustomTiles adds the synthetic custom tiles to a converted document,
// citing its first hunk, so the served view places them on the section that
// holds it. It has the signature tileview.SetFixture takes.
func AddCustomTiles(doc *tiles.Document) {
	add(doc, CustomTiles(firstHunk(doc)))
}

// AddBrowserTiles adds what AddCustomTiles adds and the escape probe's
// other modes (EscapeProbeTiles), for the browser suite.
func AddBrowserTiles(doc *tiles.Document) {
	hunk := firstHunk(doc)
	add(doc, append(CustomTiles(hunk), EscapeProbeTiles(hunk)...))
}

func firstHunk(doc *tiles.Document) string {
	var index struct {
		Hunks []struct {
			ID string `json:"id"`
		} `json:"hunks"`
	}
	_ = json.Unmarshal(doc.Index, &index)
	if len(index.Hunks) > 0 {
		return index.Hunks[0].ID
	}
	return ""
}

func add(doc *tiles.Document, custom []tiles.Tile) {
	doc.Tiles = append(doc.Tiles, custom...)
}

func mustQuote(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		panic(err)
	}
	return string(b)
}
