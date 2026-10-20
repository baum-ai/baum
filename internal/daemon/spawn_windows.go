package daemon

import (
	"errors"
	"os"
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

// detach starts the daemon without a console, in its own process group, so
// closing the caller's console or pressing Ctrl-C there does not stop it.
func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: windows.DETACHED_PROCESS | windows.CREATE_NEW_PROCESS_GROUP | windows.CREATE_BREAKAWAY_FROM_JOB,
		HideWindow:    true,
	}
}

// startDetached also breaks away from the caller's job object, so a hook
// runner that kills its job does not kill the daemon. A job that forbids
// breaking away refuses that flag; the daemon then starts inside it.
func startDetached(cmd *exec.Cmd) (*exec.Cmd, error) {
	err := cmd.Start()
	if err == nil || !errors.Is(err, windows.ERROR_ACCESS_DENIED) {
		return cmd, err
	}
	retry := exec.Command(cmd.Path, cmd.Args[1:]...)
	retry.Env, retry.Dir, retry.Stdin, retry.Stdout, retry.Stderr = cmd.Env, cmd.Dir, cmd.Stdin, cmd.Stdout, cmd.Stderr
	retry.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.DETACHED_PROCESS | windows.CREATE_NEW_PROCESS_GROUP, HideWindow: true}
	return retry, retry.Start()
}

// openOutput creates a daemon's output file, replacing an older one, shared
// for deletion so the next Spawn can rotate it while this daemon still writes
// to it, as on Unix. Windows refuses to rename a file open without that
// sharing, which os.OpenFile does not grant: a daemon restarting into a newer
// version spawns its successor while its own output is still OutputFile, and a
// client may start a daemon while the one it stopped, which has released its
// lock, is still exiting. The file inherits the private state directory's
// DACL, as os.OpenFile's would.
func openOutput(path string) (*os.File, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	h, err := windows.CreateFile(name, windows.GENERIC_WRITE, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.CREATE_ALWAYS, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	return os.NewFile(uintptr(h), path), nil
}

// refused reports a dial error meaning nothing listens on the socket.
func refused(err error) bool {
	return errors.Is(err, windows.WSAECONNREFUSED) || errors.Is(err, windows.ERROR_FILE_NOT_FOUND) || errors.Is(err, windows.ERROR_PATH_NOT_FOUND)
}
