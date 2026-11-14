package uiserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/baum-ai/baum/internal/appdb/appdbtest"
	"github.com/baum-ai/baum/internal/repos"
)

// Tile reviews are off until turned on; the route saves the change and
// refuses what it does not know.
func TestLabsSettings(t *testing.T) {
	registry, err := repos.Open(filepath.Join(appdbtest.TempDir(t), "repos.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = registry.Close() })
	handler := LocalAPI{Registry: registry}.Handler()
	t.Cleanup(handler.Close)
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	call := func(method, body string, code int) labsSettings {
		t.Helper()
		req, err := http.NewRequest(method, server.URL+"/api/settings/labs", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		response, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = response.Body.Close() }()
		if response.StatusCode != code {
			t.Fatalf("%s %s: got %d, want %d", method, body, response.StatusCode, code)
		}
		var got labsSettings
		if code == http.StatusOK {
			if err := json.NewDecoder(response.Body).Decode(&got); err != nil {
				t.Fatal(err)
			}
		}
		return got
	}
	if got := call("GET", "", 200); got.TileReviews {
		t.Fatal("tile reviews are on by default")
	}
	if got := call("PUT", `{"tile_reviews":true}`, 200); !got.TileReviews {
		t.Fatal("not turned on")
	}
	if prefs, err := registry.Preferences(context.Background()); err != nil || !prefs.Labs.TileReviewsOn() {
		t.Fatalf("not saved: %v", err)
	}
	// With tile reviews on, an arranged version opens on its tiles; with the
	// tiles page chosen every version does already.
	appearance := func(method, body string) appearanceSettings {
		t.Helper()
		req, _ := http.NewRequest(method, server.URL+"/api/settings/appearance", strings.NewReader(body))
		response, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = response.Body.Close() }()
		var got appearanceSettings
		if err := json.NewDecoder(response.Body).Decode(&got); err != nil {
			t.Fatal(err)
		}
		return got
	}
	if got := appearance("GET", ""); !got.ArrangedTiles || got.ReviewPage != "full" {
		t.Errorf("appearance %+v", got)
	}
	if got := appearance("PUT", `{"review_page":"full"}`); !got.ArrangedTiles {
		t.Error("the full review page keeps arranged versions off their tiles")
	}
	if got := appearance("PUT", `{"review_page":"tiles"}`); got.ArrangedTiles {
		t.Error("arranged tiles with the tiles page chosen")
	}
	if got := call("PUT", `{}`, 200); !got.TileReviews {
		t.Error("an empty change turned it off")
	}
	for _, invalid := range []string{`{"tile_reviews":"yes"}`, `{"other":true}`, `{} {}`} {
		call("PUT", invalid, 400)
	}
	if got := call("PUT", `{"tile_reviews":false}`, 200); got.TileReviews || call("GET", "", 200).TileReviews {
		t.Error("not turned off")
	}
}
