// Package fixdir places the checkouts of fixes (internal/fix) by the clone
// they fix. A fix agent's session is recorded in its checkout, which is
// removed when the fix ends; the clone's directory of fixes is where the local
// linkage (internal/locallink) still finds that session.
package fixdir

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
)

// Default is where fixes check out: fixes under the user's cache directory,
// outside every clone and outside the system temporary directory, which a
// Codex fix may not write to.
func Default() (string, error) {
	root, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "baum", "fixes"), nil
}

// ForClone is the directory under root holding the fixes of the clone whose
// common git directory is common (gitlocal.CommonDir), each in a directory of
// its own. It is named by a digest of that path with its symbolic links
// resolved, so it is the same from every worktree of the clone and holds only
// letters and digits, which agents keep as they are when they name a
// session's directory.
func ForClone(root, common string) string {
	if resolved, err := filepath.EvalSymlinks(common); err == nil {
		common = resolved
	}
	sum := sha256.Sum256([]byte(filepath.Clean(common)))
	return filepath.Join(root, hex.EncodeToString(sum[:8]))
}
