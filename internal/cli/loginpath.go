package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/baum-ai/baum/internal/shellenv"
)

// versionTimeout bounds `baum --version` of another baum found on PATH.
const loginPathVersionTimeout = 5 * time.Second

// loginPath is what `baum daemon login-path --json` prints: the PATH a daemon
// started now would use (the login shell's, as the daemon resolves it) and the
// baum the agent hooks find on it. The desktop app reads it to decide where
// to install the baum command and whether a different baum is in the way.
type loginPath struct {
	// Shell is the login shell that was asked, when one was found.
	Shell string `json:"shell,omitempty"`
	// LoginShell reports that the PATH is the login shell's; otherwise it is
	// the inherited one (always on Windows, which has no login shell).
	LoginShell bool `json:"login_shell"`
	// Error is why the login shell could not be read.
	Error string    `json:"error,omitempty"`
	Path  []string  `json:"path"`
	Baum  *pathBaum `json:"baum"`
}

// pathBaum is the first baum on the PATH.
type pathBaum struct {
	Path string `json:"path"`
	// Resolved is Path with its symbolic links resolved: the file that runs.
	Resolved string `json:"resolved"`
	// Version is what its --version prints, or empty when that failed.
	Version string `json:"version,omitempty"`
	// Self reports that it is this executable.
	Self bool `json:"self"`
}

func newLoginPathCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "login-path",
		Short: "Print your login shell's PATH and the baum on it (used by the desktop app)",
		Long: `Login-path reads the PATH your login shell sets up, as the daemon does when it
starts, and finds the first baum on it, which is the one coding-agent hooks
run. It prints the PATH entries, that baum's path, the file it resolves to and
its version. It changes nothing.`,
		Args:   cobra.NoArgs,
		Hidden: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			asJSON, _ := cmd.Flags().GetBool("json")
			report := readLoginPath(cmd.Context())
			if asJSON {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(report)
			}
			out := cmd.OutOrStdout()
			if report.Error != "" {
				_, _ = fmt.Fprintf(out, "login shell not read (%s); using the inherited PATH\n", report.Error)
			}
			_, _ = fmt.Fprintf(out, "PATH: %s\n", strings.Join(report.Path, string(os.PathListSeparator)))
			if report.Baum == nil {
				_, _ = fmt.Fprintln(out, "baum: not found on PATH")
				return nil
			}
			_, _ = fmt.Fprintf(out, "baum: %s (%s), version %s\n", report.Baum.Path, report.Baum.Resolved, orUnknownVersion(report.Baum.Version))
			return nil
		},
	}
	cmd.Flags().Bool("json", false, "print the result as one JSON line")
	return cmd
}

func orUnknownVersion(v string) string {
	if v == "" {
		return "unknown"
	}
	return v
}

// readLoginPath resolves the login shell's environment the way the daemon's
// start does (shellenv.Merge over the inherited environment) and looks for
// baum on its PATH.
func readLoginPath(ctx context.Context) loginPath {
	inherited := os.Environ()
	result, err := resolveShell(ctx, shellenv.Options{Environ: inherited})
	report := loginPath{Shell: result.Shell, Path: []string{}}
	var resolved map[string]string
	switch {
	case err == nil:
		resolved = result.Env
		report.LoginShell = true
	case errors.Is(err, shellenv.ErrUnsupported):
	default:
		report.Error = err.Error()
	}
	for _, entry := range shellenv.Merge(inherited, resolved) {
		name, value, _ := strings.Cut(entry, "=")
		if pathVariable(name) {
			for _, dir := range filepath.SplitList(value) {
				if dir != "" {
					report.Path = append(report.Path, dir)
				}
			}
		}
	}
	report.Baum = findBaum(ctx, report.Path)
	return report
}

func pathVariable(name string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(name, "PATH")
	}
	return name == "PATH"
}

// findBaum returns the first executable baum in dirs, as a shell's command
// lookup finds it; relative entries are skipped.
func findBaum(ctx context.Context, dirs []string) *pathBaum {
	self := ""
	if exe, err := os.Executable(); err == nil {
		self, _ = filepath.EvalSymlinks(exe)
	}
	for _, dir := range dirs {
		if !filepath.IsAbs(dir) {
			continue
		}
		path, err := exec.LookPath(filepath.Join(dir, "baum"))
		if err != nil {
			continue
		}
		resolved, err := filepath.EvalSymlinks(path)
		if err != nil {
			continue
		}
		found := &pathBaum{Path: path, Resolved: resolved}
		if self != "" && sameFile(self, resolved) {
			found.Self, found.Version = true, version()
			return found
		}
		found.Version = baumVersion(ctx, path)
		return found
	}
	return nil
}

func sameFile(a, b string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

// baumVersion runs path --version and returns its version: the last word of
// "baum version v0.3.0".
func baumVersion(ctx context.Context, path string) string {
	ctx, cancel := context.WithTimeout(ctx, loginPathVersionTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "--version")
	cmd.WaitDelay = time.Second
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	line, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")
	fields := strings.Fields(line)
	if len(fields) == 0 || len(fields) > 4 {
		return ""
	}
	return fields[len(fields)-1]
}
