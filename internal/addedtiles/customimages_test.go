package addedtiles_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/baum-ai/baum/internal/addedtiles"
)

const imageID = "att_0123456789abcdef0123456789abcdef"

// A custom tile's data holds references to stored images; inlined, each
// becomes its image as a data: URL, wherever the data holds it, and a
// reference to an image no one stored becomes an empty string. Other
// strings, numbers and data without references are left as they are.
func TestInlineImages(t *testing.T) {
	s, _ := newService(t)
	ctx := context.Background()
	if err := s.PutImage(ctx, pr, imageID, addedtiles.Image{ContentType: "image/png", Bytes: []byte("png")}); err != nil {
		t.Fatal(err)
	}
	ref := addedtiles.ImageRef(imageID)
	if id, ok := addedtiles.ImageRefID(ref); !ok || id != imageID {
		t.Fatalf("ImageRefID(%q) = %q %v", ref, id, ok)
	}
	for _, bad := range []string{"baum-image:att_short", "baum-image:", imageID, "data:image/png;base64,AAAA"} {
		if _, ok := addedtiles.ImageRefID(bad); ok {
			t.Errorf("%q read as a reference", bad)
		}
	}
	missing := addedtiles.ImageRef("att_ffffffffffffffffffffffffffffffff")
	data := json.RawMessage(`{"shot":"` + ref + `","n":1.50,"items":[{"img":"` + ref + `","gone":"` + missing + `","text":"baum-image: words"}]}`)
	got := s.InlineImages(ctx, data)
	var out struct {
		Shot  string      `json:"shot"`
		N     json.Number `json:"n"`
		Items []struct {
			Img, Gone, Text string
		} `json:"items"`
	}
	if err := json.Unmarshal(got, &out); err != nil {
		t.Fatalf("%s: %v", got, err)
	}
	url := "data:image/png;base64," + base64.StdEncoding.EncodeToString([]byte("png"))
	if out.Shot != url || len(out.Items) != 1 || out.Items[0].Img != url || out.Items[0].Gone != "" || out.Items[0].Text != "baum-image: words" || out.N != "1.50" {
		t.Errorf("inlined: %s", got)
	}
	for _, plain := range []string{`{"a":"b","n":1.50}`, `not json baum-image:`} {
		if got := s.InlineImages(ctx, json.RawMessage(plain)); string(got) != plain {
			t.Errorf("%s became %s", plain, got)
		}
	}
	// By its id alone, whichever pull request stored it.
	if image, err := s.ImageByID(ctx, imageID); err != nil || string(image.Bytes) != "png" {
		t.Errorf("ImageByID: %+v %v", image, err)
	}
	if _, err := s.ImageByID(ctx, "att_ffffffffffffffffffffffffffffffff"); !errors.Is(err, addedtiles.ErrNotFound) {
		t.Errorf("ImageByID of no image: %v", err)
	}
}

// An image stored for a pull request without added tiles (a custom tile's
// check stores its images before the tile is added) survives other writes
// for ImageGrace, and goes with the next write after that.
func TestImagesWaitForTheirTilesAdd(t *testing.T) {
	s, _ := newService(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	s.Now = func() time.Time { return now }
	if err := s.PutImage(ctx, pr, imageID, addedtiles.Image{ContentType: "image/png", Bytes: []byte("png")}); err != nil {
		t.Fatal(err)
	}
	// A write about another pull request prunes.
	other := "acme/widgets#8"
	write := func(tile string) {
		t.Helper()
		if err := s.Add(ctx, other, addedtiles.Added{Tile: tile, Kind: addedtiles.KindNative, Name: "flow", Digest: addedtiles.NativeDigest, Section: "", By: "chat"}); err != nil {
			t.Fatal(err)
		}
	}
	now = now.Add(addedtiles.ImageGrace - time.Minute)
	write("added_flow_0a1b2c3d41")
	if _, err := s.Image(ctx, pr, imageID); err != nil {
		t.Fatalf("within the grace: %v", err)
	}
	now = now.Add(2 * time.Minute)
	write("added_flow_0a1b2c3d42")
	if _, err := s.Image(ctx, pr, imageID); !errors.Is(err, addedtiles.ErrNotFound) {
		t.Errorf("after the grace: %v", err)
	}
}
