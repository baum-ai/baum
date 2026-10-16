//go:build windows

package proc

import (
	"errors"
	"os"
	"os/exec"
)

// Configure makes cancelling cmd's context kill its process. Unlike on Unix,
// processes the command started are not killed with it: Windows has no process
// group to signal here, and a job object is not used.
func Configure(cmd *exec.Cmd) {
	cmd.Cancel = func() error {
		err := cmd.Process.Kill()
		if errors.Is(err, os.ErrProcessDone) {
			return nil
		}
		return err
	}
}
