package addedtiles

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"regexp"
	"strings"
	"time"

	"github.com/baum-ai/baum/internal/appdb"
	"github.com/baum-ai/baum/internal/tileview"
)

// A native added tile is a tile of a type the review page draws with its
// own components (flow, columns, sequence, table, code) or an images tile.
// The session panel's agent writes its data once, for the latest version;
// nothing fills it again, so every later version shows that data, marked as
// from the version it was written for, and the tile route checks its
// citations against each version it serves. An earlier version than the one
// it was written for does not show it.
//
// The images of an images tile are kept per pull request in added_images,
// while the pull request has an added tile, and served by the attachment
// route like the attachments of a saved version.

// NativeDigest is the digest of every native added tile: it has no
// definition.
const NativeDigest = "native"

// imagesSchema keeps the images of a pull request's added images tiles.
const imagesSchema = `CREATE TABLE IF NOT EXISTS added_images(
  pr TEXT NOT NULL,
  id TEXT NOT NULL,
  content_type TEXT NOT NULL,
  bytes BLOB NOT NULL,
  at TEXT NOT NULL,
  PRIMARY KEY(pr, id))`

// MaxImageBytes bounds one stored image, as a saved version's attachment.
const MaxImageBytes = 10 << 20

// imageID is an added image's id: an attachment id, so the page and the
// attachment route treat it as one.
var imageID = regexp.MustCompile(`^att_[0-9a-f]{32}$`)

// ValidImageID reports whether id is an added image's id.
func ValidImageID(id string) bool { return imageID.MatchString(id) }

// Image is a stored image: its content type and bytes.
type Image struct {
	ContentType string
	Bytes       []byte
}

// PutNative stores the tile the agent wrote for a native added tile, for
// the version it was written for (tileview.NativeAdded, encoded). The
// caller has checked and masked it.
func (s *Service) PutNative(ctx context.Context, pr, tile string, version int, native json.RawMessage) error {
	if !ValidKey(pr) || version < 1 || !tileIDPattern(tile) {
		return errors.New("invalid fill")
	}
	if len(native) > MaxFillBytes || !json.Valid(native) {
		return fmt.Errorf("a tile's data is at most %d KiB of JSON", MaxFillBytes>>10)
	}
	now := s.now().UTC().Format(time.RFC3339Nano)
	err := s.write(ctx, func(conn *sql.Conn) error {
		_, err := conn.ExecContext(ctx, `INSERT INTO tile_fills(pr, tile, version, digest, data, at, origin) VALUES (?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(pr, tile, version) DO UPDATE SET digest = excluded.digest, data = excluded.data, at = excluded.at, origin = excluded.origin`, pr, tile, version, NativeDigest, []byte(native), now, OriginChat)
		return err
	})
	if err != nil {
		return err
	}
	s.changed(pr)
	return nil
}

// PutImage stores an image for a pull request's added tiles under id. The
// caller has sniffed its type. An image is kept while the pull request has
// an added tile, removed ones included (their Undo shows it again).
func (s *Service) PutImage(ctx context.Context, pr, id string, image Image) error {
	if !ValidKey(pr) || !ValidImageID(id) || !strings.HasPrefix(image.ContentType, "image/") || len(image.Bytes) == 0 {
		return errors.New("invalid image")
	}
	if len(image.Bytes) > MaxImageBytes {
		return fmt.Errorf("an image is at most %d MiB", MaxImageBytes>>20)
	}
	now := s.now().UTC().Format(time.RFC3339Nano)
	// Not through write: its prune would drop an image no tile names yet.
	db, release, err := appdb.Acquire(s.Path, false)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrUnsaved, err)
	}
	defer release()
	err = appdb.Transact(ctx, db, func(conn *sql.Conn) error {
		if err := ensureTables(ctx, conn); err != nil {
			return err
		}
		_, err := conn.ExecContext(ctx, `INSERT INTO added_images(pr, id, content_type, bytes, at) VALUES (?, ?, ?, ?, ?)
ON CONFLICT(pr, id) DO UPDATE SET at = excluded.at`, pr, id, image.ContentType, image.Bytes, now)
		return err
	})
	if err != nil {
		return fmt.Errorf("%w: %v", ErrUnsaved, err)
	}
	return nil
}

// Image reads an image stored for a pull request's added tiles; ErrNotFound
// when there is none.
func (s *Service) Image(ctx context.Context, pr, id string) (Image, error) {
	if !ValidKey(pr) || !ValidImageID(id) {
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
	err = db.QueryRowContext(ctx, `SELECT content_type, bytes FROM added_images WHERE pr = ? AND id = ?`, pr, id).Scan(&image.ContentType, &image.Bytes)
	if errors.Is(err, sql.ErrNoRows) || err != nil && strings.Contains(err.Error(), "no such table") {
		return Image{}, ErrNotFound
	}
	return image, err
}

// nativeFor is what a version shows of a native added tile: the data
// written for it, or the newest written for an earlier one, marked as from
// that version. A version before any has nothing.
func nativeFor(a Added, fills []Fill, v Version) (tileview.AddedTile, bool) {
	for _, f := range fills {
		// Fills are newest version first.
		if f.Tile != a.Tile || f.Digest != NativeDigest || f.Version > v.Number {
			continue
		}
		var native tileview.NativeAdded
		if json.Unmarshal(f.Data, &native) != nil || native.Type != a.Name {
			return tileview.AddedTile{}, false
		}
		native.ID = a.Tile
		note := tileview.AddedTile{Section: a.Section, Native: &native}
		if f.Version < v.Number {
			note.From = f.Version
		}
		return note, true
	}
	return tileview.AddedTile{}, false
}
