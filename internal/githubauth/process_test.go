package githubauth

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestCredentialHelperParent(t *testing.T) {
	if mode := os.Getenv("BAUM_TEST_CREDENTIAL_PARENT"); mode != "" {
		if mode == "allow" {
			if err := authorizeCredentialHelper(); err != nil {
				t.Fatal("same-executable parent with private pipes refused", err)
			}
		} else {
			// Refusal must happen before even reading an operation or opening the
			// keyring, regardless of whether it asks to load, save or delete.
			if err := ServeCredentialHelper(context.Background(), unreadableRequest{}, io.Discard); err != ErrStore {
				t.Fatal("foreign parent accepted", err)
			}
		}
		return
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(executable, "-test.run=^TestCredentialHelperParent$")
	cmd.Env = append(os.Environ(), "BAUM_TEST_CREDENTIAL_PARENT=allow")
	cmd.Stdin = strings.NewReader("private request")
	var output bytes.Buffer
	cmd.Stdout, cmd.Stderr = &output, &output
	if err := cmd.Run(); err != nil {
		t.Fatal(err, output.String())
	}
}

type unreadableRequest struct{}

func (unreadableRequest) Read([]byte) (int, error) {
	panic("unauthenticated credential helper read a request")
}
