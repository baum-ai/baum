package bundle

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const (
	uploadBefore      = "0b6c3a39-0f3f-4a55-9e39-2d1f6b0a1001"
	uploadAfter       = "0b6c3a39-0f3f-4a55-9e39-2d1f6b0a1002"
	signedSourceQuery = "jwt=eyJsigned-for-five-minutes"
)

func markdownAsset(id string) string { return "https://github.com/user-attachments/assets/" + id }

func signedSource(id string) string {
	return "https://private-user-images.githubusercontent.com/1234/400000001-" + id + ".png?" + signedSourceQuery
}

func pngOfSize(t *testing.T, width, height int) []byte {
	t.Helper()
	var out bytes.Buffer
	if err := png.Encode(&out, image.NewRGBA(image.Rect(0, 0, width, height))); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func sha256Sum(content []byte) []byte {
	sum := sha256.Sum256(content)
	return sum[:]
}

// removeTree makes an unpacked, read-only tree removable.
func removeTree(dir string) {
	_ = filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
		if err == nil && entry.IsDir() {
			_ = os.Chmod(path, 0700)
		}
		return nil
	})
	_ = os.RemoveAll(dir)
}

// attachmentArchive builds an attachments.tar holding content per path, and
// the bundle entries listing them.
func attachmentArchive(t *testing.T, files map[string][]byte) ([]byte, []Attachment) {
	t.Helper()
	var archive bytes.Buffer
	tw := tar.NewWriter(&archive)
	var listed []Attachment
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		content := files[name]
		if err := tw.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0400, Size: int64(len(content))}); err != nil {
			t.Fatal(err)
		}
		_, _ = tw.Write(content)
		id := strings.TrimSuffix(strings.TrimPrefix(name, "attachments/"), filepath.Ext(name))
		listed = append(listed, Attachment{ID: id, Path: name, ContentType: "image/png", SHA256: hex.EncodeToString(sha256Sum(content))})
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return archive.Bytes(), listed
}

var (
	testAttachmentA = "attachments/" + AttachmentID(markdownAsset(uploadBefore)) + ".png"
	testAttachmentB = "attachments/" + AttachmentID(markdownAsset(uploadAfter)) + ".gif"
)

