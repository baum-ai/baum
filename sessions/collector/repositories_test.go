//go:build !windows

package collector

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestFindRepositoriesPrefersCheckoutWithMoreSessions(t *testing.T) {
	tmp := t.TempDir()
	first := makeRepository(t, filepath.Join(tmp, "first"), "git@github.com:acme/demo.git")
	second := makeRepository(t, filepath.Join(tmp, "second"), "https://github.com/acme/demo.git")
	other := makeRepository(t, filepath.Join(tmp, "other"), "https://github.com/acme/other.git")
	claude := filepath.Join(tmp, "claude")
	writeClaudeStore(t, claude, "first", first)
	writeClaudeStore(t, claude, "second-a", second)
	nested := filepath.Join(second, "nested")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	writeClaudeStore(t, claude, "second-b", nested)
	writeClaudeStore(t, claude, "other", other)

	found, report, err := FindRepositories(t.Context(), StoreOptions{
		ClaudeProjectsDir: claude,
		CodexSessionsDir:  filepath.Join(tmp, "missing-codex"),
		CursorStorePaths:  []string{filepath.Join(tmp, "missing-cursor")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Skipped) != 0 {
		t.Fatalf("report = %+v", report)
	}
	demo := found["acme/demo"]
	if demo.Root != second || demo.SessionCount != 2 || len(demo.OtherCheckouts) != 1 || demo.OtherCheckouts[0] != first {
		t.Fatalf("demo = %+v", demo)
	}
	if got := demo.Roots; len(got) != 2 || got[0] != first || got[1] != second {
		t.Fatalf("demo roots = %v", got)
	}
	if got := found["acme/other"]; got.Root != other || got.SessionCount != 1 {
		t.Fatalf("other = %+v", got)
	}
}

func TestVerifyRepositoryRootsKeepsOnlyGitVerifiedOrigins(t *testing.T) {
	tmp := t.TempDir()
	first := makeRepository(t, filepath.Join(tmp, "first"), "git@github.com:acme/demo.git")
	second := makeRepository(t, filepath.Join(tmp, "second"), "https://github.com/acme/demo.git")
	other := makeRepository(t, filepath.Join(tmp, "other"), "https://github.com/acme/other.git")
	missing := filepath.Join(tmp, "missing")

	roots := VerifyRepositoryRoots(t.Context(), "ACME/demo", []string{
		filepath.Join(second, "nested"), other, missing, first, second,
	})
	if len(roots) != 2 || roots[0] != first || roots[1] != second {
		t.Fatalf("verified roots = %v", roots)
	}
}

func TestFindRepositoriesReportsUnreadableMetadata(t *testing.T) {
	tmp := t.TempDir()
	claude := filepath.Join(tmp, "claude", "project-broken")
	if err := os.MkdirAll(claude, 0o755); err != nil {
		t.Fatal(err)
	}
	broken := filepath.Join(claude, "broken.jsonl")
	if err := os.WriteFile(broken, []byte("not json\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	found, report, err := FindRepositories(t.Context(), StoreOptions{
		ClaudeProjectsDir: filepath.Join(tmp, "claude"),
		CodexSessionsDir:  filepath.Join(tmp, "missing-codex"),
		CursorStorePaths:  []string{filepath.Join(tmp, "missing-cursor")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 0 || len(report.Skipped) != 1 || report.Skipped[0].Path != broken {
		t.Fatalf("found=%+v report=%+v", found, report)
	}
}

func TestFindRepositoriesUsesExistingAncestorOfDeletedWorkingDir(t *testing.T) {
	tmp := t.TempDir()
	repo := makeRepository(t, filepath.Join(tmp, "repo"), "https://github.com/acme/demo.git")
	deleted := filepath.Join(repo, "deleted", "nested")
	claude := filepath.Join(tmp, "claude")
	writeClaudeStore(t, claude, "deleted", deleted)

	found, report, err := FindRepositories(t.Context(), StoreOptions{
		ClaudeProjectsDir: claude,
		CodexSessionsDir:  filepath.Join(tmp, "missing-codex"),
		CursorStorePaths:  []string{filepath.Join(tmp, "missing-cursor")},
	})
	if err != nil || len(report.Skipped) != 0 {
		t.Fatalf("report=%+v error=%v", report, err)
	}
	if got := found["acme/demo"]; got.Root != repo || got.SessionCount != 1 {
		t.Fatalf("repository = %+v", got)
	}
}

func makeRepository(t *testing.T, path, origin string) string {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init"}, {"remote", "add", "origin", origin}} {
		cmd := exec.Command("git", append([]string{"-C", path}, args...)...)
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
	}
	return path
}

func writeClaudeStore(t *testing.T, root, id, cwd string) {
	t.Helper()
	dir := filepath.Join(root, "project-"+id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf(`{"cwd":%q,"type":"user","sessionId":%q}`, cwd, id) + "\n"
	path := filepath.Join(dir, id+".jsonl")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, time.Now(), time.Now()); err != nil {
		t.Fatal(err)
	}
}
