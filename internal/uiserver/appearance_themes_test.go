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
	if got.LightTheme != "default-light" || got.DarkTheme != "default-dark" || len(got.Themes) != 2 ||
		!got.Themes[0].Builtin || !got.Themes[1].Builtin || got.Themes[0].ID != "default-light" || got.Themes[1].Base != "dark" {
		t.Fatalf("defaults: %#v", got)
	}

	// Create into the dark slot.
	got = call("POST", "/themes", `{"name":"  Dusk ","base":"dark","tokens":{"--surface-page":"#101820"},"slot":"dark"}`, 200)
	if len(got.Themes) != 3 || got.Themes[2].Builtin || got.Themes[2].Name != "Dusk" || got.Themes[2].Base != "dark" ||
		got.Themes[2].Tokens["--surface-page"] != "#101820" || !strings.HasPrefix(got.Themes[2].ID, "c-") {
		t.Fatalf("created: %#v", got.Themes)
	}
	dusk := got.Themes[2].ID
	if got.DarkTheme != dusk || got.LightTheme != "default-light" {
		t.Fatalf("slots: %q %q", got.LightTheme, got.DarkTheme)
	}
	// Created with no slot: the slots stay.
	got = call("POST", "/themes", `{"name":"Paper","base":"light","tokens":{}}`, 200)
	paper := got.Themes[3].ID
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
	if got.Themes[3].Name != "Newsprint" {
		t.Fatalf("renamed: %#v", got.Themes)
	}
	call("PATCH", "/themes/"+paper, `{"name":""}`, 400)
	call("PATCH", "/themes/default-light", `{"name":"x"}`, 400)
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
	if got := call("GET", "", "", 200); len(got.Themes) != 4 {
		t.Fatalf("refusals saved: %#v", got.Themes)
	}

	// Delete resets the slot that used the theme and leaves the other.
	got = call("DELETE", "/themes/"+dusk, "", 200)
	if len(got.Themes) != 3 || got.LightTheme != "default-light" || got.DarkTheme != "default-dark" {
		t.Fatalf("deleted: %#v", got)
	}
	call("DELETE", "/themes/"+dusk, "", 404)
	call("DELETE", "/themes/default-dark", "", 400)
	call("DELETE", "/themes/default-light", "", 400)
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
	if _, err := addTheme(p, repos.CustomTheme{ID: "default-dark", Name: "t", Base: "light"}, ""); err == nil {
		t.Fatal("built-in id accepted")
	}
}
