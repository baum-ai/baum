//go:build !windows

package livesessions

import (
	"errors"
	"syscall"
)

// running reports whether a process pid exists (one of another user's counts:
// it is not signalled, only asked after).
func running(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// terminate asks process pid to exit.
func terminate(pid int) error {
	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
		if errors.Is(err, syscall.ESRCH) {
			return ErrAgentGone
		}
		return err
	}
	return nil
}
