package agentdirs

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// isolate gives the test its own home, configuration folder and no
// CLAUDE_CONFIG_DIR, and returns the home.
func isolate(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv(Claude.Env, "")
	t.Setenv(Codex.Env, "")
	return home
}

func mkdir(t *testing.T, dir string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestNormalizeExpandsHomeAndRefusesRelativePaths(t *testing.T) {
	home := isolate(t)
	for given, want := range map[string]string{
		"~/.claude-work": filepath.Join(home, ".claude-work"),
		"~":              home,
		" " + filepath.Join(home, "a", "..", "b") + "/ ": filepath.Join(home, "b"),
	} {
		if got, err := Normalize(given); err != nil || got != want {
			t.Errorf("Normalize(%q) = %q, %v; want %q", given, got, err, want)
		}
	}
	for _, given := range []string{"", ".claude", "work/claude", "~other/claude", "/a\x00b"} {
		if got, err := Normalize(given); err == nil {
			t.Errorf("Normalize(%q) = %q, want an error", given, got)
		}
	}
}

func TestAllIsTheDefaultThenTheEnvironmentsThenTheRecorded(t *testing.T) {
	home := isolate(t)
	def := filepath.Join(home, ".claude")
	if got := Claude.All(""); !slices.Equal(got, []string{def}) {
		t.Fatalf("nothing set: %v", got)
	}
	work, personal := mkdir(t, filepath.Join(home, ".claude-work")), mkdir(t, filepath.Join(home, ".claude.personal"))
	if changed, err := Claude.Remember(personal); err != nil || !changed {
		t.Fatalf("Remember = %v, %v", changed, err)
	}
	t.Setenv(Claude.Env, work)
	if got := Claude.All(""); !slices.Equal(got, []string{def, work, personal}) {
		t.Fatalf("All = %v", got)
	}
	// A directory named twice is read once.
	t.Setenv(Claude.Env, personal)
	if got := Claude.All(""); !slices.Equal(got, []string{def, personal}) {
		t.Fatalf("the environment's is recorded too: %v", got)
	}
	if got, want := Claude.Under("", "projects"), []string{filepath.Join(def, "projects"), filepath.Join(personal, "projects")}; !slices.Equal(got, want) {
		t.Fatalf("Projects = %v", got)
	}
	// Another home replaces only the default.
	other := t.TempDir()
	if got := Claude.All(other); !slices.Equal(got, []string{filepath.Join(other, ".claude"), personal}) {
		t.Fatalf("Claude.All(other) = %v", got)
	}
	if Claude.Current() != personal {
		t.Fatalf("Current = %q", Claude.Current())
	}
	// A relative CLAUDE_CONFIG_DIR names no one directory.
	t.Setenv(Claude.Env, "relative")
	if Claude.FromEnv() != "" || Claude.Current() != def {
		t.Fatalf("relative: %q, %q", Claude.FromEnv(), Claude.Current())
	}
}

func TestRememberKeepsExistingOtherDirectoriesOnce(t *testing.T) {
	home := isolate(t)
	personal := mkdir(t, filepath.Join(home, ".claude.personal"))
	if changed, err := Claude.Remember(filepath.Join(home, ".claude")); err != nil || changed {
		t.Fatalf("the default directory: %v, %v", changed, err)
	}
	if _, err := Claude.Remember(filepath.Join(home, "missing")); err == nil {
		t.Fatal("a missing directory was remembered")
	}
	if _, err := Claude.Remember("relative"); err == nil {
		t.Fatal("a relative path was remembered")
	}
	if len(Claude.Recorded()) != 0 {
		t.Fatalf("recorded %v", Claude.Recorded())
	}
	for i, want := range []bool{true, false} {
		if changed, err := Claude.Remember("~/.claude.personal"); err != nil || changed != want {
			t.Fatalf("Remember %d = %v, %v", i, changed, err)
		}
	}
	if got := Claude.Recorded(); !slices.Equal(got, []string{personal}) {
		t.Fatalf("Recorded = %v", got)
	}
	file, err := Claude.RecordFile()
	if err != nil || file != filepath.Join(home, ".config", "baum", "claude-dirs.json") {
		t.Fatalf("RecordFile = %q, %v", file, err)
	}
	if info, err := os.Stat(file); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("record: %v, %v", info, err)
	}
	if changed, err := Claude.Forget(personal); err != nil || !changed {
		t.Fatalf("Forget = %v, %v", changed, err)
	}
	if changed, err := Claude.Forget(personal); err != nil || changed {
		t.Fatalf("Forget again = %v, %v", changed, err)
	}
	if len(Claude.Recorded()) != 0 {
		t.Fatalf("recorded %v", Claude.Recorded())
	}
}

