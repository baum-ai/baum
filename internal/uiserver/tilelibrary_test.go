package uiserver

import (
	"encoding/json"
	"io"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/baum-ai/baum/internal/appdb/appdbtest"
	"github.com/baum-ai/baum/internal/repos"
	"github.com/baum-ai/baum/tiles/library"
)

// GET /api/tile-library lists the built-in library types the page names as
// baum's own, with their digests and never their source.
func TestTileLibraryRoute(t *testing.T) {
	registry, err := repos.Open(filepath.Join(appdbtest.TempDir(t), "repos.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = registry.Close() })
	handler := LocalAPI{Registry: registry}.Handler()
	t.Cleanup(handler.Close)
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	response, err := server.Client().Get(server.URL + "/api/tile-library")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	body, _ := io.ReadAll(response.Body)
	if response.StatusCode != 200 {
		t.Fatalf("status %d", response.StatusCode)
	}
	var got struct {
		Types []map[string]any `json:"types"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Types) != len(library.Names) {
		t.Fatalf("types: %d", len(got.Types))
	}
	for i, entry := range library.All() {
		typ := got.Types[i]
		if typ["name"] != entry.Definition.Name || typ["digest"] != entry.Definition.Digest || typ["title"] != entry.Definition.Title {
			t.Errorf("type %d: %v", i, typ)
		}
	}
	if strings.Contains(string(body), `"single"`) {
		t.Error("the listing names single tile types, which nothing pins any more")
	}
	for _, leak := range []string{"source", "<script", "fill", "data_schema"} {
		if strings.Contains(string(body), leak) {
			t.Errorf("the listing holds %q", leak)
		}
	}
}
