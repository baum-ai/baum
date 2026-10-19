package daemon

import (
	"errors"
	"os"
	"os/exec"
	"strconv"
	"testing"

	"github.com/baum-ai/baum/internal/privatefs"
)

// SpawnSelf starts this executable as "baum daemon run --detached", detached
// from the caller: in its own session (Unix) or as a detached process group
// (Windows), with no standard input and its output in OutputFile. The daemon
// resolves its own environment, so the caller's is passed unchanged.
func SpawnSelf(paths Paths, start StartOptions) (<-chan struct{}, error) {
	// A test binary started as "daemon run" would run its tests again,
	// each of which might start another; tests start daemons through Spawn.
	if testing.Testing() {
		return nil, errors.New("a test binary does not start itself as the baum daemon")
	}
	self, err := os.Executable()
	if err != nil {
		return nil, err
	}
	args := []string{"daemon", "run", "--detached"}
	if start.Port != 0 {
		args = append(args, "--port", strconv.Itoa(start.Port))
	}
	if start.Concurrency != 0 {
		args = append(args, "--concurrency", strconv.Itoa(start.Concurrency))
	}
	return Spawn(paths, exec.Command(self, args...))
}

// Spawn starts cmd as a detached daemon process with its output in
// OutputFile. The returned channel is closed when the process exits.
func Spawn(paths Paths, cmd *exec.Cmd) (<-chan struct{}, error) {
	if err := privatefs.MkdirAll(paths.State); err != nil {
		return nil, err
	}
	if err := rotateOutput(paths); err != nil {
		return nil, err
	}
	out, err := os.OpenFile(paths.OutputFile(), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return nil, err
	}
	defer func() { _ = out.Close() }()
	cmd.Stdin = nil
	cmd.Stdout = out
	cmd.Stderr = out
	if cmd.Dir == "" {
		cmd.Dir = home()
	}
	detach(cmd)
	if cmd, err = startDetached(cmd); err != nil {
		return nil, err
	}
	exited := make(chan struct{})
	// Waiting only reaps the process if it exits while the caller runs; the
	// daemon keeps running after the caller exits.
	go func() { _ = cmd.Wait(); close(exited) }()
	return exited, nil
}
