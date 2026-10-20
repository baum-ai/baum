package collector

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/baum-ai/baum/sessions/vendors"
)

// storeLayout builds a machine-shaped set of session stores under dir and
// returns the Claude, Codex, and Cursor roots.
func storeLayout(t *testing.T, dir string) (claudeDir, codexDir, cursorDir string) {
	t.Helper()
	claudeDir = filepath.Join(dir, "claude", "projects")
	codexDir = filepath.Join(dir, "codex", "sessions")
	cursorDir = filepath.Join(dir, "Cursor")

	write(t, filepath.Join(claudeDir, "-home-dev-repo", "sess-1.jsonl"), `{"type":"user"}`)
	write(t, filepath.Join(claudeDir, "-home-dev-repo", "sess-2.jsonl"), `{"type":"user"}`)
	// Sidecar data below a project directory is not a session.
	write(t, filepath.Join(claudeDir, "-home-dev-repo", "subagents", "nested.jsonl"), `{}`)
	write(t, filepath.Join(claudeDir, "-home-dev-repo", "notes.txt"), "ignored")

	write(t, filepath.Join(codexDir, "2025", "06", "01", "rollout-2025-06-01T10-00-00-abc.jsonl"), `{"type":"session_meta"}`)
	write(t, filepath.Join(filepath.Dir(codexDir), "archived_sessions", "2025", "06", "02", "rollout-2025-06-02T10-00-00-def.jsonl"), `{"type":"session_meta"}`)
	// Only rollout-*.jsonl files count.
	write(t, filepath.Join(codexDir, "2025", "06", "01", "history.jsonl"), `{}`)

	write(t, filepath.Join(cursorDir, "User", "globalStorage", "state.vscdb"), "SQLite format 3\x00")
	write(t, filepath.Join(cursorDir, "User", "workspaceStorage", "hash-a", "state.vscdb"), "SQLite format 3\x00")
	write(t, filepath.Join(cursorDir, "User", "workspaceStorage", "hash-b", "nothing.json"), "{}")
	return claudeDir, codexDir, cursorDir
}

func write(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}

func paths(stores []Store, source vendors.Slug) []string {
	var out []string
	for _, store := range stores {
		if store.Source == source {
			out = append(out, store.Path)
		}
	}
	return out
}

func TestFindStores(t *testing.T) {
	dir := t.TempDir()
	claudeDir, codexDir, cursorDir := storeLayout(t, dir)

	stores, err := FindStores(StoreOptions{
		ClaudeProjectsDir: claudeDir,
		CodexSessionsDir:  codexDir,
		CursorStorePaths:  []string{cursorDir},
	})
	if err != nil {
		t.Fatalf("FindStores: %v", err)
	}

	if got := paths(stores, vendors.ClaudeCode); len(got) != 2 {
		t.Errorf("claude stores = %v, want the 2 top-level transcripts", got)
	}
	if got := paths(stores, vendors.Codex); len(got) != 2 {
		t.Errorf("codex stores = %v, want both active and archived rollouts", got)
	}
	if got := paths(stores, vendors.Cursor); len(got) != 2 {
		t.Errorf("cursor stores = %v, want the global and workspace stores", got)
	}
	for _, store := range stores {
		if store.SizeBytes == 0 || store.ModifiedAt.IsZero() {
			t.Errorf("%s has no size or modification time", store.Path)
		}
	}
}

func TestFindStoresOrdersByModificationTime(t *testing.T) {
	dir := t.TempDir()
	claudeDir, codexDir, cursorDir := storeLayout(t, dir)

	// Make the ordering unambiguous.
	base := time.Now().Add(-72 * time.Hour)
	touch(t, filepath.Join(codexDir, "2025", "06", "01", "rollout-2025-06-01T10-00-00-abc.jsonl"), base)
	touch(t, filepath.Join(claudeDir, "-home-dev-repo", "sess-1.jsonl"), base.Add(time.Hour))
	touch(t, filepath.Join(claudeDir, "-home-dev-repo", "sess-2.jsonl"), base.Add(2*time.Hour))

	stores, err := FindStores(StoreOptions{
		ClaudeProjectsDir: claudeDir,
		CodexSessionsDir:  codexDir,
		CursorStorePaths:  []string{cursorDir},
	})
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i < len(stores); i++ {
		if stores[i].ModifiedAt.Before(stores[i-1].ModifiedAt) {
			t.Fatalf("store %d (%s) precedes %d (%s)", i, stores[i].ModifiedAt, i-1, stores[i-1].ModifiedAt)
		}
	}
}

func TestFindStoresFiltersByTimeAndSource(t *testing.T) {
	dir := t.TempDir()
	claudeDir, codexDir, cursorDir := storeLayout(t, dir)

	old := time.Now().Add(-30 * 24 * time.Hour)
	recent := time.Now().Add(-1 * time.Hour)
	touch(t, filepath.Join(claudeDir, "-home-dev-repo", "sess-1.jsonl"), old)
	touch(t, filepath.Join(claudeDir, "-home-dev-repo", "sess-2.jsonl"), recent)

	stores, err := FindStores(StoreOptions{
		ClaudeProjectsDir: claudeDir,
		CodexSessionsDir:  codexDir,
		CursorStorePaths:  []string{cursorDir},
		Sources:           []vendors.Slug{vendors.ClaudeCode},
		Since:             time.Now().Add(-7 * 24 * time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(stores) != 1 {
		t.Fatalf("got %d stores, want only the recent Claude Code transcript: %+v", len(stores), stores)
	}
	if filepath.Base(stores[0].Path) != "sess-2.jsonl" {
		t.Errorf("kept %s, want sess-2.jsonl", stores[0].Path)
	}

	// Until excludes anything newer than the bound.
	stores, err = FindStores(StoreOptions{
		ClaudeProjectsDir: claudeDir,
		CodexSessionsDir:  codexDir,
		Sources:           []vendors.Slug{vendors.ClaudeCode},
		Until:             time.Now().Add(-7 * 24 * time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(stores) != 1 || filepath.Base(stores[0].Path) != "sess-1.jsonl" {
		t.Errorf("Until kept %+v, want only sess-1.jsonl", stores)
	}
}

func TestFindStoresMissingDirectories(t *testing.T) {
	dir := t.TempDir()
	stores, err := FindStores(StoreOptions{
		ClaudeProjectsDir: filepath.Join(dir, "no-claude"),
		CodexSessionsDir:  filepath.Join(dir, "no-codex"),
		CursorStorePaths:  []string{filepath.Join(dir, "no-cursor")},
	})
	if err != nil {
		t.Fatalf("FindStores over missing stores returned an error: %v", err)
	}
	if len(stores) != 0 {
		t.Errorf("got %d stores, want none", len(stores))
	}
}

func TestFindStoresExplicitCursorFile(t *testing.T) {
	dir := t.TempDir()
	store := filepath.Join(dir, "custom", "state.vscdb")
	write(t, store, "SQLite format 3\x00")

	stores, err := FindStores(StoreOptions{
		ClaudeProjectsDir: filepath.Join(dir, "none"),
		CodexSessionsDir:  filepath.Join(dir, "none"),
		CursorStorePaths:  []string{store},
		Sources:           []vendors.Slug{vendors.Cursor},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(stores) != 1 || stores[0].Path != store {
		t.Fatalf("got %+v, want the explicit store %s", stores, store)
	}
}

func touch(t *testing.T, path string, when time.Time) {
	t.Helper()
	if err := os.Chtimes(path, when, when); err != nil {
		t.Fatal(err)
	}
}
