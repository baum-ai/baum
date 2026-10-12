package gitlocal

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// maxWorktreeListBytes bounds the output of git worktree list.
const maxWorktreeListBytes = 4 << 20

// Worktree is one working tree of a clone.
type Worktree struct {
	// Path is the working tree's top-level directory, cleaned and in this
	// platform's form. It may no longer exist.
	Path string
	// Main is the clone's original working tree, the one that holds the
	// repository; the others are linked worktrees.
	Main bool
	// Prunable is a linked worktree whose directory is gone; git still
	// remembers it until the next prune.
	Prunable bool
}

// Worktrees lists every working tree of the clone containing dir, the main
// one first, linked worktrees after it, including ones whose directory has
// been deleted. A bare main repository has no working tree and is left out.
//
// It reads the clone's worktree bookkeeping and nothing else: finding the
// repository is the one command run with the user's configuration, as Open
// does, and git worktree list then runs against the repository directory with
// the read environment (no system or global configuration, no GIT_ variable
// of the caller's, a home directory with nothing in it). Listing worktrees
// reads no object, runs no hook, filter or helper, and never contacts a
// remote. It needs git 2.36 or newer, for -z.
func Worktrees(ctx context.Context, dir string) ([]Worktree, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	const timeout = DefaultCommandTimeout
	out, err := run(ctx, runSpec{dir: abs, env: userEnv(), limit: 64 << 10, timeout: timeout},
		"rev-parse", "--git-common-dir")
	if err != nil {
		return nil, err
	}
	common := strings.TrimRight(string(out), "\r\n")
	if common == "" {
		return nil, errors.New("listing worktrees: git named no repository directory")
	}
	common = filepath.FromSlash(common)
	if !filepath.IsAbs(common) {
		common = filepath.Join(abs, common)
	}
	common = filepath.Clean(common)
	out, err = run(ctx, runSpec{dir: common, env: worktreeEnv(common), limit: maxWorktreeListBytes, timeout: timeout},
		"worktree", "list", "--porcelain", "-z")
	if err != nil {
		return nil, err
	}
	home, _ := os.UserHomeDir()
	return trustedWorktrees(parseWorktrees(out), common, home), nil
}

// trustedWorktrees keeps the worktrees a caller may treat as part of the
// clone. The list comes from bookkeeping files inside the clone's repository
// directory, which whoever made the clone controls: an entry can name any
// directory at all. A caller that reads everything recorded under these
// directories must not be steered to a home directory or another project, so:
//
//   - no entry may be the file system root or the home directory, and a linked
//     worktree may not contain the repository directory or the main worktree;
//   - a linked worktree whose directory exists must point back at this
//     repository: its .git file names the bookkeeping entry that names it;
//   - a linked worktree whose directory is gone (a deleted worktree git has
//     not pruned yet) cannot be checked that way and is kept, since sessions
//     recorded there while it existed belong to the clone.
func trustedWorktrees(listed []Worktree, common, home string) []Worktree {
	entries := worktreeEntries(common)
	commonPath := canonical(common)
	var mainPath string
	var kept []Worktree
	for _, w := range listed {
		path := canonical(w.Path)
		if isVolumeRoot(path) || (home != "" && samePath(path, canonical(home))) {
			continue
		}
		if w.Main {
			mainPath = path
			kept = append(kept, w)
			continue
		}
		if within(path, commonPath) || (mainPath != "" && within(path, mainPath)) {
			continue
		}
		entry, ok := entries[canonical(filepath.Join(w.Path, ".git"))]
		if !ok {
			continue
		}
		if _, err := os.Lstat(w.Path); err == nil && !pointsBack(w.Path, entry) {
			continue
		}
		kept = append(kept, w)
	}
	return kept
}

// worktreeEntries maps the .git file each bookkeeping entry under
// <common>/worktrees names to the entry's directory.
func worktreeEntries(common string) map[string]string {
	admin := filepath.Join(common, "worktrees")
	list, err := os.ReadDir(admin)
	if err != nil {
		return nil
	}
	entries := make(map[string]string, len(list))
	for _, item := range list {
		dir := filepath.Join(admin, item.Name())
		target, ok := readPointer(filepath.Join(dir, "gitdir"), "")
		if !ok {
			continue
		}
		if !filepath.IsAbs(target) {
			target = filepath.Join(dir, target)
		}
		entries[canonical(target)] = dir
	}
	return entries
}

// pointsBack reports whether the worktree at path has a .git file naming
// entry, the bookkeeping directory that names it.
func pointsBack(path, entry string) bool {
	target, ok := readPointer(filepath.Join(path, ".git"), "gitdir:")
	if !ok {
		return false
	}
	if !filepath.IsAbs(target) {
		target = filepath.Join(path, target)
	}
	return samePath(canonical(target), canonical(entry))
}

// readPointer reads a small regular file holding one path, after prefix.
func readPointer(path, prefix string) (string, bool) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 64<<10 {
		return "", false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	text, ok := strings.CutPrefix(strings.TrimSpace(string(data)), prefix)
	text = strings.TrimSpace(text)
	if !ok || text == "" || strings.ContainsAny(text, "\n\x00") {
		return "", false
	}
	return filepath.FromSlash(text), true
}

// canonical resolves symbolic links in path, or in its nearest existing
// ancestor when path itself is gone, so spellings of one directory compare
// equal.
func canonical(path string) string {
	path = filepath.Clean(path)
	var rest []string
	for p := path; ; {
		if resolved, err := filepath.EvalSymlinks(p); err == nil {
			return filepath.Join(append([]string{resolved}, rest...)...)
		}
		parent := filepath.Dir(p)
		if parent == p {
			return path
		}
		rest = append([]string{filepath.Base(p)}, rest...)
		p = parent
	}
}

// within reports whether path is root or inside it; filepath.Rel ignores case
// where the platform's paths do.
func within(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func samePath(a, b string) bool { return within(a, b) && within(b, a) }

// isVolumeRoot reports a file system root such as / or C:\.
func isVolumeRoot(path string) bool { return filepath.Dir(path) == path }

// worktreeEnv is the read environment pointed at the clone's repository
// directory. The global configuration is the null device rather than a file
// in that directory, which belongs to the clone; exec keeps the last of two
// entries for one variable.
func worktreeEnv(gitDir string) []string {
	return append(mirrorEnv(gitDir), "GIT_CONFIG_GLOBAL="+os.DevNull)
}

// parseWorktrees reads git worktree list --porcelain -z: one record per
// worktree, attributes separated by NUL and records by an empty attribute.
// The first record is the main worktree.
func parseWorktrees(out []byte) []Worktree {
	var worktrees []Worktree
	var current *Worktree
	bare := false
	flush := func() {
		if current != nil && !bare {
			worktrees = append(worktrees, *current)
		}
		current, bare = nil, false
	}
	first := true
	for _, field := range strings.Split(string(out), "\x00") {
		if field == "" {
			flush()
			continue
		}
		name, value, _ := strings.Cut(field, " ")
		switch name {
		case "worktree":
			flush()
			if value == "" {
				continue
			}
			current = &Worktree{Path: filepath.Clean(filepath.FromSlash(value)), Main: first}
			first = false
		case "bare":
			bare = true
		case "prunable":
			if current != nil {
				current.Prunable = true
			}
		}
	}
	flush()
	return worktrees
}
