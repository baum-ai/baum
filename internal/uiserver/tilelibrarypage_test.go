package uiserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/baum-ai/baum/internal/appdb/appdbtest"
	"github.com/baum-ai/baum/internal/repos"
	"github.com/baum-ai/baum/internal/tilelibrary"
	"github.com/baum-ai/baum/internal/tileview"
	"github.com/baum-ai/baum/tiles/library"
)

// savedChange is a type of the given name, written as schema_change is, with
// its synthetic samples.
func savedChange(t *testing.T, name, title string) tilelibrary.Change {
	t.Helper()
	entry, _ := library.Get("schema_change")
	def := entry.Definition
	def.Name, def.Title = name, title
	encoded, err := library.Seal(&def)
	if err != nil {
		t.Fatal(err)
	}
	c := tilelibrary.Change{Definition: encoded, Glance: entry.Glance, ReviewChecks: entry.ReviewChecks, AppliesWith: entry.AppliesWith,
		Paths: []string{"**/*.sql"}, Origin: tilelibrary.OriginChat}
	for _, sample := range entry.Samples {
		if tilelibrary.IsSynthetic(sample) {
			c.Samples = append(c.Samples, sample)
		}
	}
	return c
}

type libraryAPI struct {
	t        *testing.T
	handler  http.Handler
	store    *tilelibrary.Store
	registry *repos.Registry
}

func newLibraryAPI(t *testing.T) libraryAPI {
	t.Helper()
	return newLibraryAPIChecking(t, nil)
}

