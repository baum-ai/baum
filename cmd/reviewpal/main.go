// Command reviewpal generates and views pull request review documents from
// local coding-agent sessions, the local git clone and GitHub.
package main

import (
	"fmt"
	"os"

	"github.com/lastpersonlabs/reviewpal/internal/cli"
)

func main() {
	if err := cli.Run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "reviewpal:", err)
		os.Exit(1)
	}
}
