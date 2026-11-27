package uiserver

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/baum-ai/baum/internal/repos"
	"github.com/baum-ai/baum/internal/tilelibrary"
	"github.com/baum-ai/baum/internal/tileview"
	"github.com/baum-ai/baum/tiles"
)

// The Tile library page's routes (Settings › Tile library), over the
// person's library (LocalAPI.TileTypes) and the repositories' pins
// (repos.Settings.Tiles). They list and read the saved types without their
// HTML (the page draws a type only through its preview route, in a frame),
// rename, restore a version, delete with Undo, pin a type to repositories,
// export a type as a file and import one. Each write is the person's click on the page, as the
// repository page's controls are; nothing here is reachable from a chat.

// tileTypeSummary is a saved type as the library's list shows it.
type tileTypeSummary struct {
	Name    string       `json:"name"`
	Title   string       `json:"title"`
	UseWhen string       `json:"use_when"`
	Glance  string       `json:"glance"`
	Origin  string       `json:"origin"`
	Created string       `json:"created"`
	Updated string       `json:"updated"`
	Sizes   []tiles.Size `json:"sizes"`
	Kit     int          `json:"kit"`
	Digest  string       `json:"digest"`
	// UsedAt is when a fill stored in a review last used the type, empty
	// when none has; UsedIn is in how many repositories.
	UsedAt  string   `json:"used_at,omitempty"`
	UsedIn  int      `json:"used_in"`
	Samples []string `json:"samples"`
	// Versions counts the type's versions.
	Versions int `json:"versions"`
	// Pinned are the picked repositories that pin the type, by name.
	Pinned []string `json:"pinned"`
	// Checked is false while an imported version waits for its render check
	// ("Not checked yet").
	Checked bool `json:"checked"`
}

// tileTypeDetail is a saved type as its pane shows it: the summary, the
// texts that instruct a fill and the review, its paths and its versions.
type tileTypeDetail struct {
	tileTypeSummary
	Fill         string                `json:"fill"`
	ReviewChecks string                `json:"review_checks"`
	Paths        []string              `json:"paths"`
	AppliesWith  []string              `json:"applies_with"`
	VersionList  []tilelibrary.Version `json:"version_list"`
}

func summaryOf(t tilelibrary.Type, pins map[string][]string) tileTypeSummary {
	def := t.Entry.Definition
	samples := make([]string, len(t.Entry.Samples))
	for i, s := range t.Entry.Samples {
		samples[i] = s.Name
	}
	pinned := pins[t.Name]
	if pinned == nil {
		pinned = []string{}
	}
	return tileTypeSummary{Name: t.Name, Title: def.Title, UseWhen: def.UseWhen, Glance: t.Entry.Glance, Origin: t.Origin,
		Created: t.Created, Updated: t.Updated, Sizes: def.Sizes, Kit: def.Kit, Digest: def.Digest, UsedAt: t.UsedAt,
		UsedIn: len(t.UsedIn), Samples: samples, Versions: len(t.Versions), Pinned: pinned, Checked: !t.Unchecked}
}

func detailOf(t tilelibrary.Type, pins map[string][]string) tileTypeDetail {
	paths, appliesWith := t.Paths, t.Entry.AppliesWith
	if paths == nil {
		paths = []string{}
	}
	if appliesWith == nil {
		appliesWith = []string{}
	}
	versions := t.Versions
	if versions == nil {
		versions = []tilelibrary.Version{}
	}
	return tileTypeDetail{tileTypeSummary: summaryOf(t, pins), Fill: t.Entry.Definition.Fill, ReviewChecks: t.Entry.ReviewChecks,
		Paths: paths, AppliesWith: appliesWith, VersionList: versions}
}

