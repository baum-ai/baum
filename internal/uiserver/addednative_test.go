package uiserver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/baum-ai/baum/internal/agentcontext"
	"github.com/baum-ai/baum/internal/tileview/tileviewtest"
	"github.com/baum-ai/baum/reviewv2"
)

// add_tile's built-in types are the ones the host adds, by the same names.
func TestTheBuiltinTypesAreTheNativeAddTypes(t *testing.T) {
	var names []string
	for _, b := range agentcontext.BuiltinTypes {
		names = append(names, b.Name)
	}
	if !slices.Equal(names, NativeAddTypes) {
		t.Fatalf("agentcontext lists %v, uiserver adds %v", names, NativeAddTypes)
	}
}

// flowData is a flow diagram as a tile agent writes it, resting on hunk.
func flowData(hunk, title string) json.RawMessage {
	return json.RawMessage(fmt.Sprintf(`{"type":"flow","title":%q,"caption":"How a save moves.","source":[%q],
"lanes":[{"label":null,"from":null,"steps":[{"title":"Person saves","note":null,"more":null,"tone":null,"join":null},{"title":"Daemon stores it","note":"in app.sqlite","more":null,"tone":"new","join":"arrow"}]}]}`, title, hunk))
}

// A built-in diagram added with the agent's data shows at once, ready, on
// the section its source cites, with its caption and citations lifted out
// of its data as a generated tile's are; nothing is filled. Data that does
// not fit, or none, changes nothing.
func TestAddABuiltinDiagram(t *testing.T) {
	f, filler := newAddedFixture(t)
	f.put(t, titled(t, "First title"))
	adder := AddedTilesAPI{API: *f.api}
	ctx := context.Background()
	hunk := f.diffOf(t)[0].ID

	if _, err := adder.Add(ctx, "acme/widgets", 42, agentcontext.TileAdd{Type: "flow"}); err == nil || !strings.Contains(err.Error(), "needs its data") {
		t.Fatalf("no data: %v", err)
	}
	if _, err := adder.Add(ctx, "acme/widgets", 42, agentcontext.TileAdd{Type: "flow", Data: flowData("h_000000000000", "Saving")}); err == nil || !strings.Contains(err.Error(), "name no hunk") {
		t.Fatalf("a source that is no hunk: %v", err)
	}
	long := flowData(hunk, strings.Repeat("word ", 40))
	if _, err := adder.Add(ctx, "acme/widgets", 42, agentcontext.TileAdd{Type: "flow", Data: long}); err == nil {
		t.Fatal("a title over its limit was taken")
	}
	if list, _ := f.api.Added.List(ctx, "acme/widgets#42"); len(list) != 0 {
		t.Fatalf("a refused add was kept: %+v", list)
	}

	added, err := adder.Add(ctx, "acme/widgets", 42, agentcontext.TileAdd{Type: "flow", Data: flowData(hunk, "Saving a tile")})
	if err != nil || added.State != "ready" || !strings.HasPrefix(added.Tile, "added_flow_") || added.Section == "" {
		t.Fatalf("add: %+v %v", added, err)
	}
	second, err := adder.Add(ctx, "acme/widgets", 42, agentcontext.TileAdd{Type: "flow", Title: "Another", Data: flowData(hunk, "Ignored title")})
	if err != nil || second.Tile == added.Tile {
		t.Fatalf("a second flow: %+v %v", second, err)
	}
	v := f.served(t, "")
	tile := servedTile(v, added.Tile)
	if tile == nil || tile.Type != "flow" || tile.State != reviewv2.TileReady || !tile.Added || tile.Title != "Saving a tile" || tile.Caption != "How a save moves." || len(tile.Cites) != 1 || tile.Owner != added.Section {
		t.Fatalf("the tile: %+v", tile)
	}
	var data map[string]json.RawMessage
	if json.Unmarshal(tile.Data, &data) != nil || data["lanes"] == nil || data["caption"] != nil || data["source"] != nil {
		t.Errorf("its data: %s", tile.Data)
	}
	if other := servedTile(v, second.Tile); other == nil || other.Title != "Another" {
		t.Errorf("the second tile: %+v", other)
	}
	if len(filler.asked()) != 0 {
		t.Errorf("a built-in tile started a fill: %+v", filler.asked())
	}

	// A later version shows it, marked as from the version it was written
	// for; Remove takes it off.
	f.put(t, titled(t, "Second title"))
	if later := servedTile(f.served(t, ""), added.Tile); later == nil || later.From != 1 || later.Filling {
		t.Errorf("on the next version: %+v", later)
	}
	if err := f.api.Added.Remove(ctx, "acme/widgets#42", added.Tile); err != nil {
		t.Fatal(err)
	}
	if servedTile(f.served(t, ""), added.Tile) != nil {
		t.Error("a removed tile is still served")
	}
}

