//go:build unix

package shellenv

import (
	"bufio"
	"context"
	"errors"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// detach runs the shell in a new session with no controlling terminal, so an
// interactive shell cannot take over the terminal baum was started from, and
// makes cancellation kill everything its startup files started.
func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Cancel = func() error {
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return nil
		}
		return err
	}
}

// killGroup kills what remains of the shell's session after it exited.
func killGroup(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}

// registryPath is Windows only.
func registryPath() (string, error) { return "", ErrUnsupported }

// detectShell is $SHELL when it is absolute, else the user's shell from the
// user database.
func detectShell(fromEnv string) string {
	if filepath.IsAbs(fromEnv) {
		return fromEnv
	}
	if runtime.GOOS == "darwin" {
		if u, err := user.Current(); err == nil {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			out, err := exec.CommandContext(ctx, "/usr/bin/dscl", ".", "-read", "/Users/"+u.Username, "UserShell").Output()
			if err == nil {
				if _, shell, ok := strings.Cut(strings.TrimSpace(string(out)), ":"); ok {
					return strings.TrimSpace(shell)
				}
			}
		}
		return ""
	}
	return passwdShell("/etc/passwd", os.Getuid())
}

func passwdShell(path string, uid int) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer func() { _ = f.Close() }()
	want := strconv.Itoa(uid)
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		fields := strings.Split(scanner.Text(), ":")
		if len(fields) == 7 && fields[2] == want {
			return fields[6]
		}
	}
	return ""
}
