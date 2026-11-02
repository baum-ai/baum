//go:build unix

package claude

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
		{"claude.ai", `echo '{"loggedIn":true,"authMethod":"claude.ai"}'`, true, true},
		{"signed out", `echo '{"loggedIn":false,"authMethod":"none"}'; exit 1`, false, true},
		{"no auth status", "echo 'error: unknown command' >&2; exit 1", false, false},
	} {
		binary := filepath.Join(t.TempDir(), "claude")
		if err := os.WriteFile(binary, []byte("#!/bin/sh\n"+c.script+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		if signedIn, known := SignedIn(context.Background(), binary); signedIn != c.signedIn || known != c.known {
			t.Errorf("%s: SignedIn = %v, %v", c.name, signedIn, known)
		}
	}
}

// A provider key in baum's environment never reaches a run, so it does not
// count as a sign-in.
func TestSignedInIgnoresAProviderKeyInTheEnvironment(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-test")
	binary := filepath.Join(t.TempDir(), "claude")
	script := `if [ -n "$ANTHROPIC_API_KEY" ]; then echo '{"loggedIn":true}'; else echo '{"loggedIn":false}'; exit 1; fi`
	if err := os.WriteFile(binary, []byte("#!/bin/sh\n"+script+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if signedIn, known := SignedIn(context.Background(), binary); signedIn || !known {
		t.Errorf("SignedIn = %v, %v", signedIn, known)
	}
}
