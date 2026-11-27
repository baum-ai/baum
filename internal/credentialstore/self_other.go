//go:build !linux

package credentialstore

import "os"

// selfExecutable is the running program's own executable.
func selfExecutable() (string, error) { return os.Executable() }
