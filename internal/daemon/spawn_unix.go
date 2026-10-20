//go:build unix

package daemon

import (
	"errors"
	"os"
	"os/exec"
	"strconv"
	"syscall"
)

// detach starts the daemon in a new session: no controlling terminal, and a
// terminal's hangup or interrupt for the caller's process group never
// reaches it.
func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}

func startDetached(cmd *exec.Cmd) (*exec.Cmd, error) { return cmd, cmd.Start() }

// closeInherited marks every descriptor above standard error close-on-exec.
// Go opens its own that way, but a hook inherits whatever the agent or Git
// left open, and a long-lived daemon must not keep their pipes open.
func closeInherited() {
	entries, err := os.ReadDir("/dev/fd")
	if err != nil {
		return
	}
	for _, entry := range entries {
		if fd, err := strconv.Atoi(entry.Name()); err == nil && fd > 2 {
			syscall.CloseOnExec(fd)
		}
	}
}

// refused reports a dial error meaning nothing listens on the socket.
func refused(err error) bool {
	return errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.ENOENT)
}
