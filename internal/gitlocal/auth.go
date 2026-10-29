package gitlocal

import (
	"cmp"
	"context"
	"errors"
	"strings"
)

// AuthError is a fetch the remote refused because signing in needed an
// answer nobody can give: git runs with GIT_TERMINAL_PROMPT=0 and, in the
// daemon, SSH in batch mode, so a key passphrase or a password is never
// asked for. The user has to make the credential available without one.
type AuthError struct {
	// Host is the remote's host, "" when its URL names none.
	Host string
	// SSH is true for an SSH remote, false for HTTPS.
	SSH bool
	Err error
}

func (e *AuthError) Error() string {
	host := cmp.Or(e.Host, "the remote")
	if e.SSH {
		return "baum couldn't sign in to " + host + " over SSH. Background fetches can't ask for a key passphrase, so add your key to your SSH agent (ssh-add) and try again."
	}
	return "baum couldn't sign in to " + host + " over HTTPS. Background fetches can't ask for a password, so connect GitHub in Settings or save the credential in a Git credential helper, and try again."
}

func (e *AuthError) Unwrap() error { return e.Err }

// authFailure returns err as an *AuthError when git's output (in the C
// locale) says signing in to remote needed an answer, and err otherwise.
func (r *Repo) authFailure(ctx context.Context, remote string, err error) error {
	gitErr, ok := errors.AsType[*Error](err)
	if ctx.Err() != nil || !ok {
		return err
	}
	publickey := strings.Contains(gitErr.Stderr, "Permission denied (publickey")
	if !publickey && !strings.Contains(gitErr.Stderr, "terminal prompts disabled") {
		return err
	}
	fetchURL, urlErr := r.RemoteFetchURL(ctx, remote)
	if urlErr != nil || sshRemote(fetchURL) != publickey {
		return err
	}
	host, _, _ := RemoteRepository(fetchURL)
	return &AuthError{Host: host, SSH: publickey, Err: err}
}
