package gitlocal

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Light is what Probe learns about a directory: the working tree it belongs
// to and the remote URLs configured there.
type Light struct {
	// Top is the working tree's top-level directory and CommonDir the
	// repository directory shared by its linked worktrees, both absolute and
	// cleaned.
	Top, CommonDir string
	// Remotes are the configured remote URLs, as written, in configuration
	// order.
	Remotes []Remote
}

// Probe finds the working tree containing dir and reads its remote URLs with
// two git commands, each bounded by timeout. Unlike Open it creates no private
// repository, so a scan can afford it for many directories. It reads
// configuration only and never contacts a remote.
func Probe(ctx context.Context, dir string, timeout time.Duration) (Light, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return Light{}, err
	}
	out, err := run(ctx, runSpec{dir: abs, env: userEnv(), limit: 16 << 10, timeout: timeout},
		"rev-parse", "--show-toplevel", "--git-common-dir")
	if err != nil {
		return Light{}, err
	}
	fields := strings.Split(strings.TrimRight(string(out), "\r\n"), "\n")
	if len(fields) != 2 || fields[0] == "" || fields[1] == "" {
		return Light{}, errors.New("probing repository: unexpected rev-parse output")
	}
	common := filepath.FromSlash(fields[1])
	if !filepath.IsAbs(common) {
		common = filepath.Join(abs, common)
	}
	light := Light{Top: filepath.Clean(filepath.FromSlash(fields[0])), CommonDir: filepath.Clean(common)}
	out, err = run(ctx, runSpec{dir: light.Top, env: userEnv(), limit: 256 << 10, timeout: timeout},
		"config", "--get-regexp", `^remote\..*\.url$`)
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
			return light, nil // no remote has a URL
		}
		return Light{}, err
	}
	light.Remotes = parseRemotes(out)
	return light, nil
}

// parseRemotes reads the output of git config --get-regexp for remote URLs.
func parseRemotes(out []byte) []Remote {
	var remotes []Remote
	for _, line := range strings.Split(string(out), "\n") {
		key, value, found := strings.Cut(line, " ")
		name, ok := strings.CutPrefix(key, "remote.")
		if !found || !ok {
			continue
		}
		if name, ok = strings.CutSuffix(name, ".url"); !ok || name == "" {
			continue
		}
		remotes = append(remotes, Remote{Name: name, URL: strings.TrimSpace(value)})
	}
	return remotes
}
