package main

import (
	"errors"
	"flag"
	"fmt"
	"io"

	"github.com/baum-ai/baum/internal/replay"
)

// runMetrics implements `replay metrics <run-dir> [--compare <other-run-dir>]`.
func runMetrics(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("replay metrics", flag.ContinueOnError)
	fs.SetOutput(stderr)
	compare := fs.String("compare", "", "the baseline run directory to compare with: deltas are this run minus the baseline")
	noWrite := fs.Bool("no-write", false, "print only; do not write metrics.json (and compare.json) into the run directory")
	// The run directory may come before or after the flags, and is the only
	// argument that is not one.
	var dir string
	for rest := args; ; {
		if err := fs.Parse(rest); err != nil {
			return err
		}
		if fs.NArg() == 0 {
			break
		}
		if dir != "" {
			return fmt.Errorf("unexpected argument %q: replay metrics takes one run directory and flags (write a flag's value as --flag=value)", fs.Arg(0))
		}
		dir, rest = fs.Arg(0), fs.Args()[1:]
		if len(rest) == 0 {
			break
		}
	}
	if dir == "" {
		return errors.New("usage: replay metrics <run-dir> [--compare <other-run-dir>]")
	}
	metrics, err := replay.ReadMetrics(dir)
	if err != nil {
		return err
	}
	metrics.Print(stdout)
	if !*noWrite {
		if err := replay.WriteMetrics(dir, metrics); err != nil {
			return err
		}
		_, _ = fmt.Fprintf(stdout, "\nwrote %s/metrics.json\n", dir)
	}
	if *compare == "" {
		return nil
	}
	other, err := replay.ReadMetrics(*compare)
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintln(stdout)
	comparison := replay.Compare(metrics, other)
	comparison.Print(stdout)
	if !*noWrite {
		if err := replay.WriteComparison(dir, comparison); err != nil {
			return err
		}
		_, _ = fmt.Fprintf(stdout, "\nwrote %s/compare.json\n", dir)
	}
	return nil
}
