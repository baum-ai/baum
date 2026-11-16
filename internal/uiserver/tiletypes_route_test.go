package uiserver

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/baum-ai/baum/internal/appdb/appdbtest"
	"github.com/baum-ai/baum/internal/repos"
	"github.com/baum-ai/baum/internal/tilelibrary"
	"github.com/baum-ai/baum/internal/tileview"
	"github.com/baum-ai/baum/tiles/library"
)

func previewServer(t *testing.T) (*Server, *tilelibrary.Store) {
	t.Helper()
	registry, err := repos.Open(filepath.Join(appdbtest.TempDir(t), "repos.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = registry.Close() })
	store := &tilelibrary.Store{Path: filepath.Join(appdbtest.TempDir(t), "app.sqlite")}
	handler := LocalAPI{Registry: registry, TileTypes: store}.Handler()
	t.Cleanup(handler.Close)
	s, err := New(Options{API: handler})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Listen(0); err != nil {
		t.Fatal(err)
	}
	return s, store
}

func previewGet(s *Server, path, auth string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("GET", s.Origin()+path, nil)
	if auth != "" {
		r.Header.Set("Authorization", auth)
	}
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	return w
}

// The preview route has the same auth as the other routes, serves a saved
// type's definition without its HTML, the sample's data and its citations'
// payloads, and registers the digest so its frame loads.
func TestTileTypePreviewRoute(t *testing.T) {
	s, store := previewServer(t)
	entry, _ := library.Get("schema_change")
	def := entry.Definition
	def.Name, def.Title = "migrations", "Migrations"
	encoded, err := library.Seal(&def)
	if err != nil {
		t.Fatal(err)
	}
	c := tilelibrary.Change{Definition: encoded, Glance: entry.Glance, AppliesWith: entry.AppliesWith, Origin: tilelibrary.OriginChat}
	for _, sample := range entry.Samples {
		if tilelibrary.IsSynthetic(sample) {
			c.Samples = append(c.Samples, sample)
		}
	}
	saved, err := store.Create(context.Background(), c, nil)
	if err != nil {
		t.Fatal(err)
	}
	digest := saved.Entry.Definition.Digest
	if _, ok := tileview.FrameRegistry().Get(digest); ok {
		t.Fatal("registered before the route served it")
	}
	path := "/api/tile-types/migrations/preview"

	for name, auth := range map[string]string{"no credentials": "", "wrong token": "Bearer wrong"} {
		if w := previewGet(s, path, auth); w.Code != 401 {
			t.Errorf("%s: %d", name, w.Code)
		}
	}
	if _, ok := tileview.FrameRegistry().Get(digest); ok {
		t.Fatal("an unauthenticated request registered a digest")
	}
	w := previewGet(s, path, "Bearer "+s.launch)
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	var got struct {
		Name       string
		Builtin    bool
		Digest     string
		Definition struct {
			Digest string
			Source struct{ Bytes int }
		}
		Samples []string
		Sample  string
		Data    json.RawMessage
		Refs    struct{ Glance, Detail map[string]any }
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Name != "migrations" || got.Builtin || got.Digest != digest || got.Definition.Digest != digest || got.Definition.Source.Bytes == 0 ||
		len(got.Samples) != len(c.Samples) || got.Sample != c.Samples[0].Name || len(got.Data) == 0 {
		t.Errorf("preview: %+v", got)
	}
	if strings.Contains(w.Body.String(), saved.Entry.Definition.Source.HTML[:200]) || strings.Contains(w.Body.String(), `"html":`) {
		t.Error("the preview carries the type's source")
	}
	if len(got.Refs.Detail) == 0 {
		want, _ := c.Samples[0].Refs("detail")
		if len(want) != 0 {
			t.Errorf("refs: %+v", got.Refs)
		}
	}
	if _, ok := tileview.FrameRegistry().Get(digest); !ok {
		t.Error("the digest is not registered")
	}
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Error("the preview is cacheable")
	}

	// A sample by name, an unknown sample, an unknown type, a built-in type.
	last := c.Samples[len(c.Samples)-1].Name
	var picked struct{ Sample string }
	if w := previewGet(s, path+"?sample="+last, "Bearer "+s.launch); w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &picked) != nil || picked.Sample != last {
		t.Errorf("sample %s: %d %s", last, w.Code, picked.Sample)
	}
	for _, p := range []string{path + "?sample=nope", "/api/tile-types/nothing/preview", "/api/tile-types/Bad-Name/preview"} {
		if w := previewGet(s, p, "Bearer "+s.launch); w.Code != 404 {
			t.Errorf("%s: %d", p, w.Code)
		}
	}
	w = previewGet(s, "/api/tile-types/schema_change/preview", "Bearer "+s.launch)
	var builtin struct{ Builtin bool }
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &builtin) != nil || !builtin.Builtin {
		t.Errorf("built-in preview: %d %.200s", w.Code, w.Body.String())
	}
	// A deleted type no longer previews.
	if err := store.Delete(context.Background(), "migrations"); err != nil {
		t.Fatal(err)
	}
	if w := previewGet(s, path, "Bearer "+s.launch); w.Code != 404 {
		t.Errorf("a deleted type: %d", w.Code)
	}
}

// Without a library only baum's own types preview.
func TestTileTypePreviewWithoutALibrary(t *testing.T) {
	registry, err := repos.Open(filepath.Join(appdbtest.TempDir(t), "repos.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = registry.Close() })
	handler := LocalAPI{Registry: registry}.Handler()
	t.Cleanup(handler.Close)
	s, err := New(Options{API: handler})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Listen(0); err != nil {
		t.Fatal(err)
	}
	if w := previewGet(s, "/api/tile-types/anything/preview", "Bearer "+s.launch); w.Code != 404 {
		t.Errorf("%d", w.Code)
	}
	if w := previewGet(s, "/api/tile-types/api_change/preview", "Bearer "+s.launch); w.Code != 200 {
		t.Errorf("%d", w.Code)
	}
}
