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
	urlRun            = run
)

// ErrURLRewritten reports a Git configuration (url.<base>.insteadOf) that
// changes the requested clone URL, including to another GitHub repository.
var ErrURLRewritten = errors.New("your Git configuration changes the requested GitHub clone URL; remove the rewrite or choose a folder with an existing clone")

// Clone clones a GitHub repository into opts.Dir. The token travels only in
// git's private environment, as for a fetch (see withCredential), never in
// arguments, files or the error. No hook, template, fsmonitor, submodule or
// protocol other than HTTPS runs, and with a token certificate verification
// is forced on. It creates missing parent directories (0755) and the target
// itself atomically. A failed clone removes only what this call created: the
// target when it is empty or holds a .git git left, and the parents it made
// as far as they are empty. A target that already existed (empty) is never
// removed or emptied by baum; git cleans up its own partial work there.
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
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = DefaultCloneTimeout
	}

	env := userEnv()
	if opts.Token != "" {
		env = withCredential(env, u, opts.Token)
		// A user's GIT_SSL_NO_VERIFY must not weaken the connection that
		// carries the token.
		kept := env[:0:0]
		for _, e := range env {
			if !strings.HasPrefix(strings.ToUpper(e), "GIT_SSL_NO_VERIFY=") {
				kept = append(kept, e)
			}
		}
		env = kept
	}
	if err := checkNotRewritten(ctx, env, opts.URL); err != nil {
		return err
	}

	created, existed, err := prepareCloneTarget(opts.Dir)
	if err != nil {
		return err
	}
	hooks, err := os.MkdirTemp("", "baum-nohooks-")
	if err != nil {
		removeCreated(opts.Dir, existed, created)
		return fmt.Errorf("preparing clone: %w", err)
	}
	defer func() {
		_ = os.RemoveAll(hooks)
		if err != nil {
			removeCreated(opts.Dir, existed, created)
		}
	}()

	config := []string{
		"core.hooksPath=" + hooks, "protocol.ext.allow=never", "protocol.allow=never", "protocol.https.allow=always",
		"core.fsmonitor=false", "http.followRedirects=false", "gc.auto=0", "maintenance.auto=false",
		"submodule.recurse=false",
	}
	if opts.Token != "" {
		config = append(config, "http.sslVerify=true", "http."+opts.URL+".sslVerify=true")
	}
	if testFile {
		config = append(config, "protocol.file.allow=always")
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

// checkNotRewritten fails with ErrURLRewritten when the user's configuration
// changes rawURL, even if the replacement is another HTTPS github.com URL.
func checkNotRewritten(ctx context.Context, env []string, rawURL string) error {
	out, err := urlRun(ctx, runSpec{dir: os.TempDir(), env: env, limit: 4 << 10, timeout: 30 * time.Second}, "ls-remote", "--get-url", rawURL)
	if err != nil {
		// Not being able to ask is not a reason to refuse; the clone reports its own failure.
		return nil
	}
	got := strings.TrimSpace(string(out))
	if got == rawURL {
		return nil
	}
	return ErrURLRewritten
}

// prepareCloneTarget creates the target's missing parents and then the target
// itself with os.Mkdir, which fails if something is there. A target that
// already exists must be an empty directory; existed reports that. created are
// the parents it made, outermost first.
func prepareCloneTarget(dir string) (created []string, existed bool, err error) {
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
	switch err := os.Mkdir(dir, 0o755); {
	case err == nil:
		return missing, false, nil
	case !errors.Is(err, os.ErrExist):
		removeParents(missing)
		return nil, false, fmt.Errorf("creating clone folder: %w", err)
	}
	info, statErr := os.Lstat(dir)
	if statErr != nil || !info.IsDir() {
		return nil, false, ErrCloneTargetExists
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, false, fmt.Errorf("reading clone target: %w", err)
	}
	if len(entries) > 0 {
		return nil, false, ErrCloneTargetExists
	}
	return missing, true, nil
}

func removeParents(parents []string) {
	for i := len(parents) - 1; i >= 0; i-- {
		if os.Remove(parents[i]) != nil {
			return
		}
	}
}

// removeCreated undoes a failed clone. It never removes a directory baum did
// not create, or anything git did not write: a target baum made is removed
// whole only when git left a .git in it, otherwise only if still empty (a
// file someone put there is kept); a target that existed is left alone.
func removeCreated(dir string, existed bool, parents []string) {
	if existed {
		return
	}
	if _, err := os.Lstat(filepath.Join(dir, ".git")); err == nil {
		_ = os.RemoveAll(dir)
	} else {
		_ = os.Remove(dir)
	}
	if _, err := os.Lstat(dir); err == nil {
		return
	}
	removeParents(parents)
}
