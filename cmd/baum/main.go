// Command baum runs the desktop daemon and local coding-agent hooks.
package main

import (
	"fmt"
	"os"

	"github.com/baum-ai/baum/internal/cli"
	"github.com/baum-ai/baum/internal/sqlitepool"
)

func main() {
	// One handle per SQLite file for the process's life: the daemon reads
	// and writes app.sqlite and the review stores many times a second.
	sqlitepool.Enable()
	if err := cli.Run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "baum:", err)
		os.Exit(cli.ExitCode(err))
	}
}
