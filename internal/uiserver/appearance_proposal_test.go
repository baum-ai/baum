package uiserver

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/baum-ai/baum/internal/agentcontext"
	"github.com/baum-ai/baum/internal/appdb/appdbtest"
	"github.com/baum-ai/baum/internal/repos"
)

// The appearance proposal refuses a theme slot naming an unknown theme, as
// PUT /api/settings/appearance does, when it is validated and when it is saved.
func TestAppearanceProposalChecksThemeSlots(t *testing.T) {
	registry, err := repos.Open(filepath.Join(appdbtest.TempDir(t), "repos.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = registry.Close() })
	ctx := context.Background()
	prefs, err := (LocalAPI{Registry: registry}).updatePreferences(ctx, nil, func(p repos.Preferences) repos.Preferences {
		p, _ = addTheme(p, repos.CustomTheme{ID: "c-dusk", Name: "Dusk", Base: "dark"}, "")
		return p
	})
	if err != nil || len(prefs.Appearance.Themes) != 1 {
		t.Fatalf("seed: %v", err)
	}
	target := (&Proposers{API: LocalAPI{Registry: registry}}).themeChoiceTarget()
	unchanged := func(any) error { return nil }

	for _, after := range []string{`{"theme_id":"c-nope","slot":"dark"}`, `{"theme_id":"c-nope","slot":"light"}`, `{"theme_id":"c-nope"}`} {
		raw := json.RawMessage(after)
		if err := target.validate(ctx, raw); err == nil {
			t.Errorf("validate accepted %s", after)
		}
		if err := target.apply(ctx, raw, unchanged, agentcontext.SaveInput{Theme: &agentcontext.ThemeSave{Slot: "dark"}}); err == nil {
			t.Errorf("apply accepted %s", after)
		}
	}
	if got, _ := registry.Preferences(ctx); got.Appearance.DarkTheme != "" || got.Appearance.LightTheme != "" {
		t.Fatalf("a refused proposal saved: %+v", got.Appearance)
	}

	good := json.RawMessage(`{"theme_id":"c-dusk","slot":"dark"}`)
	if err := target.validate(ctx, good); err != nil {
		t.Fatal(err)
	}
	if err := target.apply(ctx, good, unchanged, agentcontext.SaveInput{}); err != nil {
		t.Fatal(err)
	}
	// A slot in use is the app's to resolve: saved with none, it is refused.
	inUse := json.RawMessage(`{"theme_id":"default-dark","slot":"in_use"}`)
	if err := target.apply(ctx, inUse, unchanged, agentcontext.SaveInput{}); err == nil {
		t.Error("saved a slot in use without the app's slot")
	}
	if err := target.apply(ctx, inUse, unchanged, agentcontext.SaveInput{Theme: &agentcontext.ThemeSave{Slot: "light"}}); err != nil {
		t.Fatal(err)
	}
	got, _ := registry.Preferences(ctx)
	if got.Appearance.DarkTheme != "c-dusk" || got.Appearance.LightTheme != "default-dark" {
		t.Fatalf("slots: %+v", got.Appearance)
	}
}
