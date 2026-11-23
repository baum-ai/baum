package automatic

import (
	"context"
	"errors"
	"testing"

	"github.com/baum-ai/baum/internal/repos"
)

// SavePreferences is PUT /api/automatic's save: a refused check or input
// saves nothing.
func TestSavePreferences(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	off, all := false, "all"
	refused := errors.New("refused")
	if err := h.scheduler.SavePreferences(ctx, PreferencesInput{Enabled: &off}, func(repos.Preferences) error { return refused }); !errors.Is(err, refused) {
		t.Fatal(err)
	}
	unknown := "gpt"
	if err := h.scheduler.SavePreferences(ctx, PreferencesInput{Enabled: &off, Backend: &unknown}, nil); err == nil {
		t.Fatal("an unknown backend was saved")
	}
	if p, _ := h.registry.Preferences(ctx); !p.Automatic || p.PullRequests != "" {
		t.Fatalf("a refused save changed %+v", p)
	}
	if err := h.scheduler.SavePreferences(ctx, PreferencesInput{Enabled: &off, PullRequests: &all}, func(repos.Preferences) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if p, _ := h.registry.Preferences(ctx); p.Automatic || p.PullRequests != "all" {
		t.Fatalf("saved %+v", p)
	}
}
