package collector

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

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
	// ClaudeProjectsDir overrides the Claude Code session store
	// (default ~/.claude/projects).
	ClaudeProjectsDir string
	// CodexSessionsDir overrides the Codex session store
	// (default ~/.codex/sessions).
	CodexSessionsDir string
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
	claudeDir, codexDir := opts.ClaudeProjectsDir, opts.CodexSessionsDir
	if (claudeDir == "" && opts.wants(vendors.ClaudeCode)) ||
		(codexDir == "" && opts.wants(vendors.Codex)) {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("resolve session stores: %w", err)
		}
		if claudeDir == "" {
			claudeDir = filepath.Join(home, ".claude", "projects")
		}
		if codexDir == "" {
			codexDir = filepath.Join(home, ".codex", "sessions")
		}
	}

	var stores []Store
	if opts.wants(vendors.ClaudeCode) {
		found, err := claudeStores(claudeDir)
		if err != nil {
			return nil, err
		}
		stores = append(stores, found...)
	}
	if opts.wants(vendors.Codex) {
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
			if d.IsDir() || !strings.HasPrefix(d.Name(), "rollout-") || filepath.Ext(d.Name()) != ".jsonl" {
				return nil
			}
			info, err := d.Info()
			if err != nil {
				return nil
			}
			stores = append(stores, Store{
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
