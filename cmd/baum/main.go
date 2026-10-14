// Command baum generates and views pull request review documents from
// local coding-agent sessions, the local git clone and GitHub.
package main

import (
	"fmt"
	"os"

	"github.com/baumai/baum/internal/cli"
)

func main() {
	if err := cli.Run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "baum:", err)
		os.Exit(cli.ExitCode(err))
	}
}
