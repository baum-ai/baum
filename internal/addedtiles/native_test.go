package addedtiles_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/baum-ai/baum/internal/addedtiles"
)

const nativeTile = "added_flow_0a1b2c3d4e"

func addNative(t *testing.T, s *addedtiles.Service, version int) {
	t.Helper()
	ctx := context.Background()
	if err := s.Add(ctx, pr, addedtiles.Added{Tile: nativeTile, Kind: addedtiles.KindNative, Name: "flow", Digest: addedtiles.NativeDigest, Section: "sec_a", By: "chat"}); err != nil {
		t.Fatal(err)
	}
	native := json.RawMessage(`{"type":"flow","title":"Saving","cites":[],"data":{"lanes":[]}}`)
	if err := s.PutNative(ctx, pr, nativeTile, version, native); err != nil {
		t.Fatal(err)
	}
}

// A native added tile shows the data written for it on its version and,
// marked as from that version, on every later one; never on an earlier
// one, and nothing fills it.
func TestANativeTileShowsItsDataFromItsVersionOn(t *testing.T) {
	s, _ := newService(t)
	addNative(t, s, 2)
	ctx := context.Background()
	added, _ := s.List(ctx, pr)
	fills, _ := s.Fills(ctx, pr)
	for _, c := range []struct {
		version, from int
		shown         bool
	}{{1, 0, false}, {2, 0, true}, {3, 2, true}} {
		held, missing := addedtiles.AddedFor(added, fills, addedtiles.Version{Number: c.version, Latest: true}, func(string) (json.RawMessage, bool) { return nil, false })
		if len(missing) != 0 {
			t.Errorf("version %d: a native tile is to be filled", c.version)
		}
		if !c.shown {
			if len(held) != 0 {
				t.Errorf("version %d shows %+v", c.version, held)
			}
			continue
		}
		if len(held) != 1 || held[0].Native == nil || held[0].Native.ID != nativeTile || held[0].Native.Title != "Saving" || held[0].From != c.from || held[0].Section != "sec_a" || held[0].Filling {
			t.Errorf("version %d: %+v", c.version, held)
		}
	}
}

// An added image is kept while its pull request has an added tile, and
// goes with the last one.
func TestAddedImagesAreKeptWithThePullRequestsTiles(t *testing.T) {
	s, _ := newService(t)
	ctx := context.Background()
	id := "att_0123456789abcdef0123456789abcdef"
	if err := s.PutImage(ctx, pr, id, addedtiles.Image{ContentType: "image/png", Bytes: []byte("png")}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutImage(ctx, pr, "att_short", addedtiles.Image{ContentType: "image/png", Bytes: []byte("png")}); err == nil {
		t.Error("an id that is not an attachment id was taken")
	}
	if err := s.PutImage(ctx, pr, id, addedtiles.Image{ContentType: "text/html", Bytes: []byte("x")}); err == nil {
		t.Error("a type that is not an image was taken")
	}
	addNative(t, s, 1)
	image, err := s.Image(ctx, pr, id)
	if err != nil || image.ContentType != "image/png" || string(image.Bytes) != "png" {
		t.Fatalf("the image: %+v %v", image, err)
	}
	if _, err := s.Image(ctx, "acme/widgets#8", id); !errors.Is(err, addedtiles.ErrNotFound) {
		t.Errorf("another pull request's: %v", err)
	}
	if err := s.Remove(ctx, pr, nativeTile); err != nil {
		t.Fatal(err)
	}
	// A removed tile keeps its image, for Undo.
	if err := s.PutNative(ctx, pr, nativeTile, 1, json.RawMessage(`{"type":"flow","title":"x","cites":[],"data":{}}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Image(ctx, pr, id); err != nil {
		t.Errorf("a removed tile's image: %v", err)
	}
}