// tileTypePins reads which picked repositories pin each type.
func (a LocalAPI) tileTypePins(r *http.Request) (map[string][]string, error) {
	pins := map[string][]string{}
	if a.Registry == nil {
		return pins, nil
	}
	list, err := a.Registry.Repositories(r.Context())
	if err != nil {
		return nil, err
	}
	for _, repository := range list {
		for _, name := range repository.Settings.PinnedTileTypes() {
			pins[name] = append(pins[name], repository.Repository)
		}
	}
	for _, repositories := range pins {
		slices.Sort(repositories)
	}
	return pins, nil
}

// tileLibraryRoutes registers the library page's routes.
func (a LocalAPI) tileLibraryRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/tile-types", a.listTileTypes)
	mux.HandleFunc("GET /api/tile-types/{name}", a.withTileType(a.readTileType))
	mux.HandleFunc("PUT /api/tile-types/{name}/title", a.withTileType(a.renameTileType))
	mux.HandleFunc("POST /api/tile-types/{name}/restore", a.withTileType(a.restoreTileType))
	mux.HandleFunc("DELETE /api/tile-types/{name}", a.withTileType(a.deleteTileType))
	mux.HandleFunc("POST /api/tile-types/{name}/undelete", a.withTileType(a.undeleteTileType))
	mux.HandleFunc("PUT /api/tile-types/{name}/pins", a.withTileType(a.pinTileType))
	mux.HandleFunc("GET /api/tile-types/{name}/export", a.withTileType(a.exportTileType))
	mux.HandleFunc("POST /api/tile-type-files/check", a.checkTileTypeFile)
	mux.HandleFunc("POST /api/tile-type-files", a.importTileTypeFile)
}

// exportTileType serves GET /api/tile-types/{name}/export: the type's
// current version as a tile type file (tilelibrary.Export), to save as
// <name>.baumtile.json. It holds the type's synthetic samples and no review
// content.
func (a LocalAPI) exportTileType(w http.ResponseWriter, r *http.Request, name string) {
	t, err := a.TileTypes.Get(r.Context(), name)
	if err != nil {
		tileTypeError(w, err)
		return
	}
	raw, err := tilelibrary.Export(t, a.Version, time.Now())
	if err != nil {
		writeTileTypeRefusal(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", `attachment; filename="`+tilelibrary.FileName(name)+`"`)
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(raw)
}

// readTileTypeFile reads an import's body, {"file": "<the file's text>"}:
// the file is text, carried in JSON as every write is.
func (a LocalAPI) readTileTypeFile(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	if a.TileTypes == nil {
		http.NotFound(w, r)
		return nil, false
	}
	// A file of MaxFileBytes may grow to six times that escaped in JSON.
	r.Body = http.MaxBytesReader(w, r.Body, 6*tilelibrary.MaxFileBytes+1024)
	var body struct {
		File string `json:"file"`
	}
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(&body); err != nil || d.Decode(new(any)) != io.EOF {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeTileTypeRefusal(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("The file is larger than %d MiB", tilelibrary.MaxFileBytes>>20))
			return nil, false
		}
		http.Error(w, "invalid JSON request", http.StatusBadRequest)
		return nil, false
	}
	return []byte(body.File), true
}

// fileRefusal answers an import's error: a refused file 400 with its reason.
func fileRefusal(w http.ResponseWriter, err error) {
	var refused *tilelibrary.FileError
	if errors.As(err, &refused) {
		writeTileTypeRefusal(w, http.StatusBadRequest, err.Error())
		return
	}
	tileTypeError(w, err)
}

// checkTileTypeFile serves POST /api/tile-type-files/check {"file"}: every
// check an import makes (tilelibrary.Inspect), without saving, and what the
// import dialog shows before the person imports: the name it would get (and
// the file's own when that is taken), the title, use-when line, the fill
// instructions the review agent would be given, glance, review checks and
// paths, and the first sample drawn as the preview route draws a saved type.
// Once the file has passed the checks, its definition's frame document is
// registered so the dialog's preview loads: the person picked the file, and
// the app already holds its HTML.
func (a LocalAPI) checkTileTypeFile(w http.ResponseWriter, r *http.Request) {
	raw, ok := a.readTileTypeFile(w, r)
	if !ok {
		return
	}
	candidate, err := a.TileTypes.Inspect(r.Context(), raw)
	if err != nil {
		fileRefusal(w, err)
		return
	}
	entry := candidate.Entry
	if _, err := tileview.FrameRegistry().Add(entry.Definition); err != nil {
		writeTileTypeRefusal(w, http.StatusUnprocessableEntity, "the tile's frame document could not be made")
		return
	}
	paths := candidate.Change.Paths
	if paths == nil {
		paths = []string{}
	}
	body := previewBody(entry, entry.Samples[0])
	body["name"], body["renamed_from"], body["fill"] = candidate.Name, candidate.Renamed, entry.Definition.Fill
	body["glance"], body["review_checks"], body["paths"] = entry.Glance, entry.ReviewChecks, paths
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, body)
}

