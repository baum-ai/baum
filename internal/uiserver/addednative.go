package uiserver

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/baum-ai/baum/bundle"
	"github.com/baum-ai/baum/internal/addedtiles"
	"github.com/baum-ai/baum/internal/assemble"
	"github.com/baum-ai/baum/internal/locallink"
	"github.com/baum-ai/baum/internal/repos"
	"github.com/baum-ai/baum/internal/tileview"
	"github.com/baum-ai/baum/reviewv2"
	"github.com/baum-ai/baum/tiles/library"
	"github.com/baum-ai/baum/tiles/view"
)

// The native types a person can add from the session panel: the types the
// review page draws with its own components, and images. Their data is
// the agent's, written once and checked as a generated tile's; nothing
// fills them in the background.

// NativeAddTypes are the native types add_tile takes, in the order the
// tool lists them.
var NativeAddTypes = []string{reviewv2.TypeFlow, reviewv2.TypeColumns, reviewv2.TypeSequence, reviewv2.TypeTable, reviewv2.TypeCode, reviewv2.TypeImages}

// IsNativeAddType reports whether name is a native type add_tile takes.
func IsNativeAddType(name string) bool { return slices.Contains(NativeAddTypes, name) }

// The limits of an added native tile's content.
const (
	// MaxAddedImages bounds the images of one images tile.
	MaxAddedImages = 6
	// maxImageFileBytes bounds an image file read before it is downscaled.
	maxImageFileBytes = 40 << 20
	// maxCodeLines bounds a code tile's excerpt.
	maxCodeLines = 80
)

// nativeTitles are the titles a native tile gets when the agent gives none.
var nativeTitles = map[string]string{
	reviewv2.TypeFlow: "Flow", reviewv2.TypeColumns: "Comparison", reviewv2.TypeSequence: "Sequence",
	reviewv2.TypeTable: "Table", reviewv2.TypeCode: "Code", reviewv2.TypeImages: "Screenshots",
}

// addNative adds a native tile to the pull request's review page with the
// data the agent wrote, checked against the latest version as a generated
// tile of the type is (reviewv2.CheckTile), masked, and stored for that
// version. A code tile's text is read from the clone at the version's head;
// an images tile's files are read from this machine, downscaled as session
// images are, and stored for the pull request (addedtiles.PutImage).
func (a LocalAPI) addNative(ctx context.Context, c repos.Clone, number int, by string, add AddTile) (AddedTile, error) {
	if len(add.Data) == 0 || string(add.Data) == "null" {
		return AddedTile{}, fmt.Errorf("A %s tile needs its data: write it and pass it as data; nothing fills it in the background", add.Type)
	}
	shown, err := a.latestShown(ctx, c, number)
	if err != nil {
		return AddedTile{}, err
	}
	pr := addedtiles.Key(c.Repository, number)
	title := strings.TrimSpace(add.Title)
	if title == "" {
		title = nativeTitles[add.Type]
	}
	tile := reviewv2.Tile{ID: "added_check", Type: add.Type, Title: title, State: reviewv2.TileReady, Cites: []reviewv2.Anchor{}}
	switch add.Type {
	case reviewv2.TypeCode:
		data, cite, err := a.codeExcerpt(ctx, c, shown, add.Data)
		if err != nil {
			return AddedTile{}, err
		}
		tile.Data, tile.Cites = data, []reviewv2.Anchor{cite}
	case reviewv2.TypeImages:
		data, err := a.storeImages(ctx, pr, add.Data)
		if err != nil {
			return AddedTile{}, err
		}
		tile.Data = data
	default:
		diagramTitle, data, err := diagramOf(add.Type, add.Data, reviewv2.RefsOfIndex(shown.Review.Index).HasHunk)
		if err != nil {
			return AddedTile{}, err
		}
		if strings.TrimSpace(add.Title) == "" && diagramTitle != "" {
			tile.Title = diagramTitle
		}
		tile.Data = data
		reviewv2.LiftNative(&tile)
	}
	if problems := reviewv2.CheckTile(tile, reviewv2.RefsOfIndex(shown.Review.Index)); len(problems) > 0 {
		return AddedTile{}, fmt.Errorf("The data does not fit the %s tile, so nothing changed: %s", add.Type, strings.Join(problems[:min(len(problems), 12)], "; "))
	}
	native := tileview.NativeAdded{Type: tile.Type, Title: tile.Title, Caption: tile.Caption, Cites: tile.Cites, Data: tile.Data}
	encoded, err := json.Marshal(native)
	if err != nil {
		return AddedTile{}, err
	}
	masked, err := maskData(ctx, encoded)
	if err != nil {
		return AddedTile{}, err
	}
	id, err := nativeTileID(add.Type)
	if err != nil {
		return AddedTile{}, err
	}
	if err := a.roomFor(ctx, pr, id); err != nil {
		return AddedTile{}, err
	}
	section := shown.place(strings.TrimSpace(add.Section), reviewv2.HunkIDs(tile.Cites))
	if err := a.Added.Add(ctx, pr, addedtiles.Added{Tile: id, Kind: addedtiles.KindNative, Name: add.Type, Digest: addedtiles.NativeDigest, Section: section, By: by}); err != nil {
		return AddedTile{}, err
	}
	if err := a.Added.PutNative(ctx, pr, id, shown.Version, masked); err != nil {
		_ = a.Added.Remove(context.WithoutCancel(ctx), pr, id)
		return AddedTile{}, err
	}
	return AddedTile{Tile: id, State: "ready", Section: section}, nil
}

