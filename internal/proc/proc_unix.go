//go:build unix

// Package proc makes cancelling a command stop its descendants where the
// platform allows.
package proc

import (
	"errors"
	"os/exec"
	"syscall"
)

// Configure starts cmd in its own process group and makes cancelling its
// context kill the whole group, so subprocesses it spawned stop too.
func Configure(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return nil
		}
		return err
	}
}