// importTileTypeFile serves POST /api/tile-type-files {"file"}: the file is
// checked again and saved as a new type (tilelibrary.Store.Import), renamed
// when its name is taken, marked imported, and "Not checked yet" unless a
// render check ran on its samples (LocalAPI.TileImportCheck). It answers
// {name, renamed_from, checked}.
func (a LocalAPI) importTileTypeFile(w http.ResponseWriter, r *http.Request) {
	raw, ok := a.readTileTypeFile(w, r)
	if !ok {
		return
	}
	imported, err := a.TileTypes.Import(r.Context(), raw, a.TileImportCheck)
	if err != nil {
		fileRefusal(w, err)
		return
	}
	writeJSON(w, imported)
}

// listTileTypes serves GET /api/tile-types: {"types": [...]}, the saved
// types by name, each a tileTypeSummary. A daemon without a library has an
// empty one.
func (a LocalAPI) listTileTypes(w http.ResponseWriter, r *http.Request) {
	out := []tileTypeSummary{}
	if a.TileTypes != nil {
		list, err := a.TileTypes.List(r.Context(), false)
		if err != nil {
			tileTypeError(w, err)
			return
		}
		pins, err := a.tileTypePins(r)
		if err != nil {
			apiError(w, err)
			return
		}
		for _, t := range list {
			out = append(out, summaryOf(t, pins))
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, map[string]any{"types": out})
}

// withTileType checks the name in a type route's path and that the daemon
// has a library.
func (a LocalAPI) withTileType(next func(w http.ResponseWriter, r *http.Request, name string)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		if !tilelibrary.ValidName(name) || a.TileTypes == nil {
			http.NotFound(w, r)
			return
		}
		next(w, r, name)
	}
}

// writeTileType answers with a type's detail, read again.
func (a LocalAPI) writeTileType(w http.ResponseWriter, r *http.Request, name string) {
	t, err := a.TileTypes.Get(r.Context(), name)
	if err != nil {
		tileTypeError(w, err)
		return
	}
	pins, err := a.tileTypePins(r)
	if err != nil {
		apiError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, detailOf(t, pins))
}

// readTileType serves GET /api/tile-types/{name}: the type's detail.
func (a LocalAPI) readTileType(w http.ResponseWriter, r *http.Request, name string) {
	a.writeTileType(w, r, name)
}

// renameTileType serves PUT /api/tile-types/{name}/title {"title"}: the
// title is all a rename changes, as a new version.
func (a LocalAPI) renameTileType(w http.ResponseWriter, r *http.Request, name string) {
	var body struct {
		Title string `json:"title"`
	}
	if !decodeTileTypeBody(w, r, &body) {
		return
	}
	title := strings.TrimSpace(body.Title)
	if title == "" {
		writeTileTypeRefusal(w, http.StatusBadRequest, "A tile type needs a title")
		return
	}
	if _, err := a.TileTypes.Rename(r.Context(), name, title); err != nil {
		tileTypeError(w, err)
		return
	}
	a.writeTileType(w, r, name)
}

