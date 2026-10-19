// Command baum generates and views pull request review documents from
// local coding-agent sessions, the local git clone and GitHub, and carries the
// opt-in hosted service commands (login, setup, upload and the hooks).
package main

import (
	"fmt"
	"os"

	"github.com/baum-ai/baum/internal/cli"
)

func main() {
	if err := cli.Run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr); err != nil {
		if !cli.Quiet(err) {
			fmt.Fprintln(os.Stderr, "baum:", err)
		}
		os.Exit(cli.ExitCode(err))
	}
}
