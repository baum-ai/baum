package collector

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/baum-ai/baum/internal/agentdirs"
	"github.com/baum-ai/baum/sessions/vendors"
)

// codexHomes gives the test its own home, record and CODEX_HOME (work), and
// returns the home and the work and personal homes.
func codexHomes(t *testing.T) (home, work, personal string) {
	t.Helper()
	home = t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv(agentdirs.Claude.Env, "")
	work, personal = filepath.Join(home, ".codex-work"), filepath.Join(home, ".codex.personal")
	t.Setenv(agentdirs.Codex.Env, work)
	return home, work, personal
}

// Without an override, the Codex stores are the sessions and
// archived_sessions folders of every Codex home baum knows: ~/.codex,
// CODEX_HOME and the recorded ones.
func TestFindStoresReadsEveryKnownCodexHome(t *testing.T) {
	home, work, personal := codexHomes(t)
	rollout := func(dir, folder, id string) string {
		return filepath.Join(dir, folder, "2026", "07", "01", "rollout-2026-07-01T10-00-00-"+id+".jsonl")
	}
	want := []string{
		rollout(filepath.Join(home, ".codex"), "sessions", "default"),
		rollout(work, "sessions", "work"),
		rollout(work, "archived_sessions", "archived"),
		rollout(personal, "sessions", "personal"),
	}
	for _, path := range want {
		write(t, path, `{"type":"session_meta"}`)
	}
	options := StoreOptions{Sources: []vendors.Slug{vendors.Codex}}
	stores, err := FindStores(options)
	if err != nil {
		t.Fatal(err)
	}
	got := paths(stores, vendors.Codex)
	slices.Sort(got)
	if known := slices.Sorted(slices.Values(want[:3])); !slices.Equal(got, known) {
		t.Fatalf("before the home is recorded: %v", got)
	}
	if _, err := agentdirs.Codex.Remember(personal); err != nil {
		t.Fatal(err)
	}
	if stores, err = FindStores(options); err != nil {
		t.Fatal(err)
	}
	got = paths(stores, vendors.Codex)
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("FindStores = %v, want %v", got, want)
	}
	// A Codex home is not a Claude Code store, nor the other way round.
	if stores, err = FindStores(StoreOptions{Sources: []vendors.Slug{vendors.ClaudeCode}}); err != nil || len(stores) != 0 {
		t.Fatalf("Claude Code stores: %v, %v", stores, err)
	}

	// An override replaces them all, one directory or several.
	options.CodexSessionsDirs = []string{filepath.Join(personal, "sessions")}
	if stores, err = FindStores(options); err != nil || len(stores) != 1 || stores[0].Path != rollout(personal, "sessions", "personal") {
		t.Fatalf("several: %v, %v", stores, err)
	}
	options.CodexSessionsDir = filepath.Join(home, ".codex", "sessions")
	if stores, err = FindStores(options); err != nil || len(stores) != 1 || stores[0].Path != rollout(filepath.Join(home, ".codex"), "sessions", "default") {
		t.Fatalf("one: %v, %v", stores, err)
	}
}

// Session discovery for a repository reads the same homes.
func TestFindSessionsReadsEveryKnownCodexHome(t *testing.T) {
	home, work, personal := codexHomes(t)
	repo := filepath.Join(home, "repo")
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	const defaultID, workID, personalID = "019f0000-0000-7000-8000-00000000000a", "019f0000-0000-7000-8000-00000000000b", "019f0000-0000-7000-8000-00000000000c"
	for dir, id := range map[string]string{filepath.Join(home, ".codex"): defaultID, work: workID, personal: personalID} {
		write(t, filepath.Join(dir, "sessions", "2026", "07", "01", "rollout-2026-07-01T10-00-00-"+id+".jsonl"), codexRollout(id, repo))
	}
	if _, err := agentdirs.Codex.Remember(personal); err != nil {
		t.Fatal(err)
	}
	sessions, _, err := FindSessions(repo, FindOptions{ClaudeProjectsDir: filepath.Join(home, "none"), CursorStorePaths: []string{filepath.Join(home, "none")}})
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, s := range sessions {
		ids = append(ids, s.SessionID)
	}
	slices.Sort(ids)
	if !slices.Equal(ids, []string{defaultID, workID, personalID}) {
		t.Fatalf("sessions = %v", ids)
	}
}
