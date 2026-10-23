package gitlocal

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// DefaultCloneTimeout bounds one clone.
const DefaultCloneTimeout = 30 * time.Minute

var (
	cloneOwnerPattern = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,38})$`)
	cloneNamePattern  = regexp.MustCompile(`^[A-Za-z0-9._-]{1,100}$`)

	// ErrInvalidCloneURL reports a clone URL that is not an HTTPS GitHub
	// repository URL built by GitHubCloneURL.
	ErrInvalidCloneURL = errors.New("not a github.com repository URL")
	// ErrCloneTargetExists reports a target directory that is not empty.
	ErrCloneTargetExists = errors.New("the target directory exists and is not empty")
)

// GitHubCloneURL is the only form of URL Clone accepts: the HTTPS URL of a
// github.com repository named by a validated owner/name. The ".git" suffix
// makes the credential's scope a prefix of every request git sends.
func GitHubCloneURL(repository string) (string, error) {
	owner, name, ok := strings.Cut(repository, "/")
	if !ok || !cloneOwnerPattern.MatchString(owner) || !cloneNamePattern.MatchString(name) || name == "." || name == ".." {
		return "", ErrInvalidCloneURL
	}
	return "https://github.com/" + owner + "/" + name + ".git", nil
}

// CloneOptions describe one clone.
type CloneOptions struct {
	// URL comes from GitHubCloneURL.
	URL string
	// Dir is the absolute target directory. It must not exist or be empty.
	Dir string
	// Token is the GitHub credential; empty clones anonymously.
	Token string
	// Timeout overrides DefaultCloneTimeout.
	Timeout time.Duration
}

// Test seams: a clone from a local repository, and the command runner.
var (
	allowFileCloneURL bool
	cloneRun          = run
)

// Clone clones a GitHub repository into opts.Dir. The token travels only in
// git's private environment, as for a fetch (see withCredential), never in
// arguments, files or the error. No hook, template, fsmonitor, submodule or
// protocol other than HTTPS runs. It creates missing parent directories
// (0755); when the clone fails, whatever it created, including the target, is
// removed. A target that is an existing empty directory is kept, emptied.
func Clone(ctx context.Context, opts CloneOptions) (err error) {
	u, err := url.Parse(opts.URL)
	if err != nil {
		return ErrInvalidCloneURL
	}
	testFile := allowFileCloneURL && u.Scheme == "file"
	if !testFile {
		if u.Scheme != "https" || u.Host != "github.com" || u.User != nil || u.RawQuery != "" || u.Fragment != "" ||
			!strings.HasSuffix(u.Path, ".git") {
			return ErrInvalidCloneURL
		}
		want, werr := GitHubCloneURL(strings.TrimSuffix(strings.TrimPrefix(u.Path, "/"), ".git"))
		if werr != nil || want != opts.URL {
			return ErrInvalidCloneURL
		}
	}
	if !filepath.IsAbs(opts.Dir) || filepath.Clean(opts.Dir) != opts.Dir {
		return fmt.Errorf("clone target must be a clean absolute path")
	}
	created, existed, err := prepareCloneTarget(opts.Dir)
	if err != nil {
		return err
	}
	hooks, err := os.MkdirTemp("", "baum-nohooks-")
	if err != nil {
		return fmt.Errorf("preparing clone: %w", err)
	}
	defer func() {
		_ = os.RemoveAll(hooks)
		if err != nil {
			removeCreated(opts.Dir, existed, created)
		}
	}()

	env := userEnv()
	if opts.Token != "" && !testFile {
		env = withCredential(env, u, opts.Token)
	}
	config := []string{
		"core.hooksPath=" + hooks, "protocol.ext.allow=never", "protocol.allow=never", "protocol.https.allow=always",
		"core.fsmonitor=false", "http.followRedirects=false", "gc.auto=0", "maintenance.auto=false",
		"submodule.recurse=false",
	}
	if testFile {
		config = append(config, "protocol.file.allow=always")
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = DefaultCloneTimeout
	}
	_, err = cloneRun(ctx, runSpec{dir: filepath.Dir(opts.Dir), env: env, timeout: timeout, config: config},
		"clone", "--no-recurse-submodules", "--template=", "--", opts.URL, opts.Dir)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if opts.Token != "" {
			return errors.New("GitHub HTTPS clone failed; check repository access, organization SSO authorization and GitHub connection")
		}
		return err
	}
	return nil
}

// prepareCloneTarget creates the target's missing parents. It reports whether
// the target already existed (as an empty directory) and the directories it
// created, outermost first.
func prepareCloneTarget(dir string) (created []string, existed bool, err error) {
	info, statErr := os.Lstat(dir)
	switch {
	case statErr == nil:
		if !info.IsDir() {
			return nil, false, ErrCloneTargetExists
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			return nil, false, fmt.Errorf("reading clone target: %w", err)
		}
		if len(entries) > 0 {
			return nil, false, ErrCloneTargetExists
		}
		return nil, true, nil
	case !errors.Is(statErr, os.ErrNotExist):
		return nil, false, fmt.Errorf("checking clone target: %w", statErr)
	}
	var missing []string
	for p := filepath.Dir(dir); ; p = filepath.Dir(p) {
		if _, err := os.Lstat(p); err == nil {
			break
		}
		missing = append([]string{p}, missing...)
		if filepath.Dir(p) == p {
			break
		}
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return nil, false, fmt.Errorf("creating clone folder: %w", err)
	}
	return missing, false, nil
}

// removeCreated undoes a failed clone: the target, or only its contents if it
// was an existing empty directory, then the parents created for it as far as
// they are empty.
func removeCreated(dir string, existed bool, parents []string) {
	if existed {
		entries, _ := os.ReadDir(dir)
		for _, e := range entries {
			_ = os.RemoveAll(filepath.Join(dir, e.Name()))
		}
		return
	}
	_ = os.RemoveAll(dir)
	for i := len(parents) - 1; i >= 0; i-- {
		if os.Remove(parents[i]) != nil {
			return
		}
	}
}
