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
	long := strings.Repeat("é", MaxTileText)
	for _, tc := range []struct {
		name string
		in   *TileSettings
		want *TileSettings
		fail string
	}{
		{name: "none", in: nil},
		{name: "empty settings are none", in: &TileSettings{Text: "  \n", Pins: []string{}}},
		{name: "text is trimmed", in: &TileSettings{Text: "  lead with data\n"}, want: &TileSettings{Text: "lead with data"}},
		{name: "text at the limit counts characters, not bytes", in: &TileSettings{Text: long}, want: &TileSettings{Text: long}},
		{name: "text over the limit", in: &TileSettings{Text: long + "x"}, fail: "1001 characters; the most is 1000"},
		{name: "single and library types, repeats dropped", in: &TileSettings{Pins: []string{"schema_change", "risks", "schema_change", " risks "}}, want: &TileSettings{Pins: []string{"schema_change", "risks"}}},
		{name: "a library type by title", in: &TileSettings{Pins: []string{"Schema changes"}}, want: &TileSettings{Pins: []string{"schema_change"}}},
		{name: "a type that has many tiles", in: &TileSettings{Pins: []string{"change"}}, fail: `"change" is not a tile type that can be pinned`},
		{name: "an unknown type", in: &TileSettings{Pins: []string{"nope"}}, fail: "one of overview, risks"},
		{name: "too many pins", in: &TileSettings{Pins: []string{"overview", "risks", "evidence", "intent", "decisions", "context", "media", "everything_else", "schema_change"}}, fail: "9 tiles are pinned; the most is 8"},
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
			case tc.want != nil && (got.Tiles == nil || got.Tiles.Text != tc.want.Text || !slices.Equal(got.Tiles.Pins, tc.want.Pins)):
				t.Fatalf("got %+v, want %+v", got.Tiles, tc.want)
			}
		})
	}
}

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
	w := do("PUT", route, `{"automatic":null,"tiles":{"text":" Lead with the schema. ","pins":["schema_change","schema_change"]}}`)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"tiles":{"text":"Lead with the schema.","pins":["schema_change"]}`) {
		t.Fatal(w.Code, w.Body)
	}
	if s, picked, e := r.RepositorySettings(ctx, "owner/repo"); e != nil || !picked || s.Tiles == nil || s.Tiles.Text != "Lead with the schema." || !slices.Equal(s.Tiles.Pins, []string{"schema_change"}) {
		t.Fatalf("stored %+v %v %v", s, picked, e)
	}
	w = do("PUT", route, `{"automatic":null,"tiles":{"pins":["nope"]}}`)
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), `"nope" is not a tile type that can be pinned`) || strings.Contains(w.Body.String(), "invalid repository settings") {
		t.Fatal(w.Code, w.Body)
	}
	if s, _, _ := r.RepositorySettings(ctx, "owner/repo"); s.Tiles == nil || s.Tiles.Text != "Lead with the schema." {
		t.Errorf("a refused save changed the stored settings: %+v", s.Tiles)
	}
	// Tiles merge per member: an omitted member keeps its stored value, a
	// sent one, even empty, replaces it.
	if w = do("PUT", route, `{"automatic":null,"tiles":{"types":["a","b"]}}`); w.Code != http.StatusOK {
		t.Fatal(w.Code, w.Body)
	}
	if s, _, _ := r.RepositorySettings(ctx, "owner/repo"); s.Tiles == nil || s.Tiles.Text != "Lead with the schema." || !slices.Equal(s.Tiles.Types, []string{"a", "b"}) || !slices.Equal(s.Tiles.Pins, []string{"schema_change"}) {
		t.Errorf("an omitted member did not keep its value: %+v", s.Tiles)
	}
	if w = do("PUT", route, `{"automatic":null,"tiles":{"text":"","pins":[]}}`); w.Code != http.StatusOK {
		t.Fatal(w.Code, w.Body)
	}
	if s, _, _ := r.RepositorySettings(ctx, "owner/repo"); s.Tiles == nil || s.Tiles.Text != "" || len(s.Tiles.Pins) != 0 || !slices.Equal(s.Tiles.Types, []string{"a", "b"}) {
		t.Errorf("empty members did not replace: %+v", s.Tiles)
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
	if w = do("PUT", route, `{"automatic":null,"tiles":{"types":["1","2","3","4","5","6","7","8","9"]}}`); w.Code != http.StatusBadRequest {
		t.Fatal(w.Code, w.Body)
	}
	if s, _, _ := r.RepositorySettings(ctx, "owner/repo"); s.Tiles != nil {
		t.Errorf("tiles %+v", s.Tiles)
	}
	// A save through the transaction the proposals use checks too.
	_, e := r.UpdateRepositoryIf(ctx, "owner/repo", func(s Settings) (Settings, error) {
		s.Tiles = &TileSettings{Text: strings.Repeat("x", MaxTileText+1)}
		return s, nil
	})
	if !errors.Is(e, ErrInvalidSettings) {
		t.Fatal(e)
	}
	if w = do("POST", "/api/repositories", `{"repository":"owner/other","settings":{"automatic":null,"tiles":{"pins":["nope"]}}}`); w.Code != http.StatusBadRequest {
		t.Fatal(w.Code, w.Body)
	}
}
