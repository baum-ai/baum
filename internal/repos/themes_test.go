package repos

import (
	"fmt"
	"slices"
	"strings"
	"testing"
)

func TestPreferencesValidateThemes(t *testing.T) {
	ok := CustomTheme{ID: "c-1", Name: "Dusk", Base: "dark", Tokens: map[string]string{"--surface-page": "#101820"}}
	with := func(list ...CustomTheme) Preferences {
		return Preferences{Appearance: AppearancePreferences{Themes: list}}
	}
	if err := with(ok).ValidateChange(Preferences{}); err != nil {
		t.Fatal(err)
	}
	many := make([]CustomTheme, 32)
	for i := range many {
		many[i] = CustomTheme{ID: fmt.Sprint("c-", i), Name: "t", Base: "light"}
	}
	if err := with(many...).ValidateChange(Preferences{}); err != nil {
		t.Fatalf("32 themes: %v", err)
	}
	if err := with(append(many, ok)...).ValidateChange(Preferences{}); err == nil {
		t.Error("33 themes accepted")
	}
	bad := map[string]CustomTheme{
		"base":     {ID: "c-2", Name: "x", Base: "sepia"},
		"name":     {ID: "c-2", Name: " ", Base: "light"},
		"token":    {ID: "c-2", Name: "x", Base: "light", Tokens: map[string]string{"--nope": "#fff"}},
		"injected": {ID: "c-2", Name: "x", Base: "light", Tokens: map[string]string{"--surface-page": "red;}"}},
		"no id":    {Name: "x", Base: "light"},
		"long id":  {ID: strings.Repeat("a", 65), Name: "x", Base: "light"},
		"built-in": {ID: "default-dark", Name: "x", Base: "light"},
	}
	for name, theme := range bad {
		if err := with(theme).ValidateChange(Preferences{}); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	if err := with(ok, ok).ValidateChange(Preferences{}); err == nil {
		t.Error("repeated id accepted")
	}
	// A stored theme that no longer validates does not block saving another
	// preference; changing it, or adding beside it with a repeated id, does.
	stale := CustomTheme{ID: "c-old", Name: "Old", Base: "dark", Tokens: map[string]string{"--removed-token": "#fff"}}
	prev := with(stale)
	next := with(stale)
	next.Automatic = !prev.Automatic
	if err := next.ValidateChange(prev); err != nil {
		t.Errorf("unrelated save: %v", err)
	}
	renamed := stale
	renamed.Name = "Renamed"
	if err := with(renamed).ValidateChange(prev); err == nil {
		t.Error("changed invalid theme accepted")
	}
	if err := with(stale, CustomTheme{ID: "c-new", Name: "x", Base: "light"}).ValidateChange(prev); err != nil {
		t.Errorf("adding beside a stale theme: %v", err)
	}
	if err := with(stale, CustomTheme{ID: "c-old", Name: "x", Base: "light"}).ValidateChange(prev); err == nil {
		t.Error("repeated id accepted")
	}
	// Over the cap already: a save that does not grow the list passes.
	var over []CustomTheme
	for i := 0; i < 34; i++ {
		over = append(over, CustomTheme{ID: fmt.Sprint("c-", i), Name: "t", Base: "light"})
	}
	if err := with(over...).ValidateChange(with(over...)); err != nil {
		t.Errorf("over the cap, unchanged: %v", err)
	}
	if err := with(append(slices.Clone(over), CustomTheme{ID: "c-x", Name: "t", Base: "light"})...).ValidateChange(with(over...)); err == nil {
		t.Error("growth past the cap accepted")
	}
	// Unknown slot ids are not refused: they fall back to the default.
	p := with(ok)
	p.Appearance.LightTheme = "c-gone"
	if err := p.ValidateChange(Preferences{}); err != nil {
		t.Errorf("dangling slot: %v", err)
	}
}
