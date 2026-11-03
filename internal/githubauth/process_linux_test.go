package githubauth

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// An update renames a new baum over the daemon's path while the daemon keeps
// running the old one. Until the daemon restarts, its credential helper must
// still recognise it as the same executable.
func TestCredentialHelperAfterExecutableReplaced(t *testing.T) {
	if os.Getenv("BAUM_TEST_CREDENTIAL_REPLACED") == "daemon" {
		self, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		copyExecutable(t, self, self+".new")
		if err := os.Rename(self+".new", self); err != nil {
			t.Fatal(err)
		}
		helper, err := selfExecutable()
		if err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(helper, "-test.run=^TestCredentialHelperParent$")
		cmd.Env = append(os.Environ(), "BAUM_TEST_CREDENTIAL_PARENT=allow")
		cmd.Stdin = strings.NewReader("private request")
		var output bytes.Buffer
		cmd.Stdout, cmd.Stderr = &output, &output
		if err := cmd.Run(); err != nil {
			t.Fatal("helper refused the daemon after its executable was replaced: ", err, output.String())
		}
		return
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	daemon := filepath.Join(t.TempDir(), "baum")
	copyExecutable(t, executable, daemon)
	cmd := exec.Command(daemon, "-test.run=^TestCredentialHelperAfterExecutableReplaced$")
	cmd.Env = append(os.Environ(), "BAUM_TEST_CREDENTIAL_REPLACED=daemon")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatal(err, string(output))
	}
}

func copyExecutable(t *testing.T, from, to string) {
	t.Helper()
	b, err := os.ReadFile(from)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(to, b, 0o700); err != nil {
		t.Fatal(err)
	}
}
