package cursor

import (
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/lastpersonlabs/baum/sessions/cursortranscript"
)

// File is one concrete Cursor state.vscdb on disk.
type File struct {
	Path       string
	ModifiedAt time.Time
	SizeBytes  int64
}

// ListFiles expands storePaths into concrete state.vscdb files. A file path is
// used as-is; a directory is searched for stores. Empty storePaths scans the
// platform's default Cursor user-data directories.
func ListFiles(storePaths []string) []File {
	paths := storePaths
	if len(paths) == 0 {
		paths = defaultStorePaths()
	}
	var files []File
	seen := map[string]bool{}
	add := func(path string) {
		info, err := os.Stat(path)
		if err != nil || info.IsDir() || seen[path] {
			return
		}
		seen[path] = true
		files = append(files, File{
			Path:       path,
			ModifiedAt: info.ModTime().UTC(),
			SizeBytes:  info.Size(),
		})
	}
	for _, path := range paths {
		info, err := os.Stat(path)
		if err != nil {
			continue
		}
		if !info.IsDir() {
			add(path)
			continue
		}
		for _, found := range storesUnder(path) {
			add(found)
		}
	}
	return files
}

// sessionStorePaths turns FindSessions overrides into concrete DB paths.
// Empty means the default global Cursor store (then expanded like ListFiles).
func sessionStorePaths(storePaths []string) []string {
	if len(storePaths) == 0 {
		storePaths = []string{cursortranscript.DefaultStorePath()}
	}
	files := ListFiles(storePaths)
	paths := make([]string, len(files))
	for i, file := range files {
		paths[i] = file.Path
	}
	return paths
}

// defaultStorePaths returns the Cursor state stores present in this user's
// default Cursor directories: the global store plus one per workspace.
func defaultStorePaths() []string {
	var paths []string
	for _, root := range userDataDirs() {
		paths = append(paths, storesUnder(root)...)
	}
	return paths
}

// storesUnder finds the state stores inside one Cursor user-data directory,
// or returns the directory's own store when it is a single workspace-storage
// folder.
func storesUnder(root string) []string {
	var paths []string
	if direct := filepath.Join(root, "state.vscdb"); fileExists(direct) {
		paths = append(paths, direct)
	}
	if global := filepath.Join(root, "User", "globalStorage", "state.vscdb"); fileExists(global) {
		paths = append(paths, global)
	}
	for _, workspaceRoot := range []string{
		filepath.Join(root, "User", "workspaceStorage"),
		root,
	} {
		entries, err := os.ReadDir(workspaceRoot)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			store := filepath.Join(workspaceRoot, entry.Name(), "state.vscdb")
			if fileExists(store) && !slices.Contains(paths, store) {
				paths = append(paths, store)
			}
		}
	}
	return paths
}

// userDataDirs lists this platform's candidate Cursor user-data roots.
// Cursor follows VS Code's layout, which differs per platform and across
// Cursor's own packaging, so every plausible location is probed and missing
// ones are skipped.
func userDataDirs() []string {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	candidates := []string{
		filepath.Join(home, "Library", "Application Support", "Cursor"), // macOS
		filepath.Join(home, ".config", "Cursor"),                        // Linux
	}
	if appData := os.Getenv("APPDATA"); appData != "" { // Windows
		candidates = append(candidates, filepath.Join(appData, "Cursor"))
	}
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		candidates = append(candidates, filepath.Join(xdg, "Cursor"))
	}

	var dirs []string
	seen := map[string]bool{}
	for _, dir := range candidates {
		if seen[dir] {
			continue
		}
		seen[dir] = true
		if info, err := os.Stat(dir); err == nil && info.IsDir() {
			dirs = append(dirs, dir)
		}
	}
	return dirs
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
