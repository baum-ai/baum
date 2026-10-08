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
	return parseWorktrees(out), nil
}

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