// nativeTileID is a new native added tile's ID: a person may add several
// tiles of one type.
func nativeTileID(typ string) (string, error) {
	var b [5]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return "added_" + typ + "_" + hex.EncodeToString(b[:]), nil
}

// codeExcerpt is a code tile's data from what the agent named ({hunk,
// lines}): the lines of the hunk's new side, read from the file at the
// version's head, so the excerpt is the pull request's code and never the
// agent's text.
func (a LocalAPI) codeExcerpt(ctx context.Context, c repos.Clone, shown shownVersion, raw json.RawMessage) (json.RawMessage, reviewv2.Anchor, error) {
	var code reviewv2.CodeData
	if err := strictData(raw, &code); err != nil || code.Hunk == "" || code.Text != "" {
		return nil, reviewv2.Anchor{}, errors.New(`A code tile's data is {"hunk": a hunk's id, "lines": [first, last]} (lines optional); the host reads the code`)
	}
	var hunk *bundle.Hunk
	for _, h := range library.DiffOf(shown.Document) {
		if h.ID == code.Hunk {
			hunk = &h
			break
		}
	}
	if hunk == nil {
		return nil, reviewv2.Anchor{}, fmt.Errorf("The hunk %s is not in this version's diff", code.Hunk)
	}
	if hunk.LineCount < 1 {
		return nil, reviewv2.Anchor{}, fmt.Errorf("The hunk %s has no lines on its new side", code.Hunk)
	}
	first, last := hunk.StartLine, hunk.StartLine+hunk.LineCount-1
	if code.Lines != nil {
		if len(code.Lines) != 2 || code.Lines[0] > code.Lines[1] || code.Lines[0] < first || code.Lines[1] > last {
			return nil, reviewv2.Anchor{}, fmt.Errorf("lines must be a first and last line within the hunk's new side, %d to %d", first, last)
		}
		first, last = code.Lines[0], code.Lines[1]
	}
	if last-first+1 > maxCodeLines {
		return nil, reviewv2.Anchor{}, fmt.Errorf("A code tile shows at most %d lines: name fewer with lines", maxCodeLines)
	}
	var header struct {
		Head string `json:"head"`
	}
	if json.Unmarshal(shown.Review.Review, &header) != nil || header.Head == "" {
		return nil, reviewv2.Anchor{}, errors.New("This version names no head commit to read the code from")
	}
	repo, problem, err := locallink.OpenClone(ctx, c.Path, c.Repository)
	if err != nil || problem != "" {
		return nil, reviewv2.Anchor{}, errors.New("The clone could not be read for the code")
	}
	defer func() { _ = repo.Close() }()
	content, err := repo.Blob(ctx, header.Head, hunk.Path)
	if err != nil {
		return nil, reviewv2.Anchor{}, errors.New("The file could not be read at this version's head; fetch the pull request's head and try again")
	}
	lines := strings.Split(strings.ReplaceAll(string(content), "\r\n", "\n"), "\n")
	if last > len(lines) {
		return nil, reviewv2.Anchor{}, errors.New("The lines are past the end of the file at this version's head")
	}
	code.Lines = []int{first, last}
	code.Text = strings.Join(lines[first-1:last], "\n")
	data, err := json.Marshal(code)
	return data, reviewv2.HunkAnchor(code.Hunk, code.Lines), err
}

// storeImages stores the images an images tile names ({items: [{path,
// caption}]}, each an image file on this machine) and is the tile's data:
// each image by its stored id, with its caption, type and size. An image
// larger than a stored session image is downscaled as one is.
func (a LocalAPI) storeImages(ctx context.Context, pr string, raw json.RawMessage) (json.RawMessage, error) {
	var in struct {
		Items []struct {
			Path    string `json:"path"`
			Caption string `json:"caption"`
		} `json:"items"`
	}
	if err := strictData(raw, &in); err != nil || len(in.Items) == 0 {
		return nil, errors.New(`An images tile's data is {"items": [{"path": an image file's absolute path on this computer, "caption": optional}]}`)
	}
	if len(in.Items) > MaxAddedImages {
		return nil, fmt.Errorf("An images tile holds at most %d images", MaxAddedImages)
	}
	out := reviewv2.ImagesData{Items: []reviewv2.ImageItem{}}
	for _, item := range in.Items {
		image, err := readImage(item.Path)
		if err != nil {
			return nil, err
		}
		id, err := a.putImage(ctx, pr, image)
		if err != nil {
			return nil, err
		}
		out.Items = append(out.Items, reviewv2.ImageItem{Attachment: id, Caption: reviewv2.CleanInline(item.Caption), ContentType: image.contentType, Width: image.width, Height: image.height})
	}
	return json.Marshal(out)
}