// newLibraryAPIChecking is newLibraryAPI with the render check an import and
// a first preview run on its samples.
func newLibraryAPIChecking(t *testing.T, check tilelibrary.SampleCheck) libraryAPI {
	t.Helper()
	dir := appdbtest.TempDir(t)
	registry, err := repos.Open(filepath.Join(dir, "app.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = registry.Close() })
	store := &tilelibrary.Store{Path: filepath.Join(dir, "app.sqlite")}
	handler := LocalAPI{Registry: registry, TileTypes: store, TileImportCheck: check}.Handler()
	t.Cleanup(handler.Close)
	return libraryAPI{t: t, handler: handler, store: store, registry: registry}
}

func (l libraryAPI) do(method, path, body string, out any) int {
	l.t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	w := httptest.NewRecorder()
	l.handler.ServeHTTP(w, r)
	if out != nil && w.Code < 300 {
		if err := json.Unmarshal(w.Body.Bytes(), out); err != nil {
			l.t.Fatalf("%s %s: %v %s", method, path, err, w.Body)
		}
	}
	if w.Code >= 300 && out != nil {
		_ = json.Unmarshal(w.Body.Bytes(), out)
	}
	return w.Code
}

// The library page lists the saved types with what its rows show, reads one
// without its HTML, renames it, restores a version, pins it to repositories
// and deletes it with Undo.
func TestTileLibraryRoutes(t *testing.T) {
	l := newLibraryAPI(t)
	ctx := context.Background()
	var list struct{ Types []tileTypeSummary }
	if code := l.do("GET", "/api/tile-types", "", &list); code != 200 || list.Types == nil || len(list.Types) != 0 {
		t.Fatalf("empty library: %d %+v", code, list)
	}
	if _, err := l.store.Create(ctx, savedChange(t, "migrations", "Migrations"), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := l.store.Create(ctx, savedChange(t, "env_vars", "Environment variables"), nil); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"acme/web", "acme/api"} {
		if _, err := l.registry.Pick(ctx, name, nil); err != nil {
			t.Fatal(err)
		}
	}
	if code := l.do("GET", "/api/tile-types", "", &list); code != 200 || len(list.Types) != 2 {
		t.Fatalf("list: %d %+v", code, list)
	}
	first := list.Types[0]
	if first.Name != "env_vars" || first.Title != "Environment variables" || first.Origin != "chat" || first.UseWhen == "" ||
		first.Versions != 1 || len(first.Samples) == 0 || len(first.Pinned) != 0 || first.UsedIn != 0 || first.UsedAt != "" {
		t.Errorf("summary: %+v", first)
	}

	raw := httptest.NewRecorder()
	l.handler.ServeHTTP(raw, httptest.NewRequest("GET", "/api/tile-types/migrations", nil))
	if raw.Code != 200 || strings.Contains(raw.Body.String(), `"html"`) || strings.Contains(raw.Body.String(), "<script") {
		t.Fatalf("detail: %d, carries the source: %v", raw.Code, strings.Contains(raw.Body.String(), "<script"))
	}
	var detail tileTypeDetail
	if code := l.do("GET", "/api/tile-types/migrations", "", &detail); code != 200 || detail.Fill == "" || !slices.Equal(detail.Paths, []string{"**/*.sql"}) || len(detail.VersionList) != 1 {
		t.Fatalf("detail: %d %+v", code, detail)
	}
	original := detail.Digest

	// Rename: the title, as a new version.
	if code := l.do("PUT", "/api/tile-types/migrations/title", `{"title":"  Database migrations "}`, &detail); code != 200 || detail.Title != "Database migrations" || len(detail.VersionList) != 2 || detail.Digest == original {
		t.Fatalf("rename: %d %+v", code, detail)
	}
	var refusal struct{ Message string }
	if code := l.do("PUT", "/api/tile-types/migrations/title", `{"title":"   "}`, &refusal); code != 400 || refusal.Message == "" {
		t.Errorf("an empty title: %d %+v", code, refusal)
	}
	if code := l.do("PUT", "/api/tile-types/migrations/title", `{"title":"`+strings.Repeat("x", 500)+`"}`, &refusal); code != 400 || refusal.Message == "" {
		t.Errorf("a title too long: %d %+v", code, refusal)
	}
	if code := l.do("PUT", "/api/tile-types/migrations/title", `{"title":"x","more":1}`, nil); code != 400 {
		t.Errorf("an unknown member: %d", code)
	}

	// Restore the first version.
	if code := l.do("POST", "/api/tile-types/migrations/restore", `{"digest":"`+original+`"}`, &detail); code != 200 || detail.Title != "Migrations" || detail.Digest != original {
		t.Fatalf("restore: %d %+v", code, detail)
	}
	if code := l.do("POST", "/api/tile-types/migrations/restore", `{"digest":"sha256:`+strings.Repeat("0", 64)+`"}`, nil); code != 404 {
		t.Errorf("an unknown version: %d", code)
	}

	// Pins.
	if code := l.do("PUT", "/api/tile-types/migrations/pins", `{"repositories":["acme/web","acme/api"]}`, &detail); code != 200 || !slices.Equal(detail.Pinned, []string{"acme/api", "acme/web"}) {
		t.Fatalf("pin: %d %+v", code, detail)
	}
	if s, _, _ := l.registry.RepositorySettings(ctx, "acme/web"); !slices.Equal(s.PinnedTileTypes(), []string{"migrations"}) {
		t.Errorf("acme/web settings: %+v", s)
	}
	if code := l.do("PUT", "/api/tile-types/migrations/pins", `{"repositories":["acme/api"]}`, &detail); code != 200 || !slices.Equal(detail.Pinned, []string{"acme/api"}) {
		t.Fatalf("unpin: %d %+v", code, detail)
	}
	if code := l.do("PUT", "/api/tile-types/migrations/pins", `{"repositories":["acme/elsewhere"]}`, &refusal); code != 400 || !strings.Contains(refusal.Message, "repositories baum follows") {
		t.Errorf("an unpicked repository: %d %+v", code, refusal)
	}
	if code := l.do("PUT", "/api/tile-types/nothing/pins", `{"repositories":["acme/api"]}`, nil); code != 404 {
		t.Errorf("an unknown type: %d", code)
	}
	full := []string{"a1", "a2", "a3", "a4", "a5", "a6", "a7", "a8"}
	if err := l.registry.UpdateRepository(ctx, "acme/web", repos.Settings{Tiles: &repos.TileSettings{Types: full}}); err != nil {
		t.Fatal(err)
	}
	if code := l.do("PUT", "/api/tile-types/migrations/pins", `{"repositories":["acme/web"]}`, &refusal); code != 409 || !strings.Contains(refusal.Message, "acme/web") {
		t.Errorf("a full repository: %d %+v", code, refusal)
	}

	// Delete and undo.
	if code := l.do("DELETE", "/api/tile-types/migrations", "", nil); code != 204 {
		t.Fatalf("delete: %d", code)
	}
	if code := l.do("GET", "/api/tile-types", "", &list); code != 200 || len(list.Types) != 1 {
		t.Errorf("after delete: %+v", list)
	}
	if code := l.do("GET", "/api/tile-types/migrations", "", nil); code != 404 {
		t.Errorf("a deleted type reads: %d", code)
	}
	if code := l.do("POST", "/api/tile-types/migrations/undelete", "", &detail); code != 200 || detail.Name != "migrations" || !slices.Equal(detail.Pinned, []string{"acme/api"}) {
		t.Fatalf("undelete: %d %+v", code, detail)
	}
	if code := l.do("POST", "/api/tile-types/migrations/undelete", "", nil); code != 404 {
		t.Errorf("undelete twice: %d", code)
	}
	for _, path := range []string{"/api/tile-types/Bad-Name", "/api/tile-types/nothing"} {
		if code := l.do("GET", path, "", nil); code != 404 {
			t.Errorf("%s: %d", path, code)
		}
	}
}

// Without a library the list is empty and a type's routes are not found.
func TestTileLibraryRoutesWithoutALibrary(t *testing.T) {
	handler := LocalAPI{}.Handler()
	t.Cleanup(handler.Close)
	for path, want := range map[string]int{"/api/tile-types": 200, "/api/tile-types/migrations": 404} {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != want {
			t.Errorf("%s: %d", path, w.Code)
		}
	}
}

// A type exported through the page imports into a fresh library: the check
// shows what the dialog shows and registers the frame, the import saves it
// marked imported and not checked yet, and a refused file says why.
func TestTileTypeFileRoutes(t *testing.T) {
	from := newLibraryAPI(t)
	ctx := context.Background()
	// A title of its own, so its digest is not another test's: the frame
	// registry is the process's.
	saved, err := from.store.Create(ctx, savedChange(t, "migrations", "Migrations from a file"), nil)
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	from.handler.ServeHTTP(w, httptest.NewRequest("GET", "/api/tile-types/migrations/export", nil))
	if w.Code != 200 || w.Header().Get("Content-Disposition") != `attachment; filename="migrations.baumtile.json"` {
		t.Fatalf("export: %d %v", w.Code, w.Header())
	}
	file := w.Body.String()
	if !strings.Contains(file, `"schema": "baum.tile-type/1"`) || strings.Contains(file, `"digest"`) {
		t.Errorf("the file: %.300s", file)
	}
	body, _ := json.Marshal(map[string]string{"file": file})

	to := newLibraryAPI(t)
	var check struct {
		Name        string   `json:"name"`
		RenamedFrom string   `json:"renamed_from"`
		Title       string   `json:"title"`
		UseWhen     string   `json:"use_when"`
		Fill        string   `json:"fill"`
		Digest      string   `json:"digest"`
		Sample      string   `json:"sample"`
		Paths       []string `json:"paths"`
	}
	if code := to.do("POST", "/api/tile-type-files/check", string(body), &check); code != 200 || check.Name != "migrations" || check.Title != "Migrations from a file" ||
		check.Fill == "" || check.UseWhen == "" || check.Digest != saved.Entry.Definition.Digest || len(check.Paths) != 1 || check.Sample == "" {
		t.Fatalf("check: %d %+v", code, check)
	}
	if _, ok := tileview.FrameRegistry().Get(check.Digest); !ok {
		t.Error("the checked file's frame is not registered")
	}
	var list struct{ Types []tileTypeSummary }
	if to.do("GET", "/api/tile-types", "", &list); len(list.Types) != 0 {
		t.Fatal("a check saved the type")
	}
	var imported tilelibrary.Imported
	if code := to.do("POST", "/api/tile-type-files", string(body), &imported); code != 200 || imported != (tilelibrary.Imported{Name: "migrations"}) {
		t.Fatalf("import: %d %+v", code, imported)
	}
	if to.do("GET", "/api/tile-types", "", &list); len(list.Types) != 1 || list.Types[0].Origin != "import" || list.Types[0].Checked {
		t.Errorf("after import: %+v", list.Types)
	}
	// Again: the name is taken, so the dialog and the import say so.
	if code := to.do("POST", "/api/tile-type-files/check", string(body), &check); code != 200 || check.Name != "migrations_2" || check.RenamedFrom != "migrations" {
		t.Errorf("a clash: %d %+v", code, check)
	}
	var renamed struct {
		Name        string `json:"name"`
		RenamedFrom string `json:"renamed_from"`
	}
	if code := to.do("POST", "/api/tile-type-files", string(body), &renamed); code != 200 || renamed.Name != "migrations_2" || renamed.RenamedFrom != "migrations" {
		t.Errorf("a clash: %d %+v", code, renamed)
	}
	var refusal struct{ Message string }
	bad, _ := json.Marshal(map[string]string{"file": `{"schema":"baum.tile-type/2"}`})
	if code := to.do("POST", "/api/tile-type-files", string(bad), &refusal); code != 400 || !strings.Contains(refusal.Message, "baum.tile-type/1") {
		t.Errorf("a refused file: %d %+v", code, refusal)
	}
	huge, _ := json.Marshal(map[string]string{"file": strings.Repeat(" ", tilelibrary.MaxFileBytes+1)})
	if code := to.do("POST", "/api/tile-type-files/check", string(huge), &refusal); code != 400 || !strings.Contains(refusal.Message, "3 MiB") {
		t.Errorf("an oversized file: %d %+v", code, refusal)
	}
	if code := to.do("GET", "/api/tile-types/nothing/export", "", nil); code != 404 {
		t.Errorf("export of an unknown type: %d", code)
	}
}

// An import runs the render check when a runner is there and saves the type
// checked; a failed check refuses it; with no runner it is saved "Not checked
// yet", and its first preview, once a runner is there, runs the check in the
// background and marks it checked.
func TestImportRunsTheRenderCheckAndPreviewCompletesIt(t *testing.T) {
	ctx := context.Background()
	from := newLibraryAPI(t)
	if _, err := from.store.Create(ctx, savedChange(t, "checked_import", "Checked on import"), nil); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	from.handler.ServeHTTP(w, httptest.NewRequest("GET", "/api/tile-types/checked_import/export", nil))
	body, _ := json.Marshal(map[string]string{"file": w.Body.String()})

	verdict, runner := tilelibrary.VerdictPass, true
	var mu sync.Mutex
	calls := 0
	to := newLibraryAPIChecking(t, func(_ context.Context, entry library.Entry) (string, error) {
		mu.Lock()
		defer mu.Unlock()
		calls++
		if len(entry.Samples) == 0 {
			t.Error("checked without samples")
		}
		if !runner {
			return "", tilelibrary.ErrNoRunner
		}
		return verdict, nil
	})
	var imported tilelibrary.Imported
	if code := to.do("POST", "/api/tile-type-files", string(body), &imported); code != 200 || !imported.Checked || calls != 1 {
		t.Fatalf("with a runner: %d %+v, %d checks", code, imported, calls)
	}

	// A failed check refuses the file.
	mu.Lock()
	verdict = "fail"
	mu.Unlock()
	var refusal struct{ Message string }
	if code := to.do("POST", "/api/tile-type-files", string(body), &refusal); code != 400 || !strings.Contains(refusal.Message, "render check") {
		t.Fatalf("a failed check: %d %+v", code, refusal)
	}

	// With no runner the type is saved not checked yet, and a preview with
	// no runner leaves it so.
	mu.Lock()
	runner = false
	mu.Unlock()
	if code := to.do("POST", "/api/tile-type-files", string(body), &imported); code != 200 || imported.Checked || imported.Name != "checked_import_2" {
		t.Fatalf("without a runner: %d %+v", code, imported)
	}
	unchecked := func() bool {
		var list struct{ Types []tileTypeSummary }
		to.do("GET", "/api/tile-types", "", &list)
		for _, s := range list.Types {
			if s.Name == "checked_import_2" {
				return !s.Checked
			}
		}
		t.Fatal("type missing")
		return false
	}
	if code := to.do("GET", "/api/tile-types/checked_import_2/preview", "", nil); code != 200 || !unchecked() {
		t.Fatalf("preview without a runner: %d", code)
	}
	// A runner appears: the next preview checks it in the background.
	mu.Lock()
	runner, verdict = true, tilelibrary.VerdictPass
	mu.Unlock()
	deadline := time.Now().Add(10 * time.Second)
	for unchecked() {
		if time.Now().After(deadline) {
			t.Fatal("the preview never checked the type")
		}
		if code := to.do("GET", "/api/tile-types/checked_import_2/preview", "", nil); code != 200 {
			t.Fatalf("preview: %d", code)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
