package store

import (
	"io/fs"
	"os"
)

// writable reports, as a *fs.PathError, why the existing file at path could
// not be opened for writing: on Windows, its read-only attribute.
func writable(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if info.Mode().Perm()&0o200 == 0 {
		return &fs.PathError{Op: "open", Path: path, Err: fs.ErrPermission}
	}
	return nil
}
