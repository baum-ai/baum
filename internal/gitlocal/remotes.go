package gitlocal

import (
	"context"
	"errors"
	"net/url"
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

// RemoteRepository returns the host and the lower-case "owner/name" a git
// remote URL names, and false for a URL that does not name one. It reads the
// https, http, ssh and git forms, the scp-like "user@host:owner/name" form, a
// ".git" suffix and trailing slashes, and ignores credentials and port. A
// local path or file URL names no hosted repository. The host is lower-case
// and may be an SSH alias rather than a real host name.
func RemoteRepository(remote string) (host, repository string, ok bool) {
	remote = strings.TrimSpace(remote)
	var path string
	if scheme, _, found := strings.Cut(remote, "://"); found {
		switch strings.ToLower(scheme) {
		case "https", "http", "ssh", "git", "git+ssh", "ssh+git":
		default:
			return "", "", false
		}
		parsed, err := url.Parse(remote)
		if err != nil || parsed.Host == "" {
			return "", "", false
		}
		host, path = parsed.Hostname(), parsed.Path
	} else {
		// scp-like: [user@]host:path. A single letter before the colon is a
		// Windows drive, and a slash before it makes the whole a local path.
		authority, rest, found := strings.Cut(remote, ":")
		if !found || len(authority) < 2 || strings.ContainsAny(authority, `/\`) {
			return "", "", false
		}
		host, path = authority, rest
		if _, after, found := strings.Cut(authority, "@"); found {
			host = after
		}
	}
	path = strings.TrimSuffix(strings.Trim(path, "/"), ".git")
	path = strings.Trim(path, "/")
	parts := strings.Split(path, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", false
	}
	return strings.ToLower(host), strings.ToLower(parts[0] + "/" + parts[1]), true
}

// NormalizeRepository returns the lower-case "owner/name" a git remote URL
// names, ignoring its host (see RemoteRepository), and false for a URL that
// does not name one.
func NormalizeRepository(remote string) (string, bool) {
	_, repository, ok := RemoteRepository(remote)
	return repository, ok
}

// SameRepository reports whether the remote URL names the repository
// "owner/name", ignoring case.
func SameRepository(remote, repository string) bool {
	got, ok := NormalizeRepository(remote)
	return ok && got == strings.ToLower(strings.TrimSpace(repository))
}

// CurrentBranch is the name of the branch HEAD is on, without "refs/heads/",
// or "" when HEAD is detached.
func (r *Repo) CurrentBranch(ctx context.Context) (string, error) {
	done, err := r.begin()
	if err != nil {
		return "", err
	}
	defer done()
	out, err := run(ctx, runSpec{dir: r.dir, env: userEnv(), limit: 8 << 10, timeout: r.opts.CommandTimeout},
		"symbolic-ref", "--quiet", "HEAD")
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
			return "", nil // detached
		}
		return "", err
	}
	return strings.TrimPrefix(strings.TrimSpace(string(out)), "refs/heads/"), nil
}
