package tileviewtest

import (
	"embed"
	"encoding/json"
	"strings"

	"github.com/baum-ai/baum/tiles"
	"github.com/baum-ai/baum/tiles/tilestest"
)

// customFiles are the synthetic custom tiles: for each, <id>.json holds the
// tile's ID and title, its definition without the HTML and digest, and its
// data, and <id>.html the HTML. Data cites "@hunk" where the document's
// first hunk goes.
//
//go:embed testdata/custom
var customFiles embed.FS

// The synthetic custom tiles, in the order AddCustomTiles adds them.
const (
	// WidgetHealth is a well-behaved tile written against the kit's
	// documented API; it is ready.
	WidgetHealth = "widget_health"
	// EscapeProbe tries every way out of its frame and posts what each did
	// to the page as {probe: "escape_probe", results}; it is ready.
	EscapeProbe = "escape_probe"
	// NewerKit names a kit major this version does not ship; it is
	// unavailable, so its frame is never registered.
	NewerKit = "newer_kit"
)

// CustomTileIDs are the synthetic custom tiles' IDs, in order.
var CustomTileIDs = []string{WidgetHealth, EscapeProbe, NewerKit}

type customFile struct {
	ID         string               `json:"id"`
	Title      string               `json:"title"`
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
		out = append(out, tilestest.CustomTile(f.ID, f.Title, f.Definition, data))
	}
	return out
}

// AddCustomTiles adds the synthetic custom tiles to a converted document,
// citing its first hunk, each placed at the end of the layout at full width,
// and arranges the layout again (which gives a tile that does not declare
// full its first declared size). It has the signature
// tileview.SetFixture takes.
func AddCustomTiles(doc *tiles.Document) {
	var index struct {
		Hunks []struct {
			ID string `json:"id"`
		} `json:"hunks"`
	}
	_ = json.Unmarshal(doc.Index, &index)
	hunk := ""
	if len(index.Hunks) > 0 {
		hunk = index.Hunks[0].ID
	}
	for _, tile := range CustomTiles(hunk) {
		doc.Tiles = append(doc.Tiles, tile)
		doc.Layout.Entries = append(doc.Layout.Entries, tiles.Entry{Tile: tile.ID, Size: tiles.SizeFull})
	}
	tiles.Arrange(doc)
}

func mustQuote(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		panic(err)
	}
	return string(b)
}
