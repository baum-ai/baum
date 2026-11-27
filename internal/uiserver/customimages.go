package uiserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/baum-ai/baum/internal/addedtiles"
)

// The images of a tile made in chat. Where its data's schema has a string,
// the agent may write {"image_file": "/abs/path.png"}: the host reads that
// file as add_tile's images type does (readImage: an absolute path, at most
// maxImageFileBytes, sniffed as PNG, JPEG, GIF or WebP, downscaled as a
// session image), stores it with the pull request's added tiles and puts a
// reference to it (addedtiles.ImageRef) in the object's place. See
// addedtiles' customimages.go for how a frame gets it.

// imageFileKey is the one key of the object that names an image file in a
// custom tile's data.
const imageFileKey = "image_file"

// The limits of the images of one custom tile's data: as many distinct
// images as an images tile holds, and a total counting every time the data
// names one, since each is handed to its frame as a data: URL.
const (
	maxCustomImages     = MaxAddedImages
	maxCustomImageBytes = 16 << 20
)

// customImages stores the image files a custom tile's data names and
// returns the data with a reference in each one's place; data that names
// none is returned as it is. An error says what is wrong with an image, in
// words for the agent.
func (a LocalAPI) customImages(ctx context.Context, pr string, data json.RawMessage) (json.RawMessage, error) {
	if len(data) == 0 || string(data) == "null" {
		return data, nil
	}
	type stored struct {
		ref  string
		size int
		err  error
	}
	byPath := map[string]stored{}
	ids := map[string]bool{}
	// Every occurrence counts: each becomes a whole data: URL in the frame.
	total := 0
	var first error
	out, changed, err := addedtiles.MapValues(data, func(v any) (any, bool) {
		object, ok := v.(map[string]any)
		if !ok || len(object) != 1 {
			return v, false
		}
		path, ok := object[imageFileKey].(string)
		if !ok {
			return v, false
		}
		s, seen := byPath[path]
		if !seen {
			s = func() stored {
				if a.Added == nil {
					return stored{err: errors.New("images can't be stored in this baum")}
				}
				image, err := readImage(path)
				if err != nil {
					return stored{err: err}
				}
				id, err := a.putImageWithin(ctx, pr, image, ids)
				if err != nil {
					return stored{err: err}
				}
				return stored{ref: addedtiles.ImageRef(id), size: len(image.bytes)}
			}()
			byPath[path] = s
		}
		if s.err == nil {
			if total += s.size; total > maxCustomImageBytes {
				s.err = fmt.Errorf("A tile's images are at most %d MiB together after downscaling, counting each time the data names one: name fewer or smaller images", maxCustomImageBytes>>20)
			}
		}
		if s.err != nil && first == nil {
			first = s.err
		}
		return s.ref, true
	})
	switch {
	case err != nil:
		return nil, errors.New("the data is not JSON")
	case first != nil:
		return nil, first
	case !changed:
		return data, nil
	}
	return out, nil
}

// putImageWithin stores an image of a custom tile's data unless it would
// be one more distinct image than a tile holds.
func (a LocalAPI) putImageWithin(ctx context.Context, pr string, image readImageResult, ids map[string]bool) (string, error) {
	id := imageID(image.bytes)
	if !ids[id] {
		if len(ids) == maxCustomImages {
			return "", fmt.Errorf("A tile's data names at most %d images", maxCustomImages)
		}
		ids[id] = true
	}
	return a.putImage(ctx, pr, image)
}
