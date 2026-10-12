package sessions

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// TestLoadIgnoresTheUsersGitConfiguration reads a checkpoint branch whose tree
// asks for a filter, with a global configuration that defines that filter and
// asks git to verify signatures. The branch comes from a clone nobody has
// vetted, so neither the filter's command nor the signature program may run.
func TestLoadIgnoresTheUsersGitConfiguration(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the markers are made by shell scripts")
	}
	fx := newFixture(t)
	marks := t.TempDir()
	smudged, verified := filepath.Join(marks, "SMUDGE_RAN"), filepath.Join(marks, "GPG_RAN")

	// The checkpoint tree names a filter, and its tip is a commit that carries
	// a signature, which log verifies when asked to show signatures.
	git(t, fx.repo, "checkout", "-q", entireBranch)
	write(t, filepath.Join(fx.repo, ".gitattributes"), "* filter=x\n")
	git(t, fx.repo, "add", ".")
	git(t, fx.repo, "commit", "-q", "-m", "attributes")
	tree := gitOutput(t, fx.repo, "rev-parse", "HEAD^{tree}")
	parent := gitOutput(t, fx.repo, "rev-parse", "HEAD")
	commit := fmt.Sprintf("tree %s\nparent %s\nauthor T <t@example.com> 1700000000 +0000\ncommitter T <t@example.com> 1700000000 +0000\n"+
		"gpgsig -----BEGIN PGP SIGNATURE-----\n \n abcd\n -----END PGP SIGNATURE-----\n\nsigned\n", tree, parent)
	commitFile := filepath.Join(marks, "commit")
	write(t, commitFile, commit)
	signed := gitOutput(t, fx.repo, "hash-object", "-t", "commit", "-w", "--literally", commitFile)
	git(t, fx.repo, "update-ref", "refs/heads/"+entireBranch, signed)
	git(t, fx.repo, "checkout", "-q", "main")

	gpg := filepath.Join(marks, "gpg.sh")
	write(t, gpg, "#!/bin/sh\ntouch "+verified+"\nexit 1\n")
	if err := os.Chmod(gpg, 0o755); err != nil {
		t.Fatal(err)
	}
	global := filepath.Join(marks, "global")
	write(t, global, fmt.Sprintf("[filter \"x\"]\n\tsmudge = touch %s; cat\n[log]\n\tshowSignature = true\n[gpg]\n\tprogram = %s\n", smudged, gpg))
	t.Setenv("GIT_CONFIG_GLOBAL", global)
	t.Setenv("HOME", marks)

	opts := fx.options()
	opts.Since = time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC) // dates the checkpoints with git log
	found, report, err := Load(context.Background(), fx.repo, opts)
	if err != nil {
		t.Fatal(err)
	}
	for _, mark := range []string{smudged, verified} {
		if _, err := os.Stat(mark); err == nil {
			t.Errorf("%s exists: a command from the user's global configuration ran", filepath.Base(mark))
		}
	}
	entireSessions := 0
	for _, s := range found {
		if s.Source == Entire {
			entireSessions++
		}
	}
	if entireSessions == 0 || len(report.Failures) != 0 {
		t.Errorf("the checkpoint must still parse: %d Entire sessions, failures %+v", entireSessions, report.Failures)
	}
}
