package addedtiles

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io/fs"
	"strings"

	"github.com/baum-ai/baum/internal/appdb"
)

// A custom tile made in chat can show images the host stored, as an images
// tile does: the agent names an image file where the data's schema has a
// string ({"image_file": "/abs/path.png"}), the host reads, sniffs,
// downscales and stores it in added_images, and the data keeps a reference
// (ImageRef) in that string's place. A frame accepts images only as data:
// URLs, so whatever hands the data to a frame puts the stored image there
// in the reference's place: the review page reads it from the attachment
// route, and the render check's job carries it (InlineImages). The data's
// own size stays small, and the agent never writes image bytes.

// ImageRefPrefix starts a reference to a stored image in a custom tile's
// data.
const ImageRefPrefix = "baum-image:"

// ImageRef is the reference a custom tile's data holds for the stored image
// id.
func ImageRef(id string) string { return ImageRefPrefix + id }

// ImageRefID is the stored image id a reference names.
func ImageRefID(s string) (string, bool) {
	id, ok := strings.CutPrefix(s, ImageRefPrefix)
	if !ok || !ValidImageID(id) {
		return "", false
	}
	return id, true
}

// ImageByID reads a stored image by its id alone, whichever pull request
// stored it: an id is a digest of the image's bytes, so it names the same
// image wherever it was stored. ErrNotFound when there is none.
func (s *Service) ImageByID(ctx context.Context, id string) (Image, error) {
	if !ValidImageID(id) {
		return Image{}, ErrNotFound
	}
	db, release, err := appdb.Acquire(s.Path, true)
	if errors.Is(err, fs.ErrNotExist) {
		return Image{}, ErrNotFound
	}
	if err != nil {
		return Image{}, err
	}
	defer release()
	var image Image
	err = db.QueryRowContext(ctx, `SELECT content_type, bytes FROM added_images WHERE id = ? LIMIT 1`, id).Scan(&image.ContentType, &image.Bytes)
	if errors.Is(err, sql.ErrNoRows) || err != nil && strings.Contains(err.Error(), "no such table") {
		return Image{}, ErrNotFound
	}
	return image, err
}

// MaxInlined bounds the data: URLs InlineImages puts in one tile's data,
// counting every reference: data can repeat a reference, or name one
// without the check having stored it, far past the images' own limits.
const MaxInlined = 24 << 20

// InlineImages is data with each reference to a stored image replaced by
// that image as a data: URL, for a frame to draw; a reference to an image
// that is not stored, or past MaxInlined, becomes an empty string. Data
// without references, or that is not JSON, is returned as it is.
func (s *Service) InlineImages(ctx context.Context, data json.RawMessage) json.RawMessage {
	if !bytes.Contains(data, []byte(ImageRefPrefix)) {
		return data
	}
	urls := map[string]string{}
	total := 0
	out, changed, err := MapStrings(data, func(v string) (string, bool) {
		id, ok := ImageRefID(v)
		if !ok {
			return v, false
		}
		url, seen := urls[id]
		if !seen {
			if image, err := s.ImageByID(ctx, id); err == nil && strings.HasPrefix(image.ContentType, "image/") {
				url = "data:" + image.ContentType + ";base64," + base64.StdEncoding.EncodeToString(image.Bytes)
			}
			urls[id] = url
		}
		if total += len(url); total > MaxInlined {
			return "", true
		}
		return url, true
	})
	if err != nil || !changed {
		return data
	}
	return out
}

// MapStrings is data with each string value (not object keys) passed
// through f, which reports whether it changed it; changed reports whether
// any did. Numbers keep their text.
func MapStrings(data json.RawMessage, f func(string) (string, bool)) (json.RawMessage, bool, error) {
	return MapValues(data, func(v any) (any, bool) {
		if s, ok := v.(string); ok {
			return f(s)
		}
		return v, false
	})
}

// MapValues is data with each value passed through f before its children,
// which reports whether it replaced it (a replacement's children are not
// visited); changed reports whether any was. Numbers keep their text.
func MapValues(data json.RawMessage, f func(any) (any, bool)) (json.RawMessage, bool, error) {
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	var v any
	if err := d.Decode(&v); err != nil {
		return nil, false, err
	}
	changed := false
	var walk func(any) any
	walk = func(v any) any {
		if next, ok := f(v); ok {
			changed = true
			return next
		}
		switch t := v.(type) {
		case map[string]any:
			for k, child := range t {
				t[k] = walk(child)
			}
		case []any:
			for i, child := range t {
				t[i] = walk(child)
			}
		}
		return v
	}
	v = walk(v)
	if !changed {
		return data, false, nil
	}
	out, err := json.Marshal(v)
	if err != nil {
		return nil, false, err
	}
	return out, true, nil
}
