package assemble

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"testing"

	"github.com/baum-ai/baum/bundle"
)

// screenshot draws smooth gradients, whose bytes scale with their pixel count.
func screenshot(t *testing.T, format string, width, height int) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, width, height))
	for y := range height {
		for x := range width {
			img.Set(x, y, color.NRGBA{uint8(x * 255 / width), uint8(y * 255 / height), uint8((x*x + y*y) / 4096), 255})
		}
	}
	var out bytes.Buffer
	var err error
	switch format {
	case "png":
		err = png.Encode(&out, img)
	case "jpeg":
		err = jpeg.Encode(&out, img, &jpeg.Options{Quality: 95})
	case "gif":
		err = gif.Encode(&out, img, nil)
	}
	if err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func sessionImage(content []byte) SessionImage {
	return SessionImage{Bytes: content, Origin: bundle.ImageOrigin{Session: "one", Turn: 1, Input: "p1", Position: 1, Kind: "tool_result"}}
}

func TestMergeDownscalesLargeSessionImages(t *testing.T) {
	for _, tc := range []struct {
		format, mime  string
		width, height int
		wantW, wantH  int
	}{
		{"png", "image/png", 4096, 1024, 1568, 392},
		{"jpeg", "image/jpeg", 1000, 3000, 522, 1568},
	} {
		original := screenshot(t, tc.format, tc.width, tc.height)
		merged, data, notes := mergeImages(nil, nil, []SessionImage{sessionImage(original)}, DefaultLimits())
		if len(merged) != 1 || len(notes) != 0 {
			t.Fatalf("%s: %+v %v", tc.format, merged, notes)
		}
		got := merged[0]
		stored := data[got.ID]
		mime, w, h, ok := bundle.SniffImage(stored)
		sum := sha256.Sum256(stored)
		hash := hex.EncodeToString(sum[:])
		if !ok || mime != tc.mime || got.ContentType != tc.mime || w != tc.wantW || h != tc.wantH || got.Width != w || got.Height != h {
			t.Fatalf("%s: stored %s %dx%d, recorded %s %dx%d", tc.format, mime, w, h, got.ContentType, got.Width, got.Height)
		}
		if got.SHA256 != hash || got.ID != "att_"+hash[:32] || got.Size != int64(len(stored)) || len(stored) >= len(original) {
			t.Fatalf("%s: identity does not describe the resized bytes: %+v (%d of %d bytes)", tc.format, got, len(stored), len(original))
		}
	}
}

func TestMergeKeepsSmallAndAnimatableSessionImages(t *testing.T) {
	for _, original := range [][]byte{screenshot(t, "png", 800, 600), screenshot(t, "gif", 2500, 100)} {
		merged, data, _ := mergeImages(nil, nil, []SessionImage{sessionImage(original)}, DefaultLimits())
		if len(merged) != 1 || !bytes.Equal(data[merged[0].ID], original) {
			t.Fatalf("image changed: %+v", merged)
		}
	}
}

func TestDownscaleKeepsImagesItCouldAlter(t *testing.T) {
	large := screenshot(t, "png", 4096, 1024)
	if _, _, _, _, ok := downscale(large, "image/png", 4096, 1024); !ok {
		t.Fatal("plain PNG not resized")
	}
	// An acTL chunk right after IHDR (8-byte signature + 25-byte chunk) marks an APNG.
	actl := []byte{0, 0, 0, 8, 'a', 'c', 'T', 'L', 0, 0, 0, 2, 0, 0, 0, 0, 0, 0, 0, 0}
	apng := append(append(append([]byte{}, large[:33]...), actl...), large[33:]...)
	if _, _, _, _, ok := downscale(apng, "image/png", 4096, 1024); ok {
		t.Fatal("animated PNG flattened")
	}
	photo := screenshot(t, "jpeg", 3000, 1000)
	if _, _, _, _, ok := downscale(photo, "image/jpeg", 3000, 1000); !ok {
		t.Fatal("plain JPEG not resized")
	}
	exif := []byte{0xff, 0xe1, 0, 16, 'E', 'x', 'i', 'f', 0, 0, 'M', 'M', 0, 42, 0, 0, 0, 8}
	rotated := append(append(append([]byte{}, photo[:2]...), exif...), photo[2:]...)
	if _, _, _, _, ok := downscale(rotated, "image/jpeg", 3000, 1000); ok {
		t.Fatal("JPEG with EXIF orientation re-encoded")
	}
	// Too many pixels to decode: refused before reading any bytes.
	if _, _, _, _, ok := downscale(nil, "image/png", 6000, 5000); ok {
		t.Fatal("oversized image decoded")
	}
}

func TestMergeMatchesLargeSessionImageToDescriptionCopy(t *testing.T) {
	original := screenshot(t, "png", 3000, 200)
	sum := sha256.Sum256(original)
	description := bundle.DescriptionImage{ID: "att_0123456789abcdef0123456789abcdef", State: bundle.AttachmentStored, SourceURL: "https://github.com/user-attachments/assets/1",
		ContentType: "image/png", Width: 3000, Height: 200, SHA256: hex.EncodeToString(sum[:])}
	first, second := sessionImage(original), sessionImage(original)
	second.Origin.Session = "two"
	merged, data, _ := mergeImages([]bundle.DescriptionImage{description}, map[string][]byte{description.ID: original}, []SessionImage{first, second}, DefaultLimits())
	if len(merged) != 1 || merged[0].ID != description.ID || len(merged[0].Origins) != 2 || !bytes.Equal(data[description.ID], original) {
		t.Fatalf("session copy of a description image stored separately: %+v", merged)
	}
	// Repeats of one resized session image share its copy and origins.
	merged, data, _ = mergeImages(nil, nil, []SessionImage{first, second}, DefaultLimits())
	if len(merged) != 1 || len(merged[0].Origins) != 2 || len(data) != 1 || merged[0].Width != MaxSessionImageEdge {
		t.Fatalf("resized repeats not merged: %+v", merged)
	}
}
