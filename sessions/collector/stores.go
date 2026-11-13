package collector

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"time"

	"github.com/baum-ai/baum/internal/agentdirs"
	"github.com/baum-ai/baum/sessions/codextranscript"
	"github.com/baum-ai/baum/sessions/collector/cursor"
	"github.com/baum-ai/baum/sessions/vendors"
)

// Store is one local file holding agent session data. Most sources write one
// session per file; Cursor keeps every session of an editor profile in a
// single SQLite state store, so a store is not the same thing as a session.
type Store struct {
	Source vendors.Slug `json:"source"`
	Path   string       `json:"path"`
	// ModifiedAt is the file's last modification time, and the only cheap
	// signal for filtering by recency before a store is parsed.
	ModifiedAt time.Time `json:"modified_at"`
	SizeBytes  int64     `json:"size_bytes"`
}

// StoreOptions configures FindStores.
type StoreOptions struct {
	// ClaudeProjectsDir overrides the Claude Code session stores with one
	// directory. The default is the projects folder of every Claude Code
	// configuration directory baum knows (agentdirs.Claude): ~/.claude,
	// CLAUDE_CONFIG_DIR and the recorded ones.
	ClaudeProjectsDir string
	// ClaudeProjectsDirs overrides them with several, when ClaudeProjectsDir
	// is empty.
	ClaudeProjectsDirs []string
	// CodexSessionsDir overrides the Codex session stores with one
	// directory. The default is the sessions folder of every Codex home baum
	// knows (agentdirs.Codex): ~/.codex, CODEX_HOME and the recorded ones.
	// The archived_sessions folder beside each is listed too.
	CodexSessionsDir string
	// CodexSessionsDirs overrides them with several, when CodexSessionsDir
	// is empty.
	CodexSessionsDirs []string
	// CursorStorePaths overrides Cursor state store discovery. Each entry is
	// either a `state.vscdb` file or a directory searched for one.
	CursorStorePaths []string
	// Sources restricts the search; empty means every known source.
	Sources []vendors.Slug
	// Since and Until, when set, bound a store's modification time.
	Since time.Time
	Until time.Time
}

// FindStores locates every local agent session store on this machine, without
// scoping to a repository. It is the discovery path for whole-machine
// reporting; FindSessions remains the repository-scoped path used for
// backfill and upload, where one file must map to exactly one session.
//
// Missing stores yield no results rather than an error, so the search works
// on a machine that has only some of these tools installed.
func FindStores(opts StoreOptions) ([]Store, error) {
	var claudeDirs, codexDirs []string
	if opts.wants(vendors.ClaudeCode) {
		var err error
		if claudeDirs, err = claudeProjectsDirs(opts.ClaudeProjectsDir, opts.ClaudeProjectsDirs); err != nil {
			return nil, err
		}
	}
	if opts.wants(vendors.Codex) {
		var err error
		if codexDirs, err = codexSessionsDirs(opts.CodexSessionsDir, opts.CodexSessionsDirs); err != nil {
			return nil, err
		}
	}

	var stores []Store
	for _, claudeDir := range claudeDirs {
		found, err := claudeStores(claudeDir)
		if err != nil {
			return nil, err
		}
		stores = append(stores, found...)
	}
	for _, codexDir := range codexDirs {
		found, err := codexStores(codexDir)
		if err != nil {
			return nil, err
		}
		stores = append(stores, found...)
	}
	if opts.wants(vendors.Cursor) {
		for _, file := range cursor.ListFiles(opts.CursorStorePaths) {
			stores = append(stores, Store{
				Source:     vendors.Cursor,
				Path:       file.Path,
				ModifiedAt: file.ModifiedAt,
				SizeBytes:  file.SizeBytes,
			})
		}
	}

	stores = slices.DeleteFunc(stores, func(store Store) bool {
		return (!opts.Since.IsZero() && store.ModifiedAt.Before(opts.Since)) ||
			(!opts.Until.IsZero() && store.ModifiedAt.After(opts.Until))
	})
	sort.Slice(stores, func(i, j int) bool {
		if !stores[i].ModifiedAt.Equal(stores[j].ModifiedAt) {
			return stores[i].ModifiedAt.Before(stores[j].ModifiedAt)
		}
		return stores[i].Path < stores[j].Path
	})
	return stores, nil
}

