//go:build unix

package daemon

import (
	"errors"
	"os/exec"
	"syscall"
)

// detach starts the daemon in a new session: no controlling terminal, and a
// terminal's hangup or interrupt for the caller's process group never
// reaches it.
func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}

func startDetached(cmd *exec.Cmd) (*exec.Cmd, error) { return cmd, cmd.Start() }

// refused reports a dial error meaning nothing listens on the socket.
func refused(err error) bool {
	return errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.ENOENT)
}
