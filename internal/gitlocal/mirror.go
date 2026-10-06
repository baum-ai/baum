package gitlocal

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// emptyConfigName is the empty file a read uses as its global configuration.
const emptyConfigName = "empty-global-config"

// newMirror creates the private bare repository every read runs in: default
// configuration, no refs, an unborn HEAD, and the clone's object directory
// as its only object source (through objects/info/alternates, which git
// follows on to the clone's own alternates). Reading here rather than in the
// clone keeps the clone's configuration, attributes, replace refs, grafts,
// .gitmodules and promisor remote out of every read, as in the hosted bare
// mirror, without overriding them one by one.
func newMirror(tempDir, objects, objectFormat string) (string, error) {
	dir, err := os.MkdirTemp(tempDir, "baum-git-*")
	if err != nil {
		return "", fmt.Errorf("creating private repository: %w", err)
	}
	if dir, err = filepath.EvalSymlinks(dir); err != nil {
		_ = removeAll(dir)
		return "", err
	}
	config := "[core]\n\trepositoryformatversion = 0\n\tbare = true\n"
	switch objectFormat {
	case "sha1":
	case "sha256":
		config = "[core]\n\trepositoryformatversion = 1\n\tbare = true\n[extensions]\n\tobjectformat = sha256\n"
	default:
		_ = removeAll(dir)
		return "", fmt.Errorf("%w: object format %q", ErrInvalidInput, objectFormat)
	}
	// hooks stays empty: the fetch and the worktree checkout point
	// core.hooksPath at it.
	for _, name := range []string{"refs/heads", "refs/tags", "objects/info", "objects/pack", "hooks", "info", "xdg"} {
		if err := os.MkdirAll(filepath.Join(dir, filepath.FromSlash(name)), 0o700); err != nil {
			_ = removeAll(dir)
			return "", fmt.Errorf("creating private repository: %w", err)
		}
	}
	files := map[string]string{
		"HEAD":                    "ref: refs/heads/main\n",
		"config":                  config,
		emptyConfigName:           "",
		"objects/info/alternates": filepath.ToSlash(objects) + "\n",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, filepath.FromSlash(name)), []byte(content), 0o600); err != nil {
			_ = removeAll(dir)
			return "", fmt.Errorf("creating private repository: %w", err)
		}
	}
	return dir, nil
}

// syncShallow copies the clone's shallow file, so history ends where the
// clone's does instead of at a parent the clone never had.
func syncShallow(mirror, shallow string) error {
	content, err := os.ReadFile(shallow)
	target := filepath.Join(mirror, "shallow")
	if errors.Is(err, fs.ErrNotExist) {
		if err := os.Remove(target); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return nil
	}
	if err != nil {
		return err
	}
	return os.WriteFile(target, content, 0o600)
}

// isPartial reports whether the clone holds promisor packs, the mark of a
// partial clone whose missing objects git would fetch on demand.
func isPartial(objects string) bool {
	matches, _ := filepath.Glob(filepath.Join(objects, "pack", "*.promisor"))
	return len(matches) > 0
}
