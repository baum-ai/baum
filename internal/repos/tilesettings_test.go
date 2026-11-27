package repos

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
)

func TestSettingsCheckedTidiesTileSettings(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   *TileSettings
		want *TileSettings
		fail string
	}{
		{name: "none", in: nil},
		{name: "empty settings are none", in: &TileSettings{Types: []string{}}},
		{name: "repeats and blanks dropped", in: &TileSettings{Types: []string{"env_vars", " env_vars ", ""}}, want: &TileSettings{Types: []string{"env_vars"}}},
		{name: "not a type's name", in: &TileSettings{Types: []string{"Bad Name"}}, fail: "is not a saved tile type's name"},
		{name: "too many types", in: &TileSettings{Types: []string{"a", "b", "c", "d", "e", "f", "g", "h", "i"}}, fail: "9 tile types are saved; the most is 8"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Settings{Tiles: tc.in}.Checked()
			if tc.fail != "" {
				if !errors.Is(err, ErrInvalidSettings) || !strings.Contains(err.Error(), tc.fail) {
					t.Fatalf("got %+v, %v; want a refusal containing %q", got.Tiles, err, tc.fail)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			switch {
			case tc.want == nil && got.Tiles != nil:
				t.Fatalf("got %+v, want none", got.Tiles)
			case tc.want != nil && (got.Tiles == nil || !slices.Equal(got.Tiles.Types, tc.want.Types)):
				t.Fatalf("got %+v, want %+v", got.Tiles, tc.want)
			}
		})
	}
}

// The routes save a repository's pinned tile types with the host's reason
// for a refusal, and drop the tile grid's text and pins an older app sends.
func TestTileSettingsAreSavedByTheRoutesWithTheHostsReason(t *testing.T) {
	ctx := context.Background()
	r := registry(t)
	h := r.Handler()
	do := func(method, path, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(method, path, strings.NewReader(body)))
		return w
	}
	if _, e := r.Pick(ctx, "owner/repo", nil); e != nil {
		t.Fatal(e)
	}
	const route = "/api/repositories/owner/repo/settings"
	w := do("PUT", route, `{"automatic":null,"tiles":{"types":["env_vars","env_vars"],"text":" Lead with the schema. ","pins":["schema_change"]}}`)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"tiles":{"types":["env_vars"]}`) {
		t.Fatal(w.Code, w.Body)
	}
	w = do("PUT", route, `{"automatic":null,"tiles":{"types":["Bad Name"]}}`)
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "is not a saved tile type's name") || strings.Contains(w.Body.String(), "invalid repository settings") {
		t.Fatal(w.Code, w.Body)
	}
	if s, _, _ := r.RepositorySettings(ctx, "owner/repo"); s.Tiles == nil || !slices.Equal(s.Tiles.Types, []string{"env_vars"}) {
		t.Errorf("a refused save changed the stored settings: %+v", s.Tiles)
	}
	if w = do("PUT", route, `{"automatic":null}`); w.Code != http.StatusOK {
		t.Fatal(w.Code, w.Body)
	}
	if s, _, _ := r.RepositorySettings(ctx, "owner/repo"); s.Tiles == nil {
		t.Error("leaving tiles out cleared them")
	}
	if w = do("PUT", route, `{"automatic":null,"tiles":null}`); w.Code != http.StatusOK {
		t.Fatal(w.Code, w.Body)
	}
	if s, _, _ := r.RepositorySettings(ctx, "owner/repo"); s.Tiles != nil {
		t.Errorf("tiles %+v", s.Tiles)
	}
	// A save through the transaction the proposals use checks too.
	_, e := r.UpdateRepositoryIf(ctx, "owner/repo", func(s Settings) (Settings, error) {
		s.Tiles = &TileSettings{Types: []string{"Bad Name"}}
		return s, nil
	})
	if !errors.Is(e, ErrInvalidSettings) {
		t.Fatal(e)
	}
	if w = do("POST", "/api/repositories", `{"repository":"owner/other","settings":{"automatic":null,"tiles":{"types":["Bad Name"]}}}`); w.Code != http.StatusBadRequest {
		t.Fatal(w.Code, w.Body)
	}
}
