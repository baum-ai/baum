package repos

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
)

// Pinning a tile type writes it into exactly the repositories listed, keeps
// each repository's other settings and pins, and refuses a repository that
// is not picked or already pins the most.
func TestSetTileTypePins(t *testing.T) {
	ctx := context.Background()
	r := registry(t)
	for _, name := range []string{"acme/web", "acme/api", "acme/docs"} {
		if _, e := r.Pick(ctx, name, nil); e != nil {
			t.Fatal(e)
		}
	}
	if e := r.UpdateRepository(ctx, "acme/web", Settings{Automatic: new(false), Tiles: &TileSettings{Types: []string{"env_vars"}}}); e != nil {
		t.Fatal(e)
	}
	pins := func(repository string) []string {
		t.Helper()
		s, _, e := r.RepositorySettings(ctx, repository)
		if e != nil {
			t.Fatal(e)
		}
		return s.PinnedTileTypes()
	}
	if e := r.SetTileTypePins(ctx, "migrations", []string{"ACME/Web", "acme/api"}); e != nil {
		t.Fatal(e)
	}
	if got := pins("acme/web"); !slices.Equal(got, []string{"env_vars", "migrations"}) {
		t.Errorf("acme/web pins %v", got)
	}
	if s, _, _ := r.RepositorySettings(ctx, "acme/web"); s.Automatic == nil || *s.Automatic {
		t.Error("the repository's other settings changed")
	}
	if got := pins("acme/api"); !slices.Equal(got, []string{"migrations"}) {
		t.Errorf("acme/api pins %v", got)
	}
	if got := pins("acme/docs"); len(got) != 0 {
		t.Errorf("acme/docs pins %v", got)
	}
	// Moving the pin removes it where it is no longer listed.
	if e := r.SetTileTypePins(ctx, "migrations", []string{"acme/docs"}); e != nil {
		t.Fatal(e)
	}
	if got := pins("acme/web"); !slices.Equal(got, []string{"env_vars"}) {
		t.Errorf("acme/web pins %v", got)
	}
	if s, _, _ := r.RepositorySettings(ctx, "acme/api"); s.Tiles != nil {
		t.Errorf("a repository with no pins left keeps %+v", s.Tiles)
	}
	if e := r.SetTileTypePins(ctx, "migrations", []string{"acme/docs", "acme/elsewhere"}); !errors.Is(e, ErrNotFound) {
		t.Errorf("an unpicked repository: %v", e)
	}
	full := make([]string, MaxPinnedTileTypes)
	for i := range full {
		full[i] = fmt.Sprintf("type_%d", i)
	}
	if e := r.UpdateRepository(ctx, "acme/api", Settings{Tiles: &TileSettings{Types: full}}); e != nil {
		t.Fatal(e)
	}
	if e := r.SetTileTypePins(ctx, "migrations", []string{"acme/api"}); !errors.Is(e, ErrTooManyPins) || !strings.Contains(e.Error(), "acme/api") {
		t.Errorf("a full repository: %v", e)
	}
	// Nothing was written by the refused calls.
	if got := pins("acme/docs"); !slices.Equal(got, []string{"migrations"}) {
		t.Errorf("acme/docs pins %v after refusals", got)
	}
	if e := r.SetTileTypePins(ctx, "Not A Name", nil); e == nil {
		t.Error("a name that is not a tile type's is pinned")
	}
}

// A settings PUT validates the pins it carries, and one without a tiles
// member keeps the pins stored: the repository page's Reviews section saves
// the whole object without them.
func TestSettingsPutKeepsAndChecksPins(t *testing.T) {
	ctx := context.Background()
	r := registry(t)
	if _, e := r.Pick(ctx, "acme/web", nil); e != nil {
		t.Fatal(e)
	}
	c, e := r.Add(ctx, clone(t, "https://github.com/acme/web"), nil)
	if e != nil {
		t.Fatal(e)
	}
	if e := r.SetTileTypePins(ctx, "migrations", []string{"acme/web"}); e != nil {
		t.Fatal(e)
	}
	h := r.Handler()
	put := func(path, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodPut, path, strings.NewReader(body)))
		return w
	}
	for _, path := range []string{"/api/repositories/acme/web/settings", "/api/repos/" + c.ID + "/settings"} {
		if w := put(path, `{"automatic":false,"decisions":null}`); w.Code != 200 || !strings.Contains(w.Body.String(), `"migrations"`) {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body)
		}
		s, _, _ := r.RepositorySettings(ctx, "acme/web")
		if !slices.Equal(s.PinnedTileTypes(), []string{"migrations"}) || s.Automatic == nil || *s.Automatic {
			t.Fatalf("%s: stored %+v", path, s)
		}
		for _, body := range []string{
			`{"automatic":null,"tiles":{"types":["a","b","c","d","e","f","g","h","i"]}}`,
			`{"automatic":null,"tiles":{"types":["Bad Name"]}}`,
			`{"automatic":null,"tiles":{"pins":["Bad Name"]}}`,
			`{"automatic":null,"tiles":{"pins":["a","b","c","d","e","f","g","h","i"]}}`,
			`{"automatic":null,"tiles":{"text":"` + strings.Repeat("x", MaxTileText+1) + `"}}`,
			`{"automatic":null,"tiles":{"unknown":1}}`,
		} {
			if w := put(path, body); w.Code != 400 {
				t.Errorf("%s %s: %d", path, body, w.Code)
			}
		}
	}
	// An explicit tiles member replaces them.
	if w := put("/api/repositories/acme/web/settings", `{"automatic":null,"tiles":null}`); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	if s, _, _ := r.RepositorySettings(ctx, "acme/web"); s.Tiles != nil {
		t.Errorf("tiles kept: %+v", s.Tiles)
	}
}