func TestRememberDropsTheOldestBeyondTheBound(t *testing.T) {
	home := isolate(t)
	for i := range MaxRecorded + 2 {
		if _, err := Claude.Remember(mkdir(t, filepath.Join(home, fmt.Sprintf("claude-%02d", i)))); err != nil {
			t.Fatal(err)
		}
	}
	got := Claude.Recorded()
	if len(got) != MaxRecorded || got[0] != filepath.Join(home, "claude-02") || got[len(got)-1] != filepath.Join(home, fmt.Sprintf("claude-%02d", MaxRecorded+1)) {
		t.Fatalf("Recorded = %v", got)
	}
}

func TestAnUnreadableRecordIsEmptyAndReplaced(t *testing.T) {
	home := isolate(t)
	file, _ := Claude.RecordFile()
	mkdir(t, filepath.Dir(file))
	if err := os.WriteFile(file, []byte(`{"dirs":["relative","`+filepath.ToSlash(home)+`/a","`+filepath.ToSlash(home)+`/a"`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := Claude.Recorded(); len(got) != 0 {
		t.Fatalf("a cut-off record: %v", got)
	}
	personal := mkdir(t, filepath.Join(home, ".claude.personal"))
	if changed, err := Claude.Remember(personal); err != nil || !changed {
		t.Fatalf("Remember = %v, %v", changed, err)
	}
	if got := Claude.Recorded(); !slices.Equal(got, []string{personal}) {
		t.Fatalf("Recorded = %v", got)
	}
	// Entries that are not absolute paths, and repeats, are not read.
	if err := os.WriteFile(file, []byte(`{"dirs":["relative",`+fmt.Sprintf("%q,%q", personal, personal)+`]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := Claude.Recorded(); !slices.Equal(got, []string{personal}) {
		t.Fatalf("Recorded = %v", got)
	}
}

func TestCodexHomesAreKeptApartFromClaudeDirectories(t *testing.T) {
	home := isolate(t)
	def := filepath.Join(home, ".codex")
	work, personal := mkdir(t, filepath.Join(home, ".codex-work")), mkdir(t, filepath.Join(home, ".codex.personal"))
	if changed, err := Codex.Remember(def); err != nil || changed {
		t.Fatalf("the default home: %v, %v", changed, err)
	}
	if changed, err := Codex.Remember(personal); err != nil || !changed {
		t.Fatalf("Remember = %v, %v", changed, err)
	}
	t.Setenv(Codex.Env, work)
	if got := Codex.All(""); !slices.Equal(got, []string{def, work, personal}) {
		t.Fatalf("All = %v", got)
	}
	if got, want := Codex.Under("", "sessions"), []string{filepath.Join(def, "sessions"), filepath.Join(work, "sessions"), filepath.Join(personal, "sessions")}; !slices.Equal(got, want) {
		t.Fatalf("Under = %v", got)
	}
	if Codex.Current() != work {
		t.Fatalf("Current = %q", Codex.Current())
	}
	file, err := Codex.RecordFile()
	if err != nil || file != filepath.Join(home, ".config", "baum", "codex-dirs.json") {
		t.Fatalf("RecordFile = %q, %v", file, err)
	}
	// Each agent has its own record.
	if got := Claude.Recorded(); len(got) != 0 {
		t.Fatalf("Claude's record has %v", got)
	}
	if got := Claude.All(""); !slices.Equal(got, []string{filepath.Join(home, ".claude")}) {
		t.Fatalf("Claude.All = %v", got)
	}
}

func TestChooseChecksTheDirectoryAndItsSignIn(t *testing.T) {
	home := isolate(t)
	work := mkdir(t, filepath.Join(home, ".codex-work"))
	signedOut := func(context.Context, string) (bool, bool) { return false, true }
	if _, err := Codex.Choose(t.Context(), work, signedOut); err == nil || !strings.Contains(err.Error(), "CODEX_HOME="+work+" codex login") {
		t.Fatalf("signed out: %v", err)
	}
	if _, err := Codex.Choose(t.Context(), filepath.Join(home, "missing"), nil); err == nil || !strings.HasPrefix(err.Error(), "Codex home:") {
		t.Fatalf("missing: %v", err)
	}
	if _, err := Claude.Choose(t.Context(), work, signedOut); err == nil || !strings.Contains(err.Error(), "CLAUDE_CONFIG_DIR="+work+" claude") {
		t.Fatalf("Claude signed out: %v", err)
	}
	if len(Codex.Recorded()) != 0 || len(Claude.Recorded()) != 0 {
		t.Fatal("a refused directory was remembered")
	}
	if dir, err := Codex.Choose(t.Context(), "~/.codex-work", nil); err != nil || dir != work {
		t.Fatalf("Choose = %q, %v", dir, err)
	}
	if dir, err := Codex.Choose(t.Context(), " ", nil); err != nil || dir != "" {
		t.Fatalf("empty: %q, %v", dir, err)
	}
	if got := Codex.Recorded(); !slices.Equal(got, []string{work}) || len(Claude.Recorded()) != 0 {
		t.Fatalf("Codex %v, Claude %v", got, Claude.Recorded())
	}
}
