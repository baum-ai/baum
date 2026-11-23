package repos

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/baum-ai/baum/internal/config"
)

// Reviews and extraction take the reviews' directory; a fix takes it too
// unless it has its own agent, which then has its own directory or none.
func TestPreferencesClaudeConfigDirectory(t *testing.T) {
	claude, codex := config.BackendClaude, config.BackendCodex
	personal, work := filepath.Join(t.TempDir(), ".claude.personal"), filepath.Join(t.TempDir(), ".claude-work")
	empty := ""
	global := config.Config{Backend: config.BackendClaude, Model: "m"}

	if got := (Preferences{}).Resolve(global); got.ClaudeConfigDir != "" {
		t.Fatalf("no choice: %q", got.ClaudeConfigDir)
	}
	p := Preferences{ClaudeConfigDir: &personal}
	review := p.Resolve(global)
	if review.ClaudeConfigDir != personal {
		t.Fatalf("reviews: %q", review.ClaudeConfigDir)
	}
	if got := p.ResolveExtraction(review); got.ClaudeConfigDir != personal {
		t.Fatalf("extraction: %q", got.ClaudeConfigDir)
	}
	if got := p.ResolveFix(review); got.ClaudeConfigDir != personal {
		t.Fatalf("a fix that follows reviews: %q", got.ClaudeConfigDir)
	}
	// A fix directory without a fix agent is not a choice.
	p.FixClaudeConfigDir = &work
	if err := p.Validate(); err == nil || !strings.Contains(err.Error(), "choose the agent for fixes") {
		t.Fatalf("a fix directory without a fix agent: %v", err)
	}
	if got := p.ResolveFix(review); got.ClaudeConfigDir != personal {
		t.Fatalf("a fix directory without a fix agent: %q", got.ClaudeConfigDir)
	}
	p.FixBackend = &claude
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	if got := p.ResolveFix(review); got.ClaudeConfigDir != work {
		t.Fatalf("the fix agent's own: %q", got.ClaudeConfigDir)
	}
	// Its own agent without a directory uses the environment's, not reviews'.
	for _, dir := range []*string{nil, &empty} {
		p.FixClaudeConfigDir = dir
		if err := p.Validate(); err != nil {
			t.Fatal(err)
		}
		if got := p.ResolveFix(review); got.ClaudeConfigDir != "" {
			t.Fatalf("a fix agent without a directory: %q", got.ClaudeConfigDir)
		}
	}
	// Another agent keeps the stored directory and ignores it.
	p = Preferences{Backend: &codex, ClaudeConfigDir: &personal}
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}

	for _, bad := range []string{"relative", "~/.claude.personal", personal + "/../x", personal + "/"} {
		if err := (Preferences{ClaudeConfigDir: &bad}).Validate(); err == nil {
			t.Errorf("%q accepted", bad)
		}
		if err := (Preferences{FixBackend: &claude, FixClaudeConfigDir: &bad}).Validate(); err == nil {
			t.Errorf("fix %q accepted", bad)
		}
	}
}

// The directories whose plans are worth showing: those of the runs that use
// Claude Code, once each.
func TestPreferencesClaudeConfigDirsAreThoseClaudeCodeRunsWith(t *testing.T) {
	claude, codex := config.BackendClaude, config.BackendCodex
	personal, work := "/home/dev/.claude.personal", "/home/dev/.claude-work"
	global := config.Config{Backend: config.BackendAuto}
	for name, c := range map[string]struct {
		prefs Preferences
		want  []string
	}{
		"none":                      {Preferences{}, nil},
		"reviews, fixes follow":     {Preferences{ClaudeConfigDir: &personal}, []string{personal}},
		"both":                      {Preferences{ClaudeConfigDir: &personal, FixBackend: &claude, FixClaudeConfigDir: &work}, []string{personal, work}},
		"the same twice":            {Preferences{ClaudeConfigDir: &personal, FixBackend: &claude, FixClaudeConfigDir: &personal}, []string{personal}},
		"reviews with codex":        {Preferences{Backend: &codex, ClaudeConfigDir: &personal, FixBackend: &claude, FixClaudeConfigDir: &work}, []string{work}},
		"fixes with codex":          {Preferences{ClaudeConfigDir: &personal, FixBackend: &codex}, []string{personal}},
		"fixes without a fix agent": {Preferences{Backend: &claude, FixClaudeConfigDir: &work}, nil},
	} {
		if got := c.prefs.ClaudeConfigDirs(global); !slices.Equal(got, c.want) {
			t.Errorf("%s: %v, want %v", name, got, c.want)
		}
	}
}

// Extraction with an agent of its own runs with its own directory, or none;
// without one it follows the reviews'. Its directory's plan shows only while
// extraction is on.
func TestPreferencesExtractionClaudeConfigDirectory(t *testing.T) {
	claude := config.BackendClaude
	personal, work := "/home/dev/.claude.personal", "/home/dev/.claude-work"
	off := false
	global := config.Config{Backend: config.BackendClaude}
	follows := Preferences{ClaudeConfigDir: &personal}
	if got := follows.ResolveExtraction(follows.Resolve(global)).ClaudeConfigDir; got != personal {
		t.Fatalf("following reviews: %q", got)
	}
	own := Preferences{ClaudeConfigDir: &personal, Decisions: DecisionPreferences{Backend: &claude, ClaudeConfigDir: &work}}
	if err := own.Validate(); err != nil {
		t.Fatal(err)
	}
	if got := own.ResolveExtraction(own.Resolve(global)).ClaudeConfigDir; got != work {
		t.Fatalf("its own: %q", got)
	}
	if got := own.ClaudeConfigDirs(global); !slices.Equal(got, []string{personal, work}) {
		t.Fatalf("plans: %v", got)
	}
	own.Decisions.Enabled = &off
	if got := own.ClaudeConfigDirs(global); !slices.Equal(got, []string{personal}) {
		t.Fatalf("plans while extraction is off: %v", got)
	}
	none := Preferences{ClaudeConfigDir: &personal, Decisions: DecisionPreferences{Backend: &claude}}
	if got := none.ResolveExtraction(none.Resolve(global)).ClaudeConfigDir; got != "" {
		t.Fatalf("its own agent without a directory: %q", got)
	}
	if err := (Preferences{Decisions: DecisionPreferences{ClaudeConfigDir: &work}}).Validate(); err == nil || !strings.Contains(err.Error(), "data extraction") {
		t.Fatalf("a directory without an agent: %v", err)
	}
}
