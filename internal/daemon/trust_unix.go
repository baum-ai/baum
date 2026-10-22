//go:build unix

package daemon

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
)

// secure requires the executable to be executable, and it and every
// directory above it to be owned by this user or root and writable by no one
// else. A sticky, world-writable directory such as /tmp is refused too.
func secure(path string, info os.FileInfo, top string) error {
	if info.Mode().Perm()&0o111 == 0 {
		return fmt.Errorf("%s is not executable", path)
	}
	if err := ownedSafely(path, info); err != nil {
		return err
	}
	for dir := filepath.Dir(path); ; dir = filepath.Dir(dir) {
		info, err := os.Lstat(dir)
		if err != nil {
			return err
		}
		if !info.IsDir() {
			return fmt.Errorf("%s is not a directory", dir)
		}
		if err = ownedSafely(dir, info); err != nil {
			return err
		}
		if dir == top || dir == filepath.Dir(dir) {
			return nil
		}
	}
}

// On macOS, /Applications (where apps are installed, including the desktop
// app that bundles baum) and Intel Homebrew's /usr/local/bin are directories
// of root or the user, group-writable by the admin group. The admin group's
// members can become root with their own password, so a directory only they
// can write admits no one who could not already replace a root-owned file:
// accepting it adds no attacker beyond those a directory of the user admits.
// The staff group (every local user) gets no such allowance, nor does a file,
// and a world-writable or sticky directory is still refused.
var (
	adminGroupDirectories = runtime.GOOS == "darwin"
	adminGID              = uint32(80)
)

// staffGID is macOS's group of every local user.
const staffGID = 20

func ownedSafely(path string, info os.FileInfo) error {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("the owner of %s is unknown", path)
	}
	if st.Uid != 0 && int(st.Uid) != os.Getuid() {
		return fmt.Errorf("%s is owned by another user", path)
	}
	perm := info.Mode().Perm()
	if perm&0o022 == 0 {
		return nil
	}
	if adminGroupDirectories && adminWritableOnly(info.IsDir(), st.Gid, info.Mode()) {
		return nil
	}
	return fmt.Errorf("%s is group- or world-writable", path)
}

// adminWritableOnly reports whether a directory is writable by its owner and
// the admin group only.
func adminWritableOnly(dir bool, gid uint32, mode os.FileMode) bool {
	return dir &&
		gid == adminGID && gid != staffGID &&
		mode&os.ModeSticky == 0 &&
		mode.Perm()&0o002 == 0
}
