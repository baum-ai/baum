//go:build unix

package decisionlog

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/baum-ai/baum/internal/gitlocal"
	"github.com/baum-ai/baum/internal/locallink/linktest"
)

// A build whose fetch cannot sign in over SSH (the key needs a passphrase
// nobody can type) stops with git_auth_ssh, keeping the sign-in message.
func TestBuildStopsWhenTheFetchCannotSignIn(t *testing.T) {
	f := newFeatureFixture(t)
	f.pull(1, "Change", f.clock.now.Add(-time.Hour), strings.Repeat("a", 40), nil)
	f.exec(`UPDATE pull_request SET base_sha=? WHERE number=1`, strings.Repeat("b", 40))
	clone := linktest.New(t).Repo
	linktest.Git(t, clone, "remote", "set-url", "origin", "ssh://git@example.invalid/"+repo+".git")
	ssh := filepath.Join(t.TempDir(), "ssh")
	if err := os.WriteFile(ssh, []byte("#!/bin/sh\necho 'git@example.invalid: Permission denied (publickey).' >&2\nexit 255\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_SSH_COMMAND", ssh)
	report, err := f.worker.Build(context.Background(), repo, clone, BuildOptions{})
	if _, ok := errors.AsType[*gitlocal.AuthError](err); !ok || report.Stopped != ReasonGitAuthSSH {
		t.Fatalf("report = %+v, err = %v", report, err)
	}
	if _, stopped := f.versionState(report.Version); stopped != ReasonGitAuthSSH {
		t.Fatalf("version stopped = %q", stopped)
	}
}
