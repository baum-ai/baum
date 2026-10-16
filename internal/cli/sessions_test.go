package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/baum-ai/baum/sessions/collector/claudecode"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// sessionsFixture is a clone plus a Claude Code store holding one session in
// it whose prompt carries an e-mail address, and empty Codex and Cursor stores.
func sessionsFixture(t *testing.T) (repo string, storeFlags []string) {
	t.Helper()
	return sessionsFixtureOnBranch(t, "feature/x")
}

func sessionsFixtureOnBranch(t *testing.T, branch string) (repo string, storeFlags []string) {
	t.Helper()
	base := t.TempDir()
	repo = filepath.Join(base, "demo")
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	claudeDir := filepath.Join(base, "claude")
	const id = "aaaaaaaa-0000-4000-8000-000000000001"
	writeFile(t, filepath.Join(claudeDir, claudecode.EncodeProjectDir(repo), id+".jsonl"),
		fmt.Sprintf(`{"parentUuid":null,"isSidechain":false,"cwd":%[1]q,"gitBranch":%[3]q,"type":"user","uuid":"u-1","sessionId":%[2]q,"timestamp":"2026-07-01T10:00:00.000Z","promptId":"p-1","message":{"role":"user","content":"ask ada.lovelace@example.com about the parser"}}
`, repo, id, branch))
	return repo, []string{
		"--claude-dir", claudeDir,
		"--codex-dir", filepath.Join(base, "codex", "sessions"),
		"--cursor-store", filepath.Join(base, "absent.vscdb"),
	}
}

func TestSessionsTable(t *testing.T) {
	repo, flags := sessionsFixture(t)
	stdout, stderr, err := run(t, append([]string{"sessions", repo}, flags...)...)
	if err != nil {
		t.Fatalf("sessions: %v\n%s", err, stderr)
	}
	if !strings.Contains(stdout, "SOURCE") || !strings.Contains(stdout, "claude_code") || !strings.Contains(stdout, "aaaaaaaa-0000-4000-8000-000000000001") {
		t.Errorf("table:\n%s", stdout)
	}
	if !strings.Contains(stdout, "feature/x") {
		t.Errorf("branch missing:\n%s", stdout)
	}
}

func TestSessionsJSONRedactsText(t *testing.T) {
	repo, flags := sessionsFixture(t)
	stdout, _, err := run(t, append([]string{"sessions", "--json", repo}, flags...)...)
	if err != nil {
		t.Fatal(err)
	}
	var rows []sessionRow
	if err := json.Unmarshal([]byte(stdout), &rows); err != nil {
		t.Fatalf("json: %v\n%s", err, stdout)
	}
	if len(rows) != 1 || rows[0].Source != "claude_code" || rows[0].Branch != "feature/x" {
		t.Errorf("rows = %+v", rows)
	}
	if strings.Contains(stdout, "ada.lovelace") || strings.Contains(stdout, "example.com") {
		t.Errorf("e-mail leaked into the listing:\n%s", stdout)
	}
}

func TestSessionsDumpIsRedacted(t *testing.T) {
	repo, flags := sessionsFixture(t)
	stdout, _, err := run(t, append([]string{"sessions", "--dump", "aaaaaaaa-0000-4000-8000-000000000001", repo}, flags...)...)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, `"user_inputs"`) || !strings.Contains(stdout, "about the parser") {
		t.Errorf("dump should be the graph:\n%s", stdout)
	}
	if strings.Contains(stdout, "ada.lovelace") || strings.Contains(stdout, "example.com") {
		t.Errorf("dump must be redacted:\n%s", stdout)
	}
	if _, _, err := run(t, append([]string{"sessions", "--dump", "nope", repo}, flags...)...); err == nil {
		t.Error("unknown session id must fail")
	}
}

func TestSessionsFiltersAndErrors(t *testing.T) {
	repo, flags := sessionsFixture(t)
	stdout, _, err := run(t, append([]string{"sessions", "--vendor", "codex", repo}, flags...)...)
	if err != nil || !strings.Contains(stdout, "no sessions recorded") {
		t.Errorf("vendor filter: %v\n%s", err, stdout)
	}
	if _, _, err := run(t, append([]string{"sessions", "--vendor", "copilot", repo}, flags...)...); err == nil {
		t.Error("unknown vendor must fail")
	}
	if _, _, err := run(t, append([]string{"sessions", "--since", "yesterday", repo}, flags...)...); err == nil {
		t.Error("bad --since must fail")
	}
	if _, _, err := run(t, "sessions", t.TempDir()); err == nil {
		t.Error("a directory outside a git repository must fail")
	}
}

func TestParseSince(t *testing.T) {
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	if got, err := parseSince("72h", now); err != nil || !got.Equal(now.Add(-72*time.Hour)) {
		t.Errorf("duration: %v %v", got, err)
	}
	if got, err := parseSince("2026-09-01", now); err != nil || got.Year() != 2026 || got.Month() != 9 || got.Day() != 1 {
		t.Errorf("date: %v %v", got, err)
	}
	if _, err := parseSince("-1h", now); err == nil {
		t.Error("negative duration accepted")
	}
	if _, err := parseSince("soon", now); err == nil {
		t.Error("garbage accepted")
	}
}

func TestSessionsRedactsBranch(t *testing.T) {
	repo, flags := sessionsFixtureOnBranch(t, "fix/ada.lovelace@example.com")
	for _, args := range [][]string{{"sessions", repo}, {"sessions", "--json", repo}} {
		stdout, stderr, err := run(t, append(args, flags...)...)
		if err != nil {
			t.Fatalf("%v: %v\n%s", args, err, stderr)
		}
		if strings.Contains(stdout, "ada.lovelace") || strings.Contains(stdout, "example.com") {
			t.Errorf("%v leaked the branch address:\n%s", args, stdout)
		}
	}
}

func TestTruncateKeepsValidUTF8(t *testing.T) {
	got := truncate(strings.Repeat("é", 40), 10)
	if !utf8.ValidString(got) || !strings.HasSuffix(got, "…") {
		t.Fatalf("truncate = %q", got)
	}
}