// Unpinning the last type keeps the rest of tiles (its text and pins), and
// empty considers every field.
func TestUnpinningKeepsOtherTileSettings(t *testing.T) {
	ctx := context.Background()
	r := registry(t)
	if _, e := r.Pick(ctx, "acme/web", nil); e != nil {
		t.Fatal(e)
	}
	stored := &TileSettings{Types: []string{"migrations"}, Text: "Focus on schema.", Pins: []string{"risks"}}
	if e := r.UpdateRepository(ctx, "acme/web", Settings{Tiles: stored}); e != nil {
		t.Fatal(e)
	}
	if e := r.SetTileTypePins(ctx, "migrations", nil); e != nil {
		t.Fatal(e)
	}
	s, _, _ := r.RepositorySettings(ctx, "acme/web")
	if s.Tiles == nil || s.Tiles.Text != "Focus on schema." || !slices.Equal(s.Tiles.Pins, []string{"risks"}) || len(s.Tiles.Types) != 0 {
		t.Errorf("tiles after unpinning: %+v", s.Tiles)
	}
	for name, tiles := range map[string]TileSettings{"types": {Types: []string{"a"}}, "text": {Text: "t"}, "pins": {Pins: []string{"a"}}} {
		if tiles.empty() {
			t.Errorf("%s: settings with a field are empty", name)
		}
	}
	if !(TileSettings{}).empty() {
		t.Error("no fields is not empty")
	}
}

// A settings PUT merges tiles per member: what it leaves out keeps its
// stored value, what it sends (even empty) replaces it, and tiles: null
// clears all.
func TestSettingsPutMergesTilesPerMember(t *testing.T) {
	ctx := context.Background()
	r := registry(t)
	if _, e := r.Pick(ctx, "acme/web", nil); e != nil {
		t.Fatal(e)
	}
	c, e := r.Add(ctx, clone(t, "https://github.com/acme/web"), nil)
	if e != nil {
		t.Fatal(e)
	}
	h := r.Handler()
	put := func(path, body string) {
		t.Helper()
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodPut, path, strings.NewReader(body)))
		if w.Code != 200 {
			t.Fatalf("%s %s: %d %s", path, body, w.Code, w.Body)
		}
	}
	tilesOf := func() TileSettings {
		t.Helper()
		s, _, _ := r.RepositorySettings(ctx, "acme/web")
		if s.Tiles == nil {
			return TileSettings{}
		}
		return *s.Tiles
	}
	for _, path := range []string{"/api/repositories/acme/web/settings", "/api/repos/" + c.ID + "/settings"} {
		put(path, `{"automatic":null,"tiles":null}`)
		put(path, `{"automatic":null,"tiles":{"types":["migrations"],"text":"Focus.","pins":["risks"]}}`)
		// Only types: text and pins stay.
		put(path, `{"automatic":null,"tiles":{"types":["env_vars"]}}`)
		if got := tilesOf(); !slices.Equal(got.Types, []string{"env_vars"}) || got.Text != "Focus." || !slices.Equal(got.Pins, []string{"risks"}) {
			t.Fatalf("%s: only types: %+v", path, got)
		}
		// Only text: types and pins stay.
		put(path, `{"automatic":null,"tiles":{"text":"Other."}}`)
		if got := tilesOf(); !slices.Equal(got.Types, []string{"env_vars"}) || got.Text != "Other." || !slices.Equal(got.Pins, []string{"risks"}) {
			t.Fatalf("%s: only text: %+v", path, got)
		}
		// Only pins: types and text stay.
		put(path, `{"automatic":null,"tiles":{"pins":["overview","evidence"]}}`)
		if got := tilesOf(); !slices.Equal(got.Types, []string{"env_vars"}) || got.Text != "Other." || !slices.Equal(got.Pins, []string{"overview", "evidence"}) {
			t.Fatalf("%s: only pins: %+v", path, got)
		}
		// A member sent empty clears just it.
		put(path, `{"automatic":null,"tiles":{"types":[]}}`)
		if got := tilesOf(); len(got.Types) != 0 || got.Text != "Other." || len(got.Pins) != 2 {
			t.Fatalf("%s: types cleared: %+v", path, got)
		}
		// No tiles member keeps all; an empty object keeps all too.
		put(path, `{"automatic":null}`)
		put(path, `{"automatic":null,"tiles":{}}`)
		if got := tilesOf(); got.Text != "Other." || len(got.Pins) != 2 {
			t.Fatalf("%s: kept: %+v", path, got)
		}
		// Clearing every member leaves no tiles.
		put(path, `{"automatic":null,"tiles":{"text":"","pins":[]}}`)
		if got := tilesOf(); !got.empty() {
			t.Fatalf("%s: cleared: %+v", path, got)
		}
	}
}