// claudeProjectsDirs resolves the Claude Code session stores to read: the
// one override, else the several, else the projects folder of every Claude
// Code configuration directory baum knows.
func claudeProjectsDirs(override string, overrides []string) ([]string, error) {
	if override != "" {
		return []string{override}, nil
	}
	if len(overrides) > 0 {
		return overrides, nil
	}
	if _, err := os.UserHomeDir(); err != nil {
		return nil, fmt.Errorf("resolve session stores: %w", err)
	}
	return agentdirs.Claude.Under("", "projects"), nil
}

// codexSessionsDirs resolves the Codex session stores to read: the one
// override, else the several, else the sessions folder of every Codex home
// baum knows.
func codexSessionsDirs(override string, overrides []string) ([]string, error) {
	if override != "" {
		return []string{override}, nil
	}
	if len(overrides) > 0 {
		return overrides, nil
	}
	if _, err := os.UserHomeDir(); err != nil {
		return nil, fmt.Errorf("resolve session stores: %w", err)
	}
	return agentdirs.Codex.Under("", "sessions"), nil
}

// wants reports whether a source is in scope.
func (o StoreOptions) wants(source vendors.Slug) bool {
	return len(o.Sources) == 0 || slices.Contains(o.Sources, source)
}

// claudeStores lists the transcripts under the Claude Code project store.
// Only top-level files count: subdirectories hold sidecar data (subagents,
// memory) rather than sessions.
func claudeStores(projectsDir string) ([]Store, error) {
	projects, err := os.ReadDir(projectsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var stores []Store
	for _, project := range projects {
		if !project.IsDir() {
			continue
		}
		files, err := os.ReadDir(filepath.Join(projectsDir, project.Name()))
		if err != nil {
			continue
		}
		for _, file := range files {
			if file.IsDir() || filepath.Ext(file.Name()) != ".jsonl" {
				continue
			}
			info, err := file.Info()
			if err != nil {
				continue
			}
			stores = append(stores, Store{
				Source:     vendors.ClaudeCode,
				Path:       filepath.Join(projectsDir, project.Name(), file.Name()),
				ModifiedAt: info.ModTime().UTC(),
				SizeBytes:  info.Size(),
			})
		}
	}
	return stores, nil
}

// codexStores lists the rollout files under the Codex session store, which is
// laid out by date rather than by project.
func codexStores(sessionsDir string) ([]Store, error) {
	var stores []Store
	for _, root := range codexStoreRoots(sessionsDir) {
		var listed []Store
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				if path == root {
					return err
				}
				if d != nil && d.IsDir() {
					return fs.SkipDir
				}
				return nil
			}
			if d.IsDir() || !codextranscript.IsRolloutName(d.Name()) {
				return nil
			}
			info, err := d.Info()
			if err != nil {
				return nil
			}
			listed = append(listed, Store{
				Source:     vendors.Codex,
				Path:       path,
				ModifiedAt: info.ModTime().UTC(),
				SizeBytes:  info.Size(),
			})
			return nil
		})
		if err != nil && !os.IsNotExist(err) {
			return nil, err
		}
		// A compressed rollout is left out when its plain copy was listed too.
		plain := make(map[string]bool, len(listed))
		for _, store := range listed {
			plain[store.Path] = true
		}
		for _, store := range listed {
			if !codextranscript.Compressed(store.Path) || !plain[codextranscript.PlainPath(store.Path)] {
				stores = append(stores, store)
			}
		}
	}
	return stores, nil
}

func codexStoreRoots(sessionsDir string) []string {
	sessionsDir = filepath.Clean(sessionsDir)
	roots := []string{sessionsDir}
	archived := filepath.Join(filepath.Dir(sessionsDir), "archived_sessions")
	if archived != sessionsDir {
		roots = append(roots, archived)
	}
	return roots
}