// writePNG writes a w×h PNG to dir and returns its path.
func writePNG(t *testing.T, dir, name string, w, h int) string {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for x := range w {
		img.Set(x, x%h, color.NRGBA{R: 200, A: 255})
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, b.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// An images tile reads each file the agent names, stores it with the pull
// request's added tiles, and is served with the images listed in the
// document's attachments, which the attachment route serves for every
// version. A file that is not an image, or a relative path, changes
// nothing.
func TestAddAnImagesTile(t *testing.T) {
	f, _ := newAddedFixture(t)
	f.put(t, titled(t, "First title"))
	adder := AddedTilesAPI{API: *f.api}
	ctx := context.Background()
	dir := t.TempDir()
	shot := writePNG(t, dir, "switch.png", 40, 20)
	notImage := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(notImage, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, data := range []string{
		fmt.Sprintf(`{"items":[{"path":%q}]}`, notImage),
		`{"items":[{"path":"switch.png"}]}`,
		`{"items":[]}`,
		fmt.Sprintf(`{"items":[{"path":%q,"bytes":"AAAA"}]}`, shot),
	} {
		if _, err := adder.Add(ctx, "acme/widgets", 42, agentcontext.TileAdd{Type: "images", Data: json.RawMessage(data)}); err == nil {
			t.Errorf("%s was taken", data)
		}
	}
	if list, _ := f.api.Added.List(ctx, "acme/widgets#42"); len(list) != 0 {
		t.Fatalf("a refused add was kept: %+v", list)
	}
	data := json.RawMessage(fmt.Sprintf(`{"items":[{"path":%q,"caption":"The switch, on"}]}`, shot))
	added, err := adder.Add(ctx, "acme/widgets", 42, agentcontext.TileAdd{Type: "images", Title: "Review automatically", Section: "context", Data: data})
	if err != nil || added.State != "ready" || added.Section != "context" {
		t.Fatalf("add: %+v %v", added, err)
	}
	f.put(t, titled(t, "Second title"))
	v := f.served(t, "")
	tile := servedTile(v, added.Tile)
	if tile == nil || tile.Type != reviewv2.TypeImages || tile.State != reviewv2.TileReady || tile.Title != "Review automatically" || tile.Owner != "context" {
		t.Fatalf("the tile: %+v", tile)
	}
	var images reviewv2.ImagesData
	if json.Unmarshal(tile.Data, &images) != nil || len(images.Items) != 1 || images.Items[0].Caption != "The switch, on" || images.Items[0].Width != 40 || images.Items[0].Height != 20 {
		t.Fatalf("its data: %s", tile.Data)
	}
	id := images.Items[0].Attachment
	var index struct {
		Attachments []struct {
			ID          string `json:"id"`
			ContentType string `json:"content_type"`
			Width       int    `json:"width"`
			Alt         string `json:"alt"`
		} `json:"attachments"`
	}
	if json.Unmarshal(v.Index, &index) != nil || !slices.ContainsFunc(index.Attachments, func(a struct {
		ID          string `json:"id"`
		ContentType string `json:"content_type"`
		Width       int    `json:"width"`
		Alt         string `json:"alt"`
	}) bool {
		return a.ID == id && a.ContentType == "image/png" && a.Width == 40 && a.Alt == "The switch, on"
	}) {
		t.Fatalf("the index lists %s", v.Index)
	}
	for _, version := range []string{"", "&version=1", "&version=2"} {
		w := f.get("/api/attachments/"+id, version, "")
		if w.Code != 200 || w.Header().Get("Content-Type") != "image/png" || w.Body.Len() == 0 {
			t.Errorf("the image%s: %d %s", version, w.Code, w.Header().Get("Content-Type"))
		}
	}
	if w := f.get("/api/attachments/att_00000000000000000000000000000000", "", ""); w.Code != 404 {
		t.Errorf("an image no one stored: %d", w.Code)
	}
}

// A large screenshot is downscaled as a stored session image is.
func TestAnAddedImageIsDownscaled(t *testing.T) {
	f, _ := newAddedFixture(t)
	f.put(t, titled(t, "First title"))
	shot := writePNG(t, t.TempDir(), "large.png", 3200, 1000)
	added, err := (AddedTilesAPI{API: *f.api}).Add(context.Background(), "acme/widgets", 42, agentcontext.TileAdd{Type: "images", Data: json.RawMessage(fmt.Sprintf(`{"items":[{"path":%q}]}`, shot))})
	if err != nil {
		t.Fatal(err)
	}
	var images reviewv2.ImagesData
	if tile := servedTile(f.served(t, ""), added.Tile); tile == nil || json.Unmarshal(tile.Data, &images) != nil || len(images.Items) != 1 || images.Items[0].Width != 1568 {
		t.Fatalf("the tile: %+v", tile)
	}
}

// A code tile names a hunk and its lines; the host reads them from the
// clone at the version's head, so the agent never writes the code.
func TestAddACodeTile(t *testing.T) {
	f, _ := newAddedFixture(t)
	hunk := tileviewHunk(t)
	lines := make([]string, hunk.last+5)
	for i := range lines {
		lines[i] = fmt.Sprintf("line %d", i+1)
	}
	head := commitFile(t, f.clone.Path, hunk.path, strings.Join(lines, "\n")+"\n")
	doc := tileviewtest.Base(t)
	doc["review"].(map[string]any)["head"] = head
	f.put(t, tileviewtest.Encode(t, doc))
	adder := AddedTilesAPI{API: *f.api}
	ctx := context.Background()
	for _, data := range []string{
		`{"hunk":"h_000000000000"}`,
		fmt.Sprintf(`{"hunk":%q,"text":"made up"}`, hunk.id),
		fmt.Sprintf(`{"hunk":%q,"lines":[%d,%d]}`, hunk.id, hunk.first-1, hunk.last),
	} {
		if _, err := adder.Add(ctx, "acme/widgets", 42, agentcontext.TileAdd{Type: "code", Data: json.RawMessage(data)}); err == nil {
			t.Errorf("%s was taken", data)
		}
	}
	added, err := adder.Add(ctx, "acme/widgets", 42, agentcontext.TileAdd{Type: "code", Data: json.RawMessage(fmt.Sprintf(`{"hunk":%q,"lines":[%d,%d]}`, hunk.id, hunk.first, hunk.first))})
	if err != nil {
		t.Fatal(err)
	}
	tile := servedTile(f.served(t, ""), added.Tile)
	var code reviewv2.CodeData
	if tile == nil || tile.State != reviewv2.TileReady || json.Unmarshal(tile.Data, &code) != nil || code.Text != fmt.Sprintf("line %d", hunk.first) {
		t.Fatalf("the tile: %+v", tile)
	}
}

// codeHunk is a hunk of the fixture review with lines on its new side.
type codeHunk struct {
	id, path    string
	first, last int
}

func tileviewHunk(t *testing.T) codeHunk {
	t.Helper()
	doc := tileviewtest.Base(t)
	index, _ := doc["index"].(map[string]any)
	hunks, _ := index["hunks"].([]any)
	for _, raw := range hunks {
		h, _ := raw.(map[string]any)
		r, _ := h["new_range"].([]any)
		if len(r) != 2 {
			continue
		}
		start, _ := r[0].(float64)
		count, _ := r[1].(float64)
		if start >= 1 && count >= 1 {
			return codeHunk{id: h["id"].(string), path: h["path"].(string), first: int(start), last: int(start + count - 1)}
		}
	}
	t.Fatal("the fixture has no hunk with new lines")
	return codeHunk{}
}

// commitFile commits content at path in the repository at dir and returns
// the commit.
func commitFile(t *testing.T, dir, path, content string) string {
	t.Helper()
	full := filepath.Join(dir, path)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) string {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.invalid", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.invalid")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatal(string(out), err)
		}
		return strings.TrimSpace(string(out))
	}
	run("add", "--", path)
	run("commit", "-q", "-m", "fixture")
	return run("rev-parse", "HEAD")
}
