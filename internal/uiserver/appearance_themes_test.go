package uiserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/baum-ai/baum/internal/appdb/appdbtest"
	"github.com/baum-ai/baum/internal/repos"
)

func themesTestServer(t *testing.T) func(method, path, body string, code int) appearanceSettings {
	t.Helper()
	registry, err := repos.Open(filepath.Join(appdbtest.TempDir(t), "repos.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	handler := LocalAPI{Registry: registry}.Handler()
	server := httptest.NewServer(handler)
	t.Cleanup(func() { server.Close(); handler.Close(); _ = registry.Close() })
	return func(method, path, body string, code int) appearanceSettings {
		t.Helper()
		req, err := http.NewRequest(method, server.URL+"/api/settings/appearance"+path, strings.NewReader(body))
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
			t.Fatalf("%s %s %s: got %d, want %d", method, path, body, response.StatusCode, code)
		}
		var got appearanceSettings
		if code == http.StatusOK {
			if err := json.NewDecoder(response.Body).Decode(&got); err != nil {
				t.Fatal(err)
			}
		}
		return got
	}
}

func TestAppearanceThemes(t *testing.T) {
	call := themesTestServer(t)
	got := call("GET", "", "", 200)
	if got.LightTheme != "default-light" || got.DarkTheme != "default-dark" || len(got.Themes) != 6 {
		t.Fatalf("defaults: %#v", got)
	}
	for i, want := range []struct{ id, name, base string }{
		{"default-light", "Default light", "light"}, {"default-dark", "Default dark", "dark"},
		{"birch", "Birch", "light"}, {"parchment", "Parchment", "light"},
		{"pine", "Pine", "dark"}, {"ember", "Ember", "dark"},
	} {
		if g := got.Themes[i]; g.ID != want.id || g.Name != want.name || g.Base != want.base || !g.Builtin {
			t.Fatalf("built-in %d: %#v, want %v", i, g, want)
		}
	}
	// The tokens the app applies come from here: the built-ins carry them.
	if got.Themes[0].Tokens == nil || len(got.Themes[0].Tokens) != 0 || got.Themes[4].Tokens["--color-paper"] != "#dce9df" ||
		got.Themes[5].Tokens["--surface-page"] != "#1c1a19" {
		t.Fatalf("built-in tokens: %#v", got.Themes)
	}
	// Either slot takes any built-in.
	got = call("PUT", "", `{"light_theme":"pine","dark_theme":"birch"}`, 200)
	if got.LightTheme != "pine" || got.DarkTheme != "birch" {
		t.Fatalf("built-in slots: %q %q", got.LightTheme, got.DarkTheme)
	}
	got = call("PUT", "", `{"light_theme":"","dark_theme":""}`, 200)
	if got.LightTheme != "default-light" || got.DarkTheme != "default-dark" {
		t.Fatalf("reset slots: %q %q", got.LightTheme, got.DarkTheme)
	}
	// None can be renamed, deleted or taken as the id of a new theme.
	for _, id := range []string{"default-light", "default-dark", "birch", "parchment", "pine", "ember"} {
		call("PATCH", "/themes/"+id, `{"name":"x"}`, 400)
		call("DELETE", "/themes/"+id, "", 400)
	}

	// Create into the dark slot.
	got = call("POST", "/themes", `{"name":"  Dusk ","base":"dark","tokens":{"--surface-page":"#101820"},"slot":"dark"}`, 200)
	if len(got.Themes) != 7 || got.Themes[6].Builtin || got.Themes[6].Name != "Dusk" || got.Themes[6].Base != "dark" ||
		got.Themes[6].Tokens["--surface-page"] != "#101820" || !strings.HasPrefix(got.Themes[6].ID, "c-") {
		t.Fatalf("created: %#v", got.Themes)
	}
	dusk := got.Themes[6].ID
	if got.DarkTheme != dusk || got.LightTheme != "default-light" {
		t.Fatalf("slots: %q %q", got.LightTheme, got.DarkTheme)
	}
	// Created with no slot: the slots stay.
	got = call("POST", "/themes", `{"name":"Paper","base":"light","tokens":{}}`, 200)
	paper := got.Themes[7].ID
	if paper == dusk || got.DarkTheme != dusk || got.LightTheme != "default-light" {
		t.Fatalf("second theme: %#v", got)
	}
	// Any theme may sit in either slot; a built-in too.
	got = call("PUT", "", `{"light_theme":"`+dusk+`","dark_theme":"default-light"}`, 200)
	if got.LightTheme != dusk || got.DarkTheme != "default-light" {
		t.Fatalf("slots: %#v", got)
	}
	got = call("PUT", "", `{"dark_theme":""}`, 200)
	if got.DarkTheme != "default-dark" || got.LightTheme != dusk {
		t.Fatalf("empty slot: %#v", got)
	}
	call("PUT", "", `{"light_theme":"c-nope"}`, 400)
	call("PUT", "", `{"dark_theme":"c-nope"}`, 400)

	// Rename.
	got = call("PATCH", "/themes/"+paper, `{"name":"Newsprint"}`, 200)
	if got.Themes[7].Name != "Newsprint" {
		t.Fatalf("renamed: %#v", got.Themes)
	}
	call("PATCH", "/themes/"+paper, `{"name":""}`, 400)
	call("PATCH", "/themes/c-nope", `{"name":"x"}`, 404)
	call("PATCH", "/themes/"+paper, `{"name":"x","extra":1}`, 400)

	// Refusals write nothing.
	for _, body := range []string{
		`{"name":"x","base":"sepia"}`,
		`{"name":"","base":"light"}`,
		`{"name":"x","base":"light","tokens":{"--surface-page":"red;}"}}`,
		`{"name":"x","base":"light","tokens":{"--font-text":"#fff"}}`,
		`{"name":"x","base":"light","slot":"both"}`,
		`{"name":"x","base":"light","id":"c-mine"}`,
		`{"name":"x","base":"light"} {}`,
		`{"name":"` + strings.Repeat("x", 17<<10) + `","base":"light"}`,
	} {
		call("POST", "/themes", body, 400)
	}
	if got := call("GET", "", "", 200); len(got.Themes) != 8 {
		t.Fatalf("refusals saved: %#v", got.Themes)
	}

	// Delete resets the slot that used the theme and leaves the other.
	got = call("DELETE", "/themes/"+dusk, "", 200)
	if len(got.Themes) != 7 || got.LightTheme != "default-light" || got.DarkTheme != "default-dark" {
		t.Fatalf("deleted: %#v", got)
	}
	call("DELETE", "/themes/"+dusk, "", 404)
}

func TestAppearanceThemeStoresTrimmedColours(t *testing.T) {
	call := themesTestServer(t)
	got := call("POST", "/themes", `{"name":"Trim","base":"light","tokens":{"--surface-page":" \t#101820\n"}}`, 200)
	if v := got.Themes[6].Tokens["--surface-page"]; v != "#101820" {
		t.Fatalf("stored %q", v)
	}
	call("POST", "/themes", `{"name":"Nbsp","base":"light","tokens":{"--surface-page":" #101820"}}`, 400)
}

func TestAppearanceThemeDanglingSlotFallsBack(t *testing.T) {
	prefs := repos.Preferences{Appearance: repos.AppearancePreferences{LightTheme: "c-gone", DarkTheme: "default-light"}}
	got := appearanceOf(prefs)
	if got.LightTheme != "default-light" || got.DarkTheme != "default-light" {
		t.Fatalf("slots: %q %q", got.LightTheme, got.DarkTheme)
	}
}

func TestAddThemeCap(t *testing.T) {
	var p repos.Preferences
	for i := 0; i < 32; i++ {
		var err error
		if p, err = addTheme(p, repos.CustomTheme{ID: "c-" + strings.Repeat("a", i+1), Name: "t", Base: "light"}, ""); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := addTheme(p, repos.CustomTheme{ID: "c-more", Name: "t", Base: "light"}, ""); err == nil {
		t.Fatal("33rd theme accepted")
	}
	for _, id := range []string{"default-dark", "pine", "ember"} {
		if _, err := addTheme(p, repos.CustomTheme{ID: id, Name: "t", Base: "light"}, ""); err == nil {
			t.Fatalf("built-in id %s accepted", id)
		}
	}
}