// restoreTileType serves POST /api/tile-types/{name}/restore {"digest"}.
func (a LocalAPI) restoreTileType(w http.ResponseWriter, r *http.Request, name string) {
	var body struct {
		Digest string `json:"digest"`
	}
	if !decodeTileTypeBody(w, r, &body) {
		return
	}
	if _, err := a.TileTypes.Restore(r.Context(), name, body.Digest); err != nil {
		tileTypeError(w, err)
		return
	}
	a.writeTileType(w, r, name)
}

// deleteTileType serves DELETE /api/tile-types/{name}: the type is hidden,
// with Undo (undelete) for tilelibrary.UndoWindow. Its pins stay, so Undo
// brings it back pinned where it was.
func (a LocalAPI) deleteTileType(w http.ResponseWriter, r *http.Request, name string) {
	if err := a.TileTypes.Delete(r.Context(), name); err != nil {
		tileTypeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// undeleteTileType serves POST /api/tile-types/{name}/undelete.
func (a LocalAPI) undeleteTileType(w http.ResponseWriter, r *http.Request, name string) {
	if err := a.TileTypes.Undelete(r.Context(), name); err != nil {
		tileTypeError(w, err)
		return
	}
	a.writeTileType(w, r, name)
}

// pinTileType serves PUT /api/tile-types/{name}/pins {"repositories"}: the
// type is pinned to exactly those picked repositories, written into each
// repository's settings (repos.Settings.Tiles.Types) as the repository
// page's own controls write them.
func (a LocalAPI) pinTileType(w http.ResponseWriter, r *http.Request, name string) {
	var body struct {
		Repositories []string `json:"repositories"`
	}
	if !decodeTileTypeBody(w, r, &body) {
		return
	}
	if _, err := a.TileTypes.Get(r.Context(), name); err != nil {
		tileTypeError(w, err)
		return
	}
	if a.Registry == nil {
		http.Error(w, "repositories unavailable", http.StatusServiceUnavailable)
		return
	}
	err := a.Registry.SetTileTypePins(r.Context(), name, body.Repositories)
	switch {
	case errors.Is(err, repos.ErrNotFound):
		writeTileTypeRefusal(w, http.StatusBadRequest, "A tile type is pinned only to repositories baum follows")
		return
	case errors.Is(err, repos.ErrTooManyPins):
		repository, _, _ := strings.Cut(err.Error(), ":")
		writeTileTypeRefusal(w, http.StatusConflict, repository+" already pins the most tile types it can; unpin one there first")
		return
	case err != nil:
		apiError(w, err)
		return
	}
	if a.Changed != nil {
		a.Changed()
	}
	a.writeTileType(w, r, name)
}

// decodeTileTypeBody reads a small JSON body with no other member.
func decodeTileTypeBody(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if d.Decode(v) != nil || d.Decode(new(any)) != io.EOF {
		http.Error(w, "invalid JSON request", http.StatusBadRequest)
		return false
	}
	return true
}

// writeTileTypeRefusal answers with the library's reason, which the page
// shows as it is.
func writeTileTypeRefusal(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"message": message})
}

// tileTypeError answers a library error: an unknown type or version is 404,
// a type changed meanwhile 409, a refused save 400 with its reason (which
// names places, never content), and anything else 500.
func tileTypeError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, tilelibrary.ErrUnknown), errors.Is(err, tilelibrary.ErrNoVersion):
		writeTileTypeRefusal(w, http.StatusNotFound, err.Error())
	case errors.Is(err, tilelibrary.ErrStale), errors.Is(err, tilelibrary.ErrDeleted), errors.Is(err, tilelibrary.ErrExists):
		writeTileTypeRefusal(w, http.StatusConflict, err.Error())
	case errors.Is(err, tilelibrary.ErrTooMany), errors.Is(err, tilelibrary.ErrTooLarge):
		writeTileTypeRefusal(w, http.StatusBadRequest, err.Error())
	default:
		var refused *tilelibrary.RefusedError
		if errors.As(err, &refused) {
			writeTileTypeRefusal(w, http.StatusBadRequest, err.Error())
			return
		}
		apiError(w, err)
	}
}
