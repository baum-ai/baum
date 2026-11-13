//go:build !windows

package store

import (
	"io/fs"

	"golang.org/x/sys/unix"
)

// writable reports, as a *fs.PathError, why this process could not open the
// existing file at path for writing: its permissions, its owner, a read-only
// mount. It opens no descriptor, whose closing would drop this process's
// SQLite locks on the file.
func writable(path string) error {
	if err := unix.Access(path, unix.W_OK); err != nil {
		return &fs.PathError{Op: "open", Path: path, Err: err}
	}
	return nil
}