func TestSniffImageAcceptsOnlyRasterFormats(t *testing.T) {
	vp8x := append([]byte("RIFF\x00\x00\x00\x00WEBPVP8X\x0a\x00\x00\x00\x00\x00\x00\x00"), 0x1f, 0x00, 0x00, 0x09, 0x00, 0x00)
	vp8l := append([]byte("RIFF\x00\x00\x00\x00WEBPVP8L\x00\x00\x00\x00\x2f"), 0x09, 0x40, 0x02, 0x00, 0, 0, 0, 0, 0)
	for name, tc := range map[string]struct {
		content       []byte
		contentType   string
		width, height int
	}{
		"png":  {pngOfSize(t, 5, 3), "image/png", 5, 3},
		"vp8x": {vp8x, "image/webp", 32, 10},
		"vp8l": {vp8l, "image/webp", 10, 10},
		"svg":  {[]byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`), "", 0, 0},
	} {
		contentType, width, height, _ := SniffImage(tc.content)
		if contentType != tc.contentType || width != tc.width || height != tc.height {
			t.Errorf("%s: sniffed %q %dx%d, want %q %dx%d", name, contentType, width, height, tc.contentType, tc.width, tc.height)
		}
	}
}

// A signed link pasted into the markdown is stored, and identified, without
// its token.
func TestMarkdownUploadsDropQueryAndFragment(t *testing.T) {
	unsigned := "https://private-user-images.githubusercontent.com/1234/400000001-" + uploadBefore + ".png"
	uploads := MarkdownUploads("![shot](" + signedSource(uploadBefore) + "#frag)")
	images := RenderedImages(`<img alt="shot" src="`+signedSource(uploadBefore)+`">`, uploads)
	if len(images) != 1 || images[0].SourceURL != unsigned || images[0].ID != AttachmentID(unsigned) {
		t.Fatalf("images = %+v, want source %s", images, unsigned)
	}
}

// An external image is recorded, and identified, without a presigned token
// in its source or canonical source.
func TestExternalImagesDropQueryAndFragment(t *testing.T) {
	images := RenderedImages(`<img src="https://camo.githubusercontent.com/x" data-canonical-src="https://bucket.example.com/shot.png?X-Amz-Signature=secret#top">`+
		`<img src="https://cdn.example.com/plain.png?token=secret">`, nil)
	want := []string{"https://bucket.example.com/shot.png", "https://cdn.example.com/plain.png"}
	if len(images) != len(want) {
		t.Fatalf("images = %+v, want %v", images, want)
	}
	for i, image := range images {
		if image.SourceURL != want[i] || image.ID != AttachmentID(want[i]) || image.State != AttachmentExternal {
			t.Fatalf("image %d = %+v, want external source %s", i, image, want[i])
		}
	}
}

func TestHeadingAboveIgnoresTheImageLineAndUnknownSources(t *testing.T) {
	source := markdownAsset(uploadBefore)
	for body, want := range map[string]string{
		"## Title\n![x](" + source + ")":                "Title",
		"![x](" + source + ")":                          "",
		"## Title\n## ![x](" + source + ")":             "Title",
		"#NoSpace\n![x](" + source + ")":                "",
		"   ### Indented ###  \r\n![x](" + source + ")": "Indented",
		"## Title\n": "",
	} {
		if got := headingAbove(body, source); got != want {
			t.Errorf("headingAbove(%q) = %q, want %q", body, got, want)
		}
	}
}

func TestUnpackAttachmentsRejectsAnythingButTheListedImages(t *testing.T) {
	good := map[string][]byte{testAttachmentA: []byte("first")}
	archive, listed := attachmentArchive(t, good)
	write := func(t *testing.T, body []byte) string {
		path := filepath.Join(t.TempDir(), "attachments.tar")
		if err := os.WriteFile(path, body, 0600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	dir := t.TempDir()
	t.Cleanup(func() { removeTree(dir) })
	if err := UnpackAttachments(context.Background(), write(t, archive), dir, listed, 1024); err != nil {
		t.Fatalf("listed image refused: %v", err)
	}
	if info, err := os.Stat(filepath.Join(dir, "attachments")); err != nil || info.Mode().Perm() != 0500 {
		t.Fatalf("attachments directory is not read-only: %v %v", info, err)
	}
	if err := UnpackAttachments(context.Background(), "", t.TempDir(), nil, 1024); err != nil {
		t.Fatalf("a run without images failed: %v", err)
	}
	// entry archives one header per content, in order.
	entry := func(header tar.Header, contents ...string) []byte {
		var out bytes.Buffer
		tw := tar.NewWriter(&out)
		for _, content := range contents {
			header.Size = int64(len(content))
			_ = tw.WriteHeader(&header)
			_, _ = tw.Write([]byte(content))
		}
		_ = tw.Close()
		return out.Bytes()
	}
	traversal := []Attachment{{Path: "attachments/../escape.png", SHA256: listed[0].SHA256}}
	cases := map[string]struct {
		archive []byte
		listed  []Attachment
		limit   int64
	}{
		"unlisted":      {archive: entry(tar.Header{Name: testAttachmentB, Typeflag: tar.TypeReg}, "first"), listed: listed, limit: 1024},
		"symlink":       {archive: entry(tar.Header{Name: testAttachmentA, Typeflag: tar.TypeSymlink, Linkname: "/etc/passwd"}, ""), listed: listed, limit: 1024},
		"traversal":     {archive: entry(tar.Header{Name: traversal[0].Path, Typeflag: tar.TypeReg}, "first"), listed: traversal, limit: 1024},
		"altered":       {archive: entry(tar.Header{Name: testAttachmentA, Typeflag: tar.TypeReg}, "other"), listed: listed, limit: 1024},
		"oversized":     {archive: archive, listed: listed, limit: 2},
		"missing":       {archive: entry(tar.Header{}), listed: listed, limit: 1024},
		"duplicate":     {archive: entry(tar.Header{Name: testAttachmentA, Typeflag: tar.TypeReg}, "first", "first"), listed: listed, limit: 1024},
		"no archive":    {listed: listed, limit: 1024},
		"corrupt":       {archive: []byte("not a tar archive"), listed: listed, limit: 1024},
		"listed as dir": {archive: archive, listed: []Attachment{{Path: "attachments/", SHA256: listed[0].SHA256}}, limit: 1024},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			path := ""
			if tc.archive != nil {
				path = write(t, tc.archive)
			}
			if err := UnpackAttachments(context.Background(), path, t.TempDir(), tc.listed, tc.limit); err == nil {
				t.Fatal("accepted an archive that does not hold exactly the listed images")
			}
		})
	}
}
