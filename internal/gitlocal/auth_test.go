//go:build unix

package gitlocal

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A fetch that cannot sign in because the credential needs an answer
// returns one AuthError naming the host and transport; other failures keep
// git's error.
func TestFetchSignInFailures(t *testing.T) {
	asks := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("WWW-Authenticate", `Basic realm="git"`)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(asks.Close)
	for _, tc := range []struct {
		name, url, ssh string
		want           *AuthError
	}{
		{"SSH key needs a passphrase", "git@example.invalid:acme/demo.git",
			"echo 'git@example.invalid: Permission denied (publickey).' >&2", &AuthError{Host: "example.invalid", SSH: true}},
		{"SSH host unreachable", "ssh://git@example.invalid/acme/demo.git",
			"echo 'ssh: connect to host example.invalid port 22: Connection refused' >&2", nil},
		// HTTPS's pattern from an SSH remote is not its sign-in failure.
		{"SSH with the other transport's output", "ssh://git@example.invalid/acme/demo.git",
			"echo 'fatal: terminal prompts disabled' >&2", nil},
		{"HTTPS asks for a password", asks.URL + "/acme/demo.git", "", &AuthError{Host: "127.0.0.1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newRemoteFixture(t)
			git(t, f.clone, "remote", "set-url", "origin", tc.url)
			dir := t.TempDir()
			write(t, dir, "ssh", "#!/bin/sh\n"+tc.ssh+"\nexit 255\n", 0o755)
			t.Setenv("GIT_SSH_COMMAND", filepath.Join(dir, "ssh"))
			r := open(t, f.clone, Options{})
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			err := r.FetchPulls(ctx, "origin", []PullRequest{{Number: 7, BaseSHA: f.base, HeadSHA: f.head, BaseRef: "main"}})
			auth, ok := errors.AsType[*AuthError](err)
			switch {
			case tc.want == nil:
				if err == nil || ok {
					t.Fatalf("err = %v, want git's error", err)
				}
			case !ok || auth.Host != tc.want.Host || auth.SSH != tc.want.SSH:
				t.Fatalf("err = %#v, want %+v", err, tc.want)
			case strings.Count(err.Error(), "couldn't sign in") != 1:
				t.Fatalf("message = %q, want the sign-in message once", err)
			}
		})
	}
}
