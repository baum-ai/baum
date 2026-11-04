package gitlocal

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// CheckoutStamp describes, without running git, what the checkout containing
// dir has checked out and how it is configured: what HEAD names, the commit
// of the branch it names, and the size and modification time of the common
// directory's config, where remotes and upstreams live. It changes with a
// checkout, a commit, a reset or a remote or upstream edit; "git status",
// which editors run all the time and which rewrites the index, leaves it
// alone. root is the checkout's top directory. ok is false when dir is not
// in a checkout this can read; callers then ask git.
func CheckoutStamp(dir string) (stamp, root string, ok bool) {
	root, ok = checkoutRoot(dir)
	if !ok {
		return "", "", false
	}
	dotGit := filepath.Join(root, ".git")
	info, err := os.Stat(dotGit)
	if err != nil {
		return "", "", false
	}
	gitDir := dotGit
	if !info.IsDir() {
		data, err := os.ReadFile(dotGit)
		if err != nil {
			return "", "", false
		}
		target, found := strings.CutPrefix(strings.TrimSpace(string(data)), "gitdir:")
		if !found {
			return "", "", false
		}
		gitDir = strings.TrimSpace(target)
		if !filepath.IsAbs(gitDir) {
			gitDir = filepath.Join(root, gitDir)
		}
	}
	head, err := os.ReadFile(filepath.Join(gitDir, "HEAD"))
	if err != nil {
		return "", "", false
	}
	commonDir := gitDir
	if data, err := os.ReadFile(filepath.Join(gitDir, "commondir")); err == nil {
		commonDir = strings.TrimSpace(string(data))
		if !filepath.IsAbs(commonDir) {
			commonDir = filepath.Join(gitDir, commonDir)
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s\x00%s", gitDir, strings.TrimSpace(string(head)))
	if config, err := os.Stat(filepath.Join(commonDir, "config")); err == nil {
		fmt.Fprintf(&b, "\x00config\x00%d\x00%d", config.Size(), config.ModTime().UnixNano())
	}
	ref, symbolic := strings.CutPrefix(strings.TrimSpace(string(head)), "ref:")
	if !symbolic {
		return b.String(), root, true
	}
	ref = filepath.FromSlash(strings.TrimSpace(ref))
	if !filepath.IsLocal(ref) {
		return "", "", false
	}
	// A loose ref holds the branch's commit; a packed one is in packed-refs.
	if data, err := os.ReadFile(filepath.Join(commonDir, ref)); err == nil {
		fmt.Fprintf(&b, "\x00%s", strings.TrimSpace(string(data)))
		return b.String(), root, true
	}
	packed, err := os.Stat(filepath.Join(commonDir, "packed-refs"))
	if err != nil {
		// An unborn branch has no commit yet.
		return b.String() + "\x00unborn", root, true
	}
	fmt.Fprintf(&b, "\x00packed\x00%d\x00%d", packed.Size(), packed.ModTime().UnixNano())
	return b.String(), root, true
}

// checkoutRoot is the nearest directory at or above dir holding a .git entry.
func checkoutRoot(dir string) (string, bool) {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return "", false
	}
	for {
		if _, err := os.Lstat(filepath.Join(dir, ".git")); err == nil {
			return dir, true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		dir = parent
	}
}
