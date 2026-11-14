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
			`{"automatic":null,"tiles":{"types":["same","same"]}}`,
			`{"automatic":null,"tiles":{"pins":[]}}`,
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
