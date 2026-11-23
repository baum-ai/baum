package collector

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/baum-ai/baum/internal/agentdirs"
	"github.com/baum-ai/baum/sessions/collector/claudecode"
	"github.com/baum-ai/baum/sessions/vendors"
)

// Without an override, the Claude Code stores are the projects folder of
// every configuration directory baum knows: ~/.claude, CLAUDE_CONFIG_DIR and
// the recorded ones.
func TestFindStoresReadsEveryKnownClaudeConfigDirectory(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	work, personal := filepath.Join(home, ".claude-work"), filepath.Join(home, ".claude.personal")
	t.Setenv(agentdirs.Claude.Env, work)
	want := []string{
		filepath.Join(home, ".claude", "projects", "-repo", "default.jsonl"),
		filepath.Join(work, "projects", "-repo", "work.jsonl"),
		filepath.Join(personal, "projects", "-repo", "personal.jsonl"),
	}
	for _, path := range want {
		write(t, path, `{"type":"user"}`)
	}
	options := StoreOptions{Sources: []vendors.Slug{vendors.ClaudeCode}}
	stores, err := FindStores(options)
	if err != nil {
		t.Fatal(err)
	}
	got := paths(stores, vendors.ClaudeCode)
	slices.Sort(got)
	if known := slices.Sorted(slices.Values(want[:2])); !slices.Equal(got, known) {
		t.Fatalf("before the directory is recorded: %v", got)
	}
	if _, err := agentdirs.Claude.Remember(personal); err != nil {
		t.Fatal(err)
	}
	if stores, err = FindStores(options); err != nil {
		t.Fatal(err)
	}
	got = paths(stores, vendors.ClaudeCode)
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("FindStores = %v, want %v", got, want)
	}

	// An override replaces them all, one directory or several.
	options.ClaudeProjectsDirs = []string{filepath.Join(work, "projects"), filepath.Join(personal, "projects")}
	if stores, err = FindStores(options); err != nil || len(stores) != 2 {
		t.Fatalf("several: %v, %v", stores, err)
	}
	options.ClaudeProjectsDir = filepath.Join(personal, "projects")
	if stores, err = FindStores(options); err != nil || len(stores) != 1 || stores[0].Path != filepath.Join(personal, "projects", "-repo", "personal.jsonl") {
		t.Fatalf("one: %v, %v", stores, err)
	}
}

// Session discovery for a repository reads the same directories.
func TestFindSessionsReadsEveryKnownClaudeConfigDirectory(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv(agentdirs.Claude.Env, "")
	repo := filepath.Join(home, "repo")
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	personal := filepath.Join(home, ".claude.personal")
	project := claudecode.EncodeProjectDir(repo)
	const defaultID, personalID = "aaaaaaaa-0000-4000-8000-00000000000a", "bbbbbbbb-0000-4000-8000-00000000000b"
	write(t, filepath.Join(home, ".claude", "projects", project, defaultID+".jsonl"), claudeTranscript(defaultID, repo))
	write(t, filepath.Join(personal, "projects", project, personalID+".jsonl"), claudeTranscript(personalID, repo))
	if _, err := agentdirs.Claude.Remember(personal); err != nil {
		t.Fatal(err)
	}
	sessions, _, err := FindSessions(repo, FindOptions{CodexSessionsDir: filepath.Join(home, "none"), CursorStorePaths: []string{filepath.Join(home, "none")}})
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, s := range sessions {
		ids = append(ids, s.SessionID)
	}
	slices.Sort(ids)
	if !slices.Equal(ids, []string{defaultID, personalID}) {
		t.Fatalf("sessions = %v", ids)
	}
}
