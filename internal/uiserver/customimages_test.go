package uiserver

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/baum-ai/baum/internal/addedtiles"
	"github.com/baum-ai/baum/internal/tilechecks"
)

// imageData is the starter's data for hunk with one item whose text is
// an {"image_file": path}.
func imageData(hunk string, paths ...string) map[string]any {
	items := []any{}
	for i, path := range paths {
		items = append(items, map[string]any{"title": fmt.Sprintf("Shot %d", i+1), "severity": "low", "text": map[string]any{"image_file": path},
			"cites": []any{map[string]any{"type": "hunk", "id": hunk}}})
	}
	return map[string]any{"summary": "Shows the screen.", "items": items}
}

// A tile made in chat names an image file where its schema has a string:
// the check stores the image with the pull request's added tiles and checks,
// draws and keeps the data with a reference in its place, never the bytes.
// The added tile is served with that reference, the attachment route serves
// the image for the pull request, and the render check's job holds it as a
// data: URL.
func TestATileMadeInChatShowsAnImageFile(t *testing.T) {
	f := newChatTilesFixture(t)
	hunk := f.firstHunk()
	shot := writePNG(t, t.TempDir(), "screen.png", 40, 20)
	answer := f.check(checkArgs(t, "screens", "", imageData(hunk, shot, shot)))
	if answer.Verdict != tilechecks.VerdictPass {
		t.Fatalf("the check: %+v", answer)
	}
	sent := f.checker.last()
	var data struct {
		Items []struct {
			Text string `json:"text"`
		} `json:"items"`
	}
	if err := json.Unmarshal(sent.Data, &data); err != nil || len(data.Items) != 2 {
		t.Fatalf("the data checked: %s", sent.Data)
	}
	id, ok := addedtiles.ImageRefID(data.Items[0].Text)
	if !ok || data.Items[1].Text != data.Items[0].Text || strings.Contains(string(sent.Data), "image_file") || len(sent.Data) > 1024 {
		t.Fatalf("the data checked: %s", sent.Data)
	}
	if image, err := f.api.Added.Image(context.Background(), "acme/widgets#42", id); err != nil || image.ContentType != "image/png" {
		t.Fatalf("the stored image: %+v %v", image, err)
	}
	inlined := string(f.api.Added.InlineImages(context.Background(), sent.Data))
	if strings.Count(inlined, `"data:image/png;base64,`) != 2 || strings.Contains(inlined, addedtiles.ImageRefPrefix) {
		t.Errorf("inlined: %.200s", inlined)
	}

	text, failed := f.call("add_custom_tile", map[string]any{"check": answer.Check})
	if failed {
		t.Fatalf("add_custom_tile: %s", text)
	}
	tile := servedTile(f.servedWithDrafts("").Document, "screens")
	if tile == nil || !strings.Contains(string(tile.Data), addedtiles.ImageRef(id)) {
		t.Fatalf("the tile: %+v", tile)
	}
	if w := f.get("/api/attachments/"+id, "", ""); w.Code != 200 || w.Header().Get("Content-Type") != "image/png" {
		t.Errorf("the attachment route: %d %s", w.Code, w.Header().Get("Content-Type"))
	}
}

// writeNoisePNG writes a w×h PNG of noise, which compresses poorly, to dir.
func writeNoisePNG(t *testing.T, dir, name string, w, h int) string {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	if _, err := rand.Read(img.Pix); err != nil {
		t.Fatal(err)
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

// An image file is read as add_tile's images type reads one: an absolute
// path to a PNG, JPEG, GIF or WebP image of at most 40 MiB, followed through
// a link only to an image. Anything else is a failed check that names the
// problem, never an error, and nothing is checked or stored; so is data
// that names more images than an images tile holds.
func TestImageFilesAChatTileRefuses(t *testing.T) {
	f := newChatTilesFixture(t)
	hunk := f.firstHunk()
	dir := t.TempDir()
	notImage := filepath.Join(dir, "token.txt")
	if err := os.WriteFile(notImage, []byte("ghp_not_an_image"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "shot.png")
	if err := os.Symlink(notImage, link); err != nil {
		t.Fatal(err)
	}
	large := filepath.Join(dir, "large.png")
	if err := os.WriteFile(large, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(large, maxImageFileBytes+1); err != nil {
		t.Fatal(err)
	}
	var many []string
	for i := range maxCustomImages + 1 {
		many = append(many, writePNG(t, dir, fmt.Sprintf("s%d.png", i), 10+i, 10))
	}
	for _, c := range []struct {
		name  string
		paths []string
		says  string
	}{
		{"a relative path", []string{"screen.png"}, "not an absolute path"},
		{"a missing file", []string{filepath.Join(dir, "missing.png")}, "could not be read"},
		{"a text file", []string{notImage}, "is not a PNG, JPEG, GIF or WebP image"},
		{"a link to a text file", []string{link}, "is not a PNG, JPEG, GIF or WebP image"},
		{"a file over the limit", []string{large}, "is over 40 MiB"},
		{"too many images", many, fmt.Sprintf("at most %d images", maxCustomImages)},
	} {
		sent := len(f.checker.sent)
		answer := f.check(checkArgs(t, "screens", "", imageData(hunk, c.paths...)))
		if answer.Verdict != tilechecks.VerdictFail || len(answer.Errors) != 1 || answer.Errors[0].Code != tilechecks.CodeDataInvalid ||
			!strings.Contains(answer.Errors[0].Message, c.says) || !strings.Contains(answer.Errors[0].Message, "image_file") {
			t.Errorf("%s: %+v", c.name, answer)
		}
		if strings.Contains(fmt.Sprint(answer.Errors), "ghp_") {
			t.Errorf("%s: the answer quotes the file", c.name)
		}
		if len(f.checker.sent) != sent {
			t.Errorf("%s: the data was checked", c.name)
		}
	}
	// Naming one image again counts again towards the total: each becomes a
	// whole data: URL in the frame.
	noise := writeNoisePNG(t, dir, "noise.png", 1200, 1200)
	repeats := make([]string, 0, 64)
	for range cap(repeats) {
		repeats = append(repeats, noise)
	}
	answer := f.check(checkArgs(t, "screens", "", imageData(hunk, repeats...)))
	if answer.Verdict != tilechecks.VerdictFail || len(answer.Errors) != 1 || !strings.Contains(answer.Errors[0].Message, "counting each time") {
		t.Errorf("a repeated image: %+v", answer)
	}
	// An object with other keys is data, left to the schema.
	data := imageData(hunk, many[0])
	data["items"].([]any)[0].(map[string]any)["text"] = map[string]any{"image_file": many[0], "alt": "x"}
	f.check(checkArgs(t, "screens", "", data))
	if sent := string(f.checker.last().Data); !strings.Contains(sent, `"image_file"`) {
		t.Errorf("an object with more than image_file was read: %s", sent)
	}
}
