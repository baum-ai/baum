package reviewruns

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/baum-ai/baum/agent"
	"github.com/baum-ai/baum/sessions/collector/claudecode"
)

// maxRollouts bounds how many Codex rollout files one fallback scan reads the
// first line of. The scan goes newest first, so what it can miss is old.
var maxRollouts = 50000

var sessionName = regexp.MustCompile(`^[0-9A-Za-z-]+$`)

// claudeProjects is where Claude Code keeps its sessions: the projects
// directory of $CLAUDE_CONFIG_DIR, else of ~/.claude.
func claudeProjects() (string, bool) {
	if dir := os.Getenv("CLAUDE_CONFIG_DIR"); dir != "" {
		return filepath.Join(dir, "projects"), true
	}
	home, err := os.UserHomeDir()
	return filepath.Join(home, ".claude", "projects"), err == nil
}

// codexSessions is where Codex keeps its rollouts: the sessions directory of
// $CODEX_HOME, else of ~/.codex.
func codexSessions() (string, bool) {
	if dir := os.Getenv("CODEX_HOME"); dir != "" {
		return filepath.Join(dir, "sessions"), true
	}
	home, err := os.UserHomeDir()
	return filepath.Join(home, ".codex", "sessions"), err == nil
}

// resolvedRoot is root with symlinks resolved, as the run directories Create
// returns are spelled.
func resolvedRoot(root string) string {
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	return filepath.Clean(root)
}

// underRoot resolves the parent of dir so aliases such as /var on macOS
// and short Windows paths compare like the paths returned by Create. It
// leaves dir itself unresolved so candidates can reject a final symlink.
func underRoot(root, dir string) bool {
	dir = filepath.Clean(dir)
	return resolvedRoot(filepath.Dir(dir)) == resolvedRoot(root) && strings.HasPrefix(filepath.Base(dir), agent.RunDirPrefix)
}

// RemoveSessions deletes the files the agent CLIs keep for the sessions they
// ran in the run directory dir, which must be a run directory directly under
// root (anything else is ignored). With ids it deletes those sessions only:
// Claude Code's <id>.jsonl and <id>/ in the directory's project directory, and
// Codex's rollout files ending in -<id>.jsonl whose recorded cwd is dir.
// Without ids it deletes every session of the directory, chat forks made later
// included: Claude Code's whole project directory, the Codex rollouts of the
// ids Record kept (found by name in the dated folders from the run's start),
// and, only when the run has no complete record, the newest Codex rollouts
// (at most maxRollouts) that record dir as their cwd. Symlinks are never followed and missing files are
// not an error.
func RemoveSessions(root, dir string, ids ...string) error {
	dir = filepath.Clean(dir)
	if !underRoot(root, dir) {
		return nil
	}
	for _, id := range ids {
		if !sessionName.MatchString(id) {
			return errors.New("invalid session id")
		}
	}
	if len(ids) > 0 {
		// The listed sessions only, from the days the run was alive.
		since := time.Unix(1, 0)
		if m, ok := readManifest(root, dir); ok {
			since = m.Created
		}
		return errors.Join(removeClaude(dir, ids), removeCodex(dir, ids, since, false))
	}
	// Every session of the directory: the recorded ones by id, and a bounded
	// scan only when the record is missing or may be incomplete.
	m, recorded := readManifest(root, dir)
	if !recorded {
		m = manifest{Created: time.Unix(1, 0)}
	}
	err := errors.Join(removeClaude(dir, nil), removeCodex(dir, m.IDs, m.Created, !recorded || !m.Complete || m.Pending))
	if err == nil {
		removeManifest(root, dir)
	}
	return err
}

func removeClaude(dir string, ids []string) error {
	projects, ok := claudeProjects()
	if !ok {
		return nil
	}
	project := filepath.Join(projects, claudecode.EncodeProjectDir(dir))
	if filepath.Dir(project) != filepath.Clean(projects) {
		return nil
	}
	if info, err := os.Lstat(project); err != nil || !info.IsDir() {
		return nil
	}
	if len(ids) == 0 {
		return removeTree(project)
	}
	var errs []error
	for _, id := range ids {
		for _, name := range []string{id + ".jsonl", id} {
			path := filepath.Join(project, name)
			if _, err := os.Lstat(path); err == nil {
				errs = append(errs, removeTree(path))
			}
		}
	}
	return errors.Join(errs...)
}
