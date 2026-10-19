// Package privatefs creates and checks files and directories only the current
// user may use: mode 0600/0700 and owned by the user on Unix, a protected
// current-user DACL on Windows.
package privatefs

import "os"

// MkdirAll creates path and its missing parents with mode 0700 and checks
// that path itself is private.
func MkdirAll(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return err
	}
	return ProtectDirectory(path)
}
