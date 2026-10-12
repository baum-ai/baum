package gitlocal

import (
	"context"
	"errors"
	"os/exec"
	"strings"
)

// Remote is a remote of the clone and one URL it is configured with.
type Remote struct {
	Name string
	URL  string
}

// RemoteURLs lists every configured remote URL of the clone exactly as written
// in its configuration, in configuration order; a remote with several URLs
// appears once for each. It reads configuration only and never contacts a
// remote.
func (r *Repo) RemoteURLs(ctx context.Context) ([]Remote, error) {
	done, err := r.begin()
	if err != nil {
		return nil, err
	}
	defer done()
	out, err := run(ctx, runSpec{dir: r.dir, env: userEnv(), limit: 256 << 10, timeout: r.opts.CommandTimeout},
		"config", "--get-regexp", `^remote\..*\.url$`)
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
			return nil, nil // no remote has a URL
		}
		return nil, err
	}
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
	return remotes, nil
}
