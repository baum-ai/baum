// Package reviewruns manages the run directories that local reviews keep so
// a chat can continue their agent session in the same working directory.
//
// Kept directories live under one root in the user's cache directory, named
// with agent.RunDirPrefix. Pruning only ever removes directories directly
// under the root whose names carry the prefix, and never follows a symlink.
// It removes directories only: the CLI's own session files stay.
package reviewruns

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/baum-ai/baum/agent"
	"github.com/baum-ai/baum/internal/store"
)

// KeepVersions is how many of a pull request's latest versions keep their
// run directory.
const KeepVersions = 2

// MaxAge is how old a kept directory may get, by modification time.
const MaxAge = 14 * 24 * time.Hour

// DefaultRoot is where kept run directories live: under the user's cache
// directory, outside every clone and outside the system temporary directory.
func DefaultRoot() (string, error) {
	root, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "baum", "review-runs"), nil
}

// Create makes the root (private) and a new run directory in it, and returns
// the directory with symlinks resolved, which is the working directory the
// agent CLI records its session under.
func Create(root string) (string, error) {
	if err := os.MkdirAll(root, 0o700); err != nil {
		return "", err
	}
	dir, err := os.MkdirTemp(root, agent.RunDirPrefix+"*")
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		_ = os.RemoveAll(dir)
		return "", err
	}
	return resolved, nil
}

// candidate reports whether dir is a real directory (not a symlink) directly
// under root with the run directory prefix. Root is compared with its
// symlinks resolved, as Create returns directories.
func candidate(root, dir string) bool {
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	root, dir = filepath.Clean(root), filepath.Clean(dir)
	if filepath.Dir(dir) != root || !strings.HasPrefix(filepath.Base(dir), agent.RunDirPrefix) {
		return false
	}
	info, err := os.Lstat(dir)
	return err == nil && info.IsDir()
}

// Use readies a kept run directory for another agent call: it must be a real
// run directory directly under root, and its modification time is set to now
// so Expire, which goes by it, does not remove a directory a chat is still
// using. It reports false for a directory that is gone or not a run
// directory.
func Use(root, dir string, now time.Time) bool {
	if dir == "" || !filepath.IsAbs(dir) || !underRoot(root, dir) || !candidate(root, dir) {
		return false
	}
	return os.Chtimes(dir, now, now) == nil
}

// Remove removes the listed run directories under root and the agent CLI
// sessions that ran in them, and ignores anything else.
func Remove(root string, dirs ...string) error {
	var errs []error
	for _, dir := range dirs {
		if dir == "" || !underRoot(root, dir) {
			continue
		}
		if candidate(root, dir) {
			errs = append(errs, removeTree(dir))
		}
		errs = append(errs, RemoveSessions(root, dir))
	}
	return errors.Join(errs...)
}

// PruneVersions removes the run directories of a pull request's versions
// beyond the latest KeepVersions. versions is newest first, as
// store.Store.Versions returns it. A directory a kept version also names stays.
func PruneVersions(root string, versions []store.Metadata) error {
	keep := map[string]bool{}
	var old []string
	for i, m := range versions {
		if m.Session == nil || m.Session.Directory == "" {
			continue
		}
		dir := filepath.Clean(m.Session.Directory)
		if i < KeepVersions {
			keep[dir] = true
		} else {
			old = append(old, dir)
		}
	}
	var remove []string
	for _, dir := range old {
		if !keep[dir] {
			remove = append(remove, dir)
		}
	}
	return Remove(root, remove...)
}

// Expire removes the run directories under root last modified more than
// maxAge before now. A directory of a review still running is being written
// and is far younger.
func Expire(root string, now time.Time, maxAge time.Duration) error {
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var errs []error
	for _, entry := range entries {
		// DirEntry.Type does not follow symlinks.
		if !entry.Type().IsDir() || !strings.HasPrefix(entry.Name(), agent.RunDirPrefix) {
			continue
		}
		info, err := entry.Info()
		if err != nil || now.Sub(info.ModTime()) <= maxAge {
			continue
		}
		dir := filepath.Join(resolvedRoot(root), entry.Name())
		errs = append(errs, removeTree(filepath.Join(root, entry.Name())), RemoveSessions(root, dir))
	}
	return errors.Join(errs...)
}

// Maintain expires old kept directories under root now and then every
// interval until ctx ends. A failure is passed to report (if not nil) and the
// next round tries again.
func Maintain(ctx context.Context, root string, interval time.Duration, report func(error)) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if err := Expire(root, time.Now(), MaxAge); err != nil && report != nil {
			report(err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// removeTree removes dir; the unpacked checkout has read-only directories,
// which are made writable first. WalkDir does not follow symlinks.
func removeTree(dir string) error {
	_ = filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err == nil && entry.IsDir() {
			_ = os.Chmod(path, 0o700)
		}
		return nil
	})
	return os.RemoveAll(dir)
}
