package repos

import (
	"slices"
	"strings"
	"testing"

	"github.com/baum-ai/baum/internal/config"
)

// The Codex home follows the Claude Code directory's rules: reviews and
// extraction take the reviews' home; a fix takes it too unless it has its own
// agent, which then has its own home or none. Neither agent's choice reaches
// the other's field.
func TestPreferencesCodexHome(t *testing.T) {
	claude, codex := config.BackendClaude, config.BackendCodex
	work, personal := "/home/dev/.codex-work", "/home/dev/.codex.personal"
	claudeDir := "/home/dev/.claude.personal"
	empty := ""
	global := config.Config{Backend: config.BackendCodex}

	if got := (Preferences{}).Resolve(global); got.CodexHome != "" {
		t.Fatalf("no choice: %q", got.CodexHome)
	}
	p := Preferences{CodexHome: &work, ClaudeConfigDir: &claudeDir}
	review := p.Resolve(global)
	if review.CodexHome != work || review.ClaudeConfigDir != claudeDir {
		t.Fatalf("reviews: %+v", review)
	}
	if got := p.ResolveExtraction(review); got.CodexHome != work {
		t.Fatalf("extraction: %q", got.CodexHome)
	}
	if got := p.ResolveFix(review); got.CodexHome != work {
		t.Fatalf("a fix that follows reviews: %q", got.CodexHome)
	}
	p.FixCodexHome = &personal
	if err := p.Validate(); err == nil || !strings.Contains(err.Error(), "choose the agent for fixes") {
		t.Fatalf("a fix home without a fix agent: %v", err)
	}
	p.FixBackend = &codex
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	if got := p.ResolveFix(review); got.CodexHome != personal || got.ClaudeConfigDir != "" {
		t.Fatalf("the fix agent's own: %+v", got)
	}
	for _, dir := range []*string{nil, &empty} {
		p.FixCodexHome = dir
		if got := p.ResolveFix(review); got.CodexHome != "" {
			t.Fatalf("a fix agent without a home: %q", got.CodexHome)
		}
	}
	// A fix with Claude Code drops the reviews' Codex home.
	p.FixBackend, p.FixCodexHome = &claude, nil
	if got := p.ResolveFix(review); got.CodexHome != "" {
		t.Fatalf("a Claude Code fix: %q", got.CodexHome)
	}

	for _, bad := range []string{"relative", "~/.codex-work", work + "/../x", work + "/"} {
		if err := (Preferences{CodexHome: &bad}).Validate(); err == nil || !strings.Contains(err.Error(), "Codex home") {
			t.Errorf("%q accepted: %v", bad, err)
		}
		if err := (Preferences{FixBackend: &codex, FixCodexHome: &bad}).Validate(); err == nil {
			t.Errorf("fix %q accepted", bad)
		}
	}

	// Extraction with its own agent has its own home or none.
	own := Preferences{CodexHome: &work, Decisions: DecisionPreferences{Backend: &codex, CodexHome: &personal}}
	if err := own.Validate(); err != nil {
		t.Fatal(err)
	}
	if got := own.ResolveExtraction(own.Resolve(global)).CodexHome; got != personal {
		t.Fatalf("extraction's own: %q", got)
	}
	own.Decisions.CodexHome = nil
	if got := own.ResolveExtraction(own.Resolve(global)).CodexHome; got != "" {
		t.Fatalf("extraction's own agent without a home: %q", got)
	}
	if err := (Preferences{Decisions: DecisionPreferences{CodexHome: &work}}).Validate(); err == nil || !strings.Contains(err.Error(), "data extraction") {
		t.Fatalf("a home without an agent: %v", err)
	}
}

// The homes whose plans are worth showing: those of the runs that use Codex,
// once each.
func TestPreferencesCodexHomesAreThoseCodexRunsWith(t *testing.T) {
	claude, codex := config.BackendClaude, config.BackendCodex
	work, personal := "/home/dev/.codex-work", "/home/dev/.codex.personal"
	off := false
	global := config.Config{Backend: config.BackendAuto}
	for name, c := range map[string]struct {
		prefs Preferences
		want  []string
	}{
		"none":                     {Preferences{}, nil},
		"auto runs Claude Code":    {Preferences{CodexHome: &work}, nil},
		"reviews, fixes follow":    {Preferences{Backend: &codex, CodexHome: &work}, []string{work}},
		"both":                     {Preferences{Backend: &codex, CodexHome: &work, FixBackend: &codex, FixCodexHome: &personal}, []string{work, personal}},
		"reviews with claude":      {Preferences{Backend: &claude, CodexHome: &work, FixBackend: &codex, FixCodexHome: &personal}, []string{personal}},
		"extraction's own":         {Preferences{Backend: &claude, Decisions: DecisionPreferences{Backend: &codex, CodexHome: &personal}}, []string{personal}},
		"extraction's own but off": {Preferences{Backend: &claude, Decisions: DecisionPreferences{Enabled: &off, Backend: &codex, CodexHome: &personal}}, nil},
	} {
		if got := c.prefs.CodexHomes(global); !slices.Equal(got, c.want) {
			t.Errorf("%s: %v, want %v", name, got, c.want)
		}
		if got := c.prefs.ClaudeConfigDirs(global); len(got) != 0 {
			t.Errorf("%s: Claude Code directories %v", name, got)
		}
	}
}
