package linktest

import (
	"path/filepath"
	"testing"

	"github.com/lastpersonlabs/baum/sessions"
	"github.com/lastpersonlabs/baum/sessions/collector/claudecode"
)

// EarlierSnapshot returns stores holding an earlier reading of the fixture's
// session: the same id, but its commit had not happened yet, so it names a
// commit the pull request does not have.
func (f Fixture) EarlierSnapshot(t *testing.T) sessions.Options {
	t.Helper()
	base := t.TempDir()
	claudeDir := filepath.Join(base, "claude")
	write(t, filepath.Join(claudeDir, claudecode.EncodeProjectDir(f.Repo), SessionID+".jsonl"),
		transcript(f.Repo, "0000000000000000000000000000000000000000"))
	return sessions.Options{
		ClaudeProjectsDir: claudeDir,
		CodexSessionsDir:  filepath.Join(base, "codex"),
		CursorStorePaths:  []string{filepath.Join(base, "absent.vscdb")},
	}
}
