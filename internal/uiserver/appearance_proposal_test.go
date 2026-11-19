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
	target := (&Proposers{API: LocalAPI{Registry: registry}}).appearanceTarget(true)
	unchanged := func(any) error { return nil }

	for _, after := range []string{`{"dark_theme":"c-nope"}`, `{"light_theme":"c-nope"}`} {
		raw := json.RawMessage(after)
		if err := target.validate(ctx, raw); err == nil {
			t.Errorf("validate accepted %s", after)
		}
		if err := target.apply(ctx, raw, unchanged, agentcontext.SaveInput{}); err == nil {
			t.Errorf("apply accepted %s", after)
		}
	}
	if got, _ := registry.Preferences(ctx); got.Appearance.DarkTheme != "" || got.Appearance.LightTheme != "" {
		t.Fatalf("a refused proposal saved: %+v", got.Appearance)
	}

	good := json.RawMessage(`{"dark_theme":"c-dusk","light_theme":"default-dark"}`)
	if err := target.validate(ctx, good); err != nil {
		t.Fatal(err)
	}
	if err := target.apply(ctx, good, unchanged, agentcontext.SaveInput{}); err != nil {
		t.Fatal(err)
	}
	got, _ := registry.Preferences(ctx)
	if got.Appearance.DarkTheme != "c-dusk" || got.Appearance.LightTheme != "default-dark" {
		t.Fatalf("slots: %+v", got.Appearance)
	}
}
