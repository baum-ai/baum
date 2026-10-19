//go:build !windows

package privatefs

import (
	"errors"
	"os"
	"syscall"
)

// ProtectDirectory checks that path is a directory owned by the current user
// with no group or other permissions.
func ProtectDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return errors.New("directory must be private (0700): " + path)
	}
	if !owned(info) {
		return errors.New("directory must belong to the current user: " + path)
	}
	return nil
}

// PrivateFile reports whether info describes a regular file owned by the
// current user with no group or other permissions.
func PrivateFile(_ string, info os.FileInfo) bool {
	return info.Mode().IsRegular() && info.Mode().Perm()&0077 == 0 && owned(info)
}

func owned(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && int(stat.Uid) == os.Getuid()
}
