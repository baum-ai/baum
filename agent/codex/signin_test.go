//go:build unix

package codex

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestSignedIn(t *testing.T) {
	for _, c := range []struct {
		name, script    string
		signedIn, known bool
	}{
		{"ChatGPT", "echo 'Logged in using ChatGPT'", true, true},
		{"signed out", "echo 'Not logged in' >&2; exit 1", false, true},
		{"unknown command", "echo 'error: unrecognized subcommand' >&2; exit 2", false, false},
	} {
		binary := filepath.Join(t.TempDir(), "codex")
		if err := os.WriteFile(binary, []byte("#!/bin/sh\n"+c.script+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		if signedIn, known := SignedIn(context.Background(), binary); signedIn != c.signedIn || known != c.known {
			t.Errorf("%s: SignedIn = %v, %v", c.name, signedIn, known)
		}
	}
}
