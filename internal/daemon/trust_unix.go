//go:build unix

package daemon

import (
	"fmt"
	"os"
	"path/filepath"
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

func ownedSafely(path string, info os.FileInfo) error {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("the owner of %s is unknown", path)
	}
	if st.Uid != 0 && int(st.Uid) != os.Getuid() {
		return fmt.Errorf("%s is owned by another user", path)
	}
	if info.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("%s is group- or world-writable", path)
	}
	return nil
}
