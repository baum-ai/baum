package collector

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/lastpersonlabs/baum/sessions/collector/claudecode"
	"github.com/lastpersonlabs/baum/sessions/collector/codex"
	"github.com/lastpersonlabs/baum/sessions/collector/cursor"
	"github.com/lastpersonlabs/baum/sessions/collector/model"
)

// Session and SessionFile are the shared discovery result shape.
type (
	Session     = model.Session
	SessionFile = model.SessionFile
	ScanReport  = model.ScanReport
	SkippedFile = model.SkippedFile
)

type FindOptions struct {
	// ClaudeProjectsDir overrides the Claude Code session store
	// (default ~/.claude/projects).
	ClaudeProjectsDir string
	// CodexSessionsDir overrides the Codex session store
	// (default ~/.codex/sessions).
	CodexSessionsDir string
	// CursorStorePaths overrides Cursor state store discovery. Each entry is
	// either a state.vscdb file or a directory searched for one; empty means
	// the default global Cursor store.
	CursorStorePaths []string
	// PreviousPaths are directories the repository previously lived at.
	PreviousPaths []string
}

// FindSessions returns the local Claude Code, Codex, and Cursor sessions relevant to
// the git repository containing dir, ordered by modification time. The report
// names every session file discovery had to drop.
func FindSessions(dir string, opts FindOptions) ([]Session, ScanReport, error) {
	var report ScanReport
	root, err := RepoRoot(dir)
	if err != nil {
		return nil, report, err
	}
	roots := []string{root}
	for _, prev := range opts.PreviousPaths {
		abs, err := filepath.Abs(prev)
		if err != nil {
			return nil, report, fmt.Errorf("previous path %q: %w", prev, err)
		}
		if !slices.Contains(roots, abs) {
			roots = append(roots, abs)
		}
	}
	claudeDir, codexDir := opts.ClaudeProjectsDir, opts.CodexSessionsDir
	if claudeDir == "" || codexDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, report, fmt.Errorf("resolve session stores: %w", err)
		}
		if claudeDir == "" {
			claudeDir = filepath.Join(home, ".claude", "projects")
		}
		if codexDir == "" {
			codexDir = filepath.Join(home, ".codex", "sessions")
		}
	}

	sessions, claudeReport, err := claudecode.FindSessions(claudeDir, roots)
	if err != nil {
		return nil, report, err
	}
	codexSessions, codexReport, err := codex.FindSessions(codexDir, roots)
	if err != nil {
		return nil, report, err
	}
	sessions = append(sessions, codexSessions...)
	cursorSessions, cursorReport, err := cursor.FindSessions(opts.CursorStorePaths, roots)
	if err != nil {
		return nil, report, err
	}
	sessions = append(sessions, cursorSessions...)
	report.Merge(claudeReport)
	report.Merge(codexReport)
	report.Merge(cursorReport)

	var worktrees []string
	for _, root := range roots {
		worktrees = append(worktrees, rememberedWorktrees(root)...)
	}
	for i := range sessions {
		sessions[i].RepoRoot, sessions[i].WorktreeRoot = sessionRoots(sessions[i].WorkingDir, roots, worktrees)
	}
	sort.Slice(sessions, func(i, j int) bool {
		if !sessions[i].ModifiedAt.Equal(sessions[j].ModifiedAt) {
			return sessions[i].ModifiedAt.Before(sessions[j].ModifiedAt)
		}
		return sessions[i].Path < sessions[j].Path
	})
	return sessions, report, nil
}

// sessionRoots names the checkout a session's working directory sits in and,
// when the session ran in a git worktree inside that checkout, the worktree
// too. A worktree directory holds a .git file, so one walk finds both.
//
// The walk needs the directory to still be there, and an agent worktree is
// usually deleted as soon as its branch is pushed, so for a directory that is
// gone git's memory of it is read instead. The walk's answer is discarded then:
// it climbs to the nearest ancestor that still exists, which can sit above where
// the session ran. A directory that is present is read as what it is now, never
// overruled by a stale registration.
//
// A gone directory git does not remember leaves only the checkout, which may be
// one directory too shallow. It is recorded anyway, because nothing consumes
// these roots yet and a session that cannot be placed now cannot be placed later
// either: the directory that would have settled it is what is missing.
func sessionRoots(workingDir string, roots, worktrees []string) (repoRoot, worktreeRoot string) {
	_, statErr := os.Stat(workingDir)
	missing := errors.Is(statErr, fs.ErrNotExist)
	for _, root := range roots {
		if !model.PathWithin(root, workingDir) {
			continue
		}
		nearest, err := repoRootFromRecordedPath(workingDir)
		if err != nil || missing {
			nearest = deepestContaining(worktrees, workingDir)
		}
		if model.PathWithin(root, nearest) && nearest != root {
			return root, nearest
		}
		return root, ""
	}
	return "", ""
}

// rememberedWorktrees is where root's git worktrees lived, deleted ones
// included: git keeps one bookkeeping entry per worktree naming its directory,
// and prunes them on its own. Reading the entries rather than running git
// worktree list keeps this a directory read like every other root resolution
// here.
func rememberedWorktrees(root string) []string {
	admin := filepath.Join(root, ".git", "worktrees")
	entries, err := os.ReadDir(admin)
	if err != nil {
		return nil
	}
	dirs := make([]string, 0, len(entries))
	for _, entry := range entries {
		gitdir, err := os.ReadFile(filepath.Join(admin, entry.Name(), "gitdir"))
		if err != nil {
			continue
		}
		// The entry names the worktree's .git file, so its parent is the root.
		// Git writes the path relative to the entry's own directory when
		// worktree.useRelativePaths is set (available since git 2.48).
		named := strings.TrimSpace(string(gitdir))
		if !filepath.IsAbs(named) {
			named = filepath.Join(admin, entry.Name(), named)
		}
		dirs = append(dirs, filepath.Dir(named))
	}
	return dirs
}

// deepestContaining is the longest of dirs that path lies within, or "".
func deepestContaining(dirs []string, path string) string {
	deepest := ""
	for _, dir := range dirs {
		if len(dir) > len(deepest) && model.PathWithin(dir, path) {
			deepest = dir
		}
	}
	return deepest
}

// RepoRoot returns the root of the git repository containing dir: the
// nearest ancestor (including dir itself) with a .git entry.
func RepoRoot(dir string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	if info, err := os.Stat(abs); err != nil {
		return "", err
	} else if !info.IsDir() {
		return "", fmt.Errorf("%s is not a directory", abs)
	}
	for d := abs; ; {
		if _, err := os.Stat(filepath.Join(d, ".git")); err == nil {
			return d, nil
		}
		parent := filepath.Dir(d)
		if parent == d {
			return "", fmt.Errorf("no git repository found at or above %s", abs)
		}
		d = parent
	}
}

// repoRootFromRecordedPath finds the repository containing a session's
// recorded working directory even when that directory has since been deleted.
func repoRootFromRecordedPath(path string) (string, error) {
	candidate, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	for {
		info, statErr := os.Stat(candidate)
		switch {
		case statErr == nil:
			if !info.IsDir() {
				candidate = filepath.Dir(candidate)
			}
			return RepoRoot(candidate)
		case !errors.Is(statErr, os.ErrNotExist):
			return "", statErr
		}
		parent := filepath.Dir(candidate)
		if parent == candidate {
			return "", fmt.Errorf("no existing ancestor for recorded working directory %s", path)
		}
		candidate = parent
	}
}
