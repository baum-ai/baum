package repos

import (
	"context"
	"errors"
	"testing"

	"github.com/baum-ai/baum/agent/prompt"
)

var errRefused = errors.New("refused")

// The If variants hand the edit the stored state as the writing transaction
// reads it; an error the edit returns writes nothing.
func TestUpdateIfVariantsWriteNothingWhenRefused(t *testing.T) {
	ctx := context.Background()
	r := registry(t)
	if _, e := r.Pick(ctx, "owner/repo", nil); e != nil {
		t.Fatal(e)
	}
	if _, e := r.UpdatePreferences(ctx, func(p Preferences) Preferences { p.JobConcurrency = 2; return p }); e != nil {
		t.Fatal(e)
	}
	if _, e := r.UpdatePreferencesIf(ctx, func(p Preferences) (Preferences, error) {
		if p.JobConcurrency != 2 {
			t.Errorf("the edit read %d", p.JobConcurrency)
		}
		p.JobConcurrency = 3
		return p, errRefused
	}); !errors.Is(e, errRefused) {
		t.Fatal(e)
	}
	if p, _ := r.Preferences(ctx); p.JobConcurrency != 2 {
		t.Errorf("a refused edit wrote %d", p.JobConcurrency)
	}
	if p, e := r.UpdatePreferencesIf(ctx, func(p Preferences) (Preferences, error) { p.JobConcurrency = 3; return p, nil }); e != nil || p.JobConcurrency != 3 {
		t.Fatal(p, e)
	}

	off := false
	if _, e := r.UpdateRepositoryIf(ctx, "owner/repo", func(s Settings) (Settings, error) { s.Automatic = &off; return s, errRefused }); !errors.Is(e, errRefused) {
		t.Fatal(e)
	}
	if s, _, _ := r.RepositorySettings(ctx, "owner/repo"); s.Automatic != nil {
		t.Errorf("a refused edit wrote %v", *s.Automatic)
	}
	if _, e := r.UpdateRepositoryIf(ctx, "Owner/Repo", func(s Settings) (Settings, error) { s.Automatic = &off; return s, nil }); e != nil {
		t.Fatal(e)
	}
	if s, _, _ := r.RepositorySettings(ctx, "owner/repo"); s.Automatic == nil || *s.Automatic {
		t.Errorf("settings %+v", s)
	}
	if _, e := r.UpdateRepositoryIf(ctx, "other/repo", func(s Settings) (Settings, error) { return s, nil }); !errors.Is(e, ErrNotFound) {
		t.Errorf("an unpicked repository: %v", e)
	}
}

func TestPutPromptOverrideIf(t *testing.T) {
	ctx := context.Background()
	r := registry(t)
	if _, e := r.Pick(ctx, "owner/repo", nil); e != nil {
		t.Fatal(e)
	}
	if _, e := r.SetPromptOverride(ctx, GlobalPrompts, prompt.IDFix, "global"); e != nil {
		t.Fatal(e)
	}
	text := "repository text"
	var seen PromptState
	if e := r.PutPromptOverrideIf(ctx, "owner/repo", prompt.IDFix, &text, func(s PromptState) error { seen = s; return errRefused }); !errors.Is(e, errRefused) {
		t.Fatal(e)
	}
	if seen.Own != nil || seen.Global == nil || seen.Global.Text != "global" {
		t.Errorf("the check read %+v", seen)
	}
	if own, _ := r.PromptOverrides(ctx, "owner/repo"); len(own) != 0 {
		t.Errorf("a refused put wrote %+v", own)
	}
	if e := r.PutPromptOverrideIf(ctx, "owner/repo", prompt.IDFix, &text, func(PromptState) error { return nil }); e != nil {
		t.Fatal(e)
	}
	state, e := r.PromptState(ctx, "owner/repo", prompt.IDFix)
	if e != nil || state.Own == nil || state.Own.Text != text || state.Global == nil {
		t.Fatalf("%+v %v", state, e)
	}
	if e := r.PutPromptOverrideIf(ctx, "owner/repo", prompt.IDFix, nil, func(PromptState) error { return errRefused }); !errors.Is(e, errRefused) {
		t.Fatal(e)
	}
	if e := r.PutPromptOverrideIf(ctx, "owner/repo", prompt.IDFix, nil, nil); e != nil {
		t.Fatal(e)
	}
	if state, _ := r.PromptState(ctx, "owner/repo", prompt.IDFix); state.Own != nil {
		t.Errorf("not reset: %+v", state)
	}
	// The global scope has no global row above it.
	if state, _ := r.PromptState(ctx, GlobalPrompts, prompt.IDFix); state.Own == nil || state.Global != nil {
		t.Errorf("global: %+v", state)
	}
	// Validation and the picked check come as with SetPromptOverride.
	blank := " "
	if e := r.PutPromptOverrideIf(ctx, GlobalPrompts, prompt.IDFix, &blank, nil); !errors.Is(e, ErrInvalidPrompt) {
		t.Errorf("a blank text: %v", e)
	}
	if e := r.PutPromptOverrideIf(ctx, "other/repo", prompt.IDFix, &text, nil); !errors.Is(e, ErrNotPicked) {
		t.Errorf("an unpicked repository: %v", e)
	}
	if _, e := r.PromptState(ctx, "other/repo", prompt.IDFix); !errors.Is(e, ErrNotPicked) {
		t.Errorf("an unpicked repository's state: %v", e)
	}
}
