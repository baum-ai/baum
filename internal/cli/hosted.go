package cli

import (
	"errors"
	"io"

	"github.com/spf13/cobra"

	"github.com/baum-ai/baum/hosted/baumcli"
)

// localGroupID is the help section of the commands that work on this machine
// alone; the hosted service commands have their own.
const localGroupID = "local"

// addHostedCommands lists every command already on root as local, then adds
// the hosted service commands (login, logout, whoami, setup, upload and the
// hidden hook dispatchers) in a "Hosted service" section. None of them sends
// anything to a baum server before `baum login`.
func addHostedCommands(root *cobra.Command, stdout, stderr io.Writer) {
	root.Long += `

The hosted service commands connect this machine to a baum server. They are
opt-in: nothing is sent to a baum server before ` + "`baum login`" + `.`
	root.AddGroup(&cobra.Group{ID: localGroupID, Title: "Local commands:"})
	for _, cmd := range root.Commands() {
		if cmd.GroupID == "" {
			cmd.GroupID = localGroupID
		}
	}
	baumcli.AddCommands(root, version(), stdout, stderr)
}

// hookExitCode is the status a delegated agent hook exited with, which the
// process must exit with too.
func hookExitCode(err error) (int, bool) {
	var exitErr *baumcli.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.Code, true
	}
	return 0, false
}

// Quiet reports whether err must not be printed: a delegated agent hook's
// status, whose output belongs to the agent.
func Quiet(err error) bool {
	_, ok := hookExitCode(err)
	return ok
}