// putImage stores an image read by readImage for the pull request's added
// tiles, under an id that is a digest of its bytes, and returns that id.
func (a LocalAPI) putImage(ctx context.Context, pr string, image readImageResult) (string, error) {
	id := imageID(image.bytes)
	if err := a.Added.PutImage(ctx, pr, id, addedtiles.Image{ContentType: image.contentType, Bytes: image.bytes}); err != nil {
		return "", err
	}
	return id, nil
}

// imageID is the id a stored image's bytes are kept under: an attachment
// id that is a digest of them.
func imageID(bytes []byte) string {
	sum := sha256.Sum256(bytes)
	return "att_" + hex.EncodeToString(sum[:16])
}

// readImageResult is an image file as it is stored.
type readImageResult struct {
	bytes         []byte
	contentType   string
	width, height int
}

// readImage reads an image file: sniffed (PNG, JPEG, GIF or WebP), and
// downscaled when it is larger than a stored session image.
func readImage(path string) (readImageResult, error) {
	path = strings.TrimSpace(path)
	if !filepath.IsAbs(path) {
		return readImageResult{}, fmt.Errorf("%q is not an absolute path: give the image file's full path on this computer", path)
	}
	f, err := os.Open(path)
	if err != nil {
		return readImageResult{}, fmt.Errorf("The image %s could not be read", filepath.Base(path))
	}
	defer func() { _ = f.Close() }()
	content, err := io.ReadAll(io.LimitReader(f, maxImageFileBytes+1))
	if err != nil {
		return readImageResult{}, fmt.Errorf("The image %s could not be read", filepath.Base(path))
	}
	if len(content) > maxImageFileBytes {
		return readImageResult{}, fmt.Errorf("The image %s is over %d MiB", filepath.Base(path), maxImageFileBytes>>20)
	}
	mime, width, height, ok := bundle.SniffImage(content)
	if !ok {
		return readImageResult{}, fmt.Errorf("%s is not a PNG, JPEG, GIF or WebP image", filepath.Base(path))
	}
	if resized, rmime, rw, rh, ok := assemble.Downscale(content, mime, width, height); ok {
		content, mime, width, height = resized, rmime, rw, rh
	}
	if len(content) > addedtiles.MaxImageBytes {
		return readImageResult{}, fmt.Errorf("The image %s is over %d MiB after downscaling", filepath.Base(path), addedtiles.MaxImageBytes>>20)
	}
	return readImageResult{bytes: content, contentType: mime, width: width, height: height}, nil
}

// diagramOf is a flow, columns, sequence or table tile's data from what
// the agent wrote: a diagram as a review's tile agent writes one
// (view.DiagramSchema), checked as one is (view.CheckDiagram) and in its
// stored shape. A text over its limit or a source that names no hunk of
// the version is refused, not cut or dropped: the agent fixes it.
func diagramOf(typ string, raw json.RawMessage, hasHunk func(string) bool) (string, json.RawMessage, error) {
	var d view.ReplyDiagram
	if err := strictData(raw, &d); err != nil {
		return "", nil, fmt.Errorf("The data is not a %s diagram as tile_type's fill_schema gives it: %v", typ, err)
	}
	if d.Type == "" {
		d.Type = typ
	}
	if d.Type != typ {
		return "", nil, fmt.Errorf("The data is a %s diagram, not %s", d.Type, typ)
	}
	if d.Source == nil {
		d.Source = []string{}
	}
	title, data, report, problem := view.CheckDiagram(d, hasHunk)
	if problem != "" {
		return "", nil, fmt.Errorf("The %s tile cannot be shown, so nothing changed: %s", typ, problem)
	}
	counts := report.Counts()
	var problems []string
	if n := counts[view.FixCut]; n > 0 {
		problems = append(problems, fmt.Sprintf("%d of its texts are over their limits (see fill_schema's maxLength)", n))
	}
	if n := counts[view.FixAnchor]; n > 0 {
		problems = append(problems, fmt.Sprintf("%d of its sources name no hunk of this version", n))
	}
	if n := counts[view.FixNativeItem]; n > 0 {
		problems = append(problems, fmt.Sprintf("%d of its rows are not as long as its columns, or past the limit", n))
	}
	if len(problems) > 0 {
		return "", nil, fmt.Errorf("The data does not fit the %s tile, so nothing changed: %s", typ, strings.Join(problems, "; "))
	}
	return title, data, nil
}

// strictData decodes data the agent wrote, refusing fields its shape does
// not have.
func strictData(raw json.RawMessage, v any) error {
	d := json.NewDecoder(strings.NewReader(string(raw)))
	d.DisallowUnknownFields()
	return d.Decode(v)
}
