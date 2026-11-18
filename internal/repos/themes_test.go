package repos

import (
	"fmt"
	"strings"
	"testing"
)

func TestPreferencesValidateThemes(t *testing.T) {
	ok := CustomTheme{ID: "c-1", Name: "Dusk", Base: "dark", Tokens: map[string]string{"--surface-page": "#101820"}}
	with := func(list ...CustomTheme) Preferences {
		return Preferences{Appearance: AppearancePreferences{Themes: list}}
	}
	if err := with(ok).Validate(); err != nil {
		t.Fatal(err)
	}
	many := make([]CustomTheme, 32)
	for i := range many {
		many[i] = CustomTheme{ID: fmt.Sprint("c-", i), Name: "t", Base: "light"}
	}
	if err := with(many...).Validate(); err != nil {
		t.Fatalf("32 themes: %v", err)
	}
	if err := with(append(many, ok)...).Validate(); err == nil {
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
		if err := with(theme).Validate(); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	if err := with(ok, ok).Validate(); err == nil {
		t.Error("repeated id accepted")
	}
	// Unknown slot ids are not refused: they fall back to the default.
	p := with(ok)
	p.Appearance.LightTheme = "c-gone"
	if err := p.Validate(); err != nil {
		t.Errorf("dangling slot: %v", err)
	}
}
