// Command baum runs the desktop daemon and local coding-agent hooks.
package main

import (
	"fmt"
	"os"

	"github.com/baum-ai/baum/internal/cli"
)

func main() {
	if err := cli.Run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "baum:", err)
		os.Exit(cli.ExitCode(err))
	}
}
